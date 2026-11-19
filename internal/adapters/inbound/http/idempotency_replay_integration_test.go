//go:build integration

package http

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

// The replay-class tests over REAL Postgres and the real router (ADR 0016): a
// RETRYABLE refusal leaves no key row, so the same key runs the work again.

// racingCatalog stands for a competing writer: on each of its first two Gets it
// commits a new version of the item from ANOTHER connection, so the Save of the
// request that read the old version is refused ports.ErrConcurrentUpdate (the
// use case re-runs once, ADR 0002 A15, so one race alone is absorbed and two are
// reported). When release is set the first Get waits on it (a second request is
// then queued behind the key row).
type racingCatalog struct {
	ports.CatalogRepository
	pool    *pgtest.Pool
	gets    atomic.Int32
	reading chan struct{}
	release chan struct{}
}

func (c *racingCatalog) Get(ctx context.Context, id catalog.ID) (*catalog.Item, error) {
	item, err := c.CatalogRepository.Get(ctx, id)
	n := c.gets.Add(1)
	if err != nil || n > 2 {
		return item, err
	}
	if n == 1 && c.release != nil {
		close(c.reading)
		<-c.release
	}
	_, err = c.pool.Exec(context.Background(), `UPDATE catalog_items SET version = version + 1 WHERE catalog_item_id = $1`, string(id))
	return item, err
}

func newRacingRig(t *testing.T, gated bool) (*pgRig, *racingCatalog) {
	t.Helper()
	race := &racingCatalog{}
	r := newPGRigWith(t, func(d *usecases.Deps, pool *pgtest.Pool) {
		race.CatalogRepository, race.pool = d.Catalog, pool
		if gated {
			race.reading, race.release = make(chan struct{}), make(chan struct{})
		}
		d.Catalog = race
	})
	return r, race
}

func (r *pgRig) keyRows(key string) int {
	r.t.Helper()
	return r.count(`SELECT count(*) FROM idempotency_keys WHERE key = $1`, key)
}

// concurrent-update is RETRYABLE: the loser's key row is absent, the retry under
// the same key executes, and its success is then stored and replayed.
func TestConcurrentUpdateRefusalIsNotStoredSoItsKeyRetriesTheWork(t *testing.T) {
	r, _ := newRacingRig(t, false)
	item := r.do(http.MethodPost, "/catalog-items", "k-item", itemBody).json()["catalogItemId"].(string)
	target := "/catalog-items/" + item + "/discontinuations"

	lost := r.do(http.MethodPost, target, "k-race", "")
	lost.problem(t, http.StatusConflict, "concurrent-update")
	if n := r.keyRows("k-race"); n != 0 {
		t.Fatalf("the loser's key row: %d, want none (the refusal is not stored)", n)
	}
	retry := r.do(http.MethodPost, target, "k-race", "")
	if retry.code != http.StatusOK || retry.json()["state"] != "DISCONTINUED" || retry.header.Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("retry = %d %s %v, want a fresh 200 DISCONTINUED", retry.code, retry.body, retry.header)
	}
	if n := r.keyRows("k-race"); n != 1 {
		t.Fatalf("key rows after the success: %d, want 1", n)
	}
	again := r.do(http.MethodPost, target, "k-race", "")
	if again.code != http.StatusOK || again.header.Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("third = %d %v, want the stored 200 replayed", again.code, again.header)
	}
	if n := r.count(`SELECT count(*) FROM outbox_events WHERE event_type LIKE '%CatalogItemDiscontinued'`); n != 1 {
		t.Fatalf("%d CatalogItemDiscontinued events, want 1", n)
	}
}

// A duplicate that arrives while the first request is still running waits on the
// key row; when the first ends in a RETRYABLE refusal the key is released, so the
// duplicate executes the work itself (it is not answered 409 and not replayed).
func TestConcurrentDuplicateOfAReleasedRefusalExecutesTheWork(t *testing.T) {
	r, race := newRacingRig(t, true)
	item := r.do(http.MethodPost, "/catalog-items", "k-item", itemBody).json()["catalogItemId"].(string)
	target := "/catalog-items/" + item + "/discontinuations"

	first := make(chan response, 1)
	go func() { first <- r.do(http.MethodPost, target, "k-dup", "") }()
	<-race.reading
	second := make(chan response, 1)
	go func() { second <- r.do(http.MethodPost, target, "k-dup", "") }()
	waitForKeyWaiter(t, r)
	close(race.release)

	if got := <-first; got.code != http.StatusConflict {
		t.Fatalf("first = %d %s, want 409 concurrent-update", got.code, got.body)
	}
	got := <-second
	if got.code != http.StatusOK || got.header.Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("duplicate = %d %s %v, want a fresh 200 (it executed)", got.code, got.body, got.header)
	}
}

// waitForKeyWaiter blocks until a statement waits on a lock held by another
// transaction: the duplicate's INSERT queued behind the first request's key row.
func waitForKeyWaiter(t *testing.T, r *pgRig) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r.count(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'INSERT INTO idempotency_keys%'`) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the duplicate never queued behind the key row")
}

// pack-level-not-found is RETRYABLE: a PUT can declare the level later.
func TestPackLevelNotFoundRefusalReleasesItsKey(t *testing.T) {
	r := newPGRig(t)
	item := r.do(http.MethodPost, "/catalog-items", "k-item", itemBody).json()["catalogItemId"].(string)
	retire := "/catalog-items/" + item + "/packaging/EACH/retirements"

	r.do(http.MethodPost, retire, "k-ret", "").problem(t, http.StatusNotFound, "pack-level-not-found")
	if n := r.keyRows("k-ret"); n != 0 {
		t.Fatalf("key rows after the refusal: %d, want 0", n)
	}
	if put := r.do(http.MethodPut, "/catalog-items/"+item+"/packaging/EACH", "", `{"gtin":"06141410000120","unitsPerPack":1}`); put.code != http.StatusOK {
		t.Fatalf("declare: %d %s", put.code, put.body)
	}
	if retry := r.do(http.MethodPost, retire, "k-ret", ""); retry.code != http.StatusOK || retry.header.Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("retry = %d %s %v, want a fresh 200", retry.code, retry.body, retry.header)
	}
}

// listing-not-on-node is RETRYABLE and the FAILED feed it recorded stays: the
// GET shows FAILED, the key is free, and the same request under the same key,
// after the listing exists, applies the feed (Resubmit) and turns it APPLIED.
func TestListingNotOnNodeRefusalKeepsTheFailedFeedAndLetsTheSameKeyRetry(t *testing.T) {
	r := newPGRig(t)
	item, node := feedFixture(t, r)
	body := feedBody("late", node, "2026-10-23T10:00:00Z", false, line("lst-late", 5, "PHYSICAL"))

	first := r.do(http.MethodPost, "/inventory-feeds", "k-late", body)
	if p := first.problem(t, http.StatusUnprocessableEntity, "listing-not-on-node"); p["instance"] != "/inventory-feeds/late" {
		t.Fatalf("instance = %v, want the recorded feed", p["instance"])
	}
	if n := r.count(`SELECT count(*) FROM inventory_feeds WHERE feed_id = 'late' AND state = 'FAILED' AND failure_code = 'ErrListingNotOnNode'`); n != 1 {
		t.Fatalf("the FAILED feed row: %d, want 1", n)
	}
	if got := r.do(http.MethodGet, "/inventory-feeds/late", "", ""); got.code != http.StatusOK || got.json()["state"] != "FAILED" {
		t.Fatalf("GET after the refusal: %d %s, want the FAILED feed", got.code, got.body)
	}
	if n := r.keyRows("k-late"); n != 0 {
		t.Fatalf("key rows after the refusal: %d, want 0 (RETRYABLE: the key is released)", n)
	}

	r.insertListing("lst-late", item, node) // the event-fed listing arrives
	retry := r.do(http.MethodPost, "/inventory-feeds", "k-late", body)
	if retry.code != http.StatusCreated || retry.json()["state"] != "APPLIED" || retry.header.Get(IdempotencyReplayedHeader) != "" {
		t.Fatalf("retry = %d %s %v, want a fresh 201 APPLIED", retry.code, retry.body, retry.header)
	}
	if n, f := r.keyRows("k-late"), r.count(`SELECT count(*) FROM inventory_feeds WHERE feed_id = 'late'`); n != 1 || f != 1 {
		t.Fatalf("key rows %d, feed rows %d, want 1 and 1", n, f)
	}
	if got := r.do(http.MethodGet, "/inventory-feeds/late", "", ""); got.json()["state"] != "APPLIED" {
		t.Fatalf("GET after the retry: %s, want APPLIED", got.body)
	}
	if again := r.do(http.MethodPost, "/inventory-feeds", "k-late", body); again.code != http.StatusCreated || again.header.Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("third = %d %v, want the stored 201 replayed", again.code, again.header)
	}
}

// A still-failing retry re-evaluates the same feed id: one row, still FAILED.
func TestListingNotOnNodeRetryThatStillFailsLeavesOneFailedFeed(t *testing.T) {
	r := newPGRig(t)
	_, node := feedFixture(t, r)
	body := feedBody("late2", node, "2026-10-23T10:00:00Z", false, line("lst-never", 5, "PHYSICAL"))
	for i := 0; i < 2; i++ {
		r.do(http.MethodPost, "/inventory-feeds", "k-late2", body).problem(t, http.StatusUnprocessableEntity, "listing-not-on-node")
	}
	if n := r.count(`SELECT count(*) FROM inventory_feeds WHERE feed_id = 'late2' AND state = 'FAILED'`); n != 1 {
		t.Fatalf("FAILED feed rows: %d, want 1", n)
	}
	if n := r.count(`SELECT count(*) FROM outbox_events WHERE subject = 'late2'`); n != 0 {
		t.Fatalf("%d events for a FAILED feed, want 0", n)
	}
}

// negative-availability is an OUTCOME: the FAILED feed commits WITH the stored
// 422, a replay answers from the row and records nothing new.
func TestNegativeAvailabilityOutcomeCommitsOnceAndReplays(t *testing.T) {
	r := newPGRig(t)
	item, node := feedFixture(t, r)
	r.insertListing("lst-neg", item, node)
	body := feedBody("neg", node, "2026-10-23T10:00:00Z", false, line("lst-neg", -5, "PHYSICAL"))

	r.do(http.MethodPost, "/inventory-feeds", "k-neg", body).problem(t, http.StatusUnprocessableEntity, "negative-availability")
	version := r.count(`SELECT version FROM inventory_feeds WHERE feed_id = 'neg'`)
	for i := 0; i < 3; i++ {
		again := r.do(http.MethodPost, "/inventory-feeds", "k-neg", body)
		again.problem(t, http.StatusUnprocessableEntity, "negative-availability")
		if again.header.Get(IdempotencyReplayedHeader) != "true" {
			t.Fatalf("replay %d is not marked replayed", i)
		}
	}
	if n := r.keyRows("k-neg"); n != 1 {
		t.Fatalf("key rows: %d, want 1 (stored with the FAILED feed)", n)
	}
	if got := r.count(`SELECT version FROM inventory_feeds WHERE feed_id = 'neg' AND state = 'FAILED'`); got != version {
		t.Fatalf("feed version %d after replays, was %d: a replay records nothing new", got, version)
	}
}

// feedFixture makes node-1 ACTIVE and returns an item id and the node.
func feedFixture(t *testing.T, r *pgRig) (item, node string) {
	t.Helper()
	node = "node-1"
	if _, err := r.pool.Exec(context.Background(), `INSERT INTO node_status_view (node_id, status, status_at, version) VALUES ($1, 'ACTIVE', now(), 1)`, node); err != nil {
		t.Fatal(err)
	}
	item = r.do(http.MethodPost, "/catalog-items", "k-item", itemBody).json()["catalogItemId"].(string)
	return item, node
}

// insertListing stands for a listing the event consumers create (ADR 0014).
func (r *pgRig) insertListing(id, item, node string) {
	r.t.Helper()
	if _, err := r.pool.Exec(context.Background(),
		`INSERT INTO listings (listing_id, catalog_item_id, node_id, state, available_qty, basis, version) VALUES ($1, $2, $3, 'LIVE', 0, 'UNKNOWN', 1)`,
		id, item, node); err != nil {
		r.t.Fatal(err)
	}
}
