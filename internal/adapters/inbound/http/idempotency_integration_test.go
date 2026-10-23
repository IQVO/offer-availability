//go:build integration

package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/clock"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/offer-availability/internal/adapters/outbound/kafka"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// pgRig is the REST server over REAL Postgres with the real Idempotency-Key
// middleware: the keyed routes, the aggregates and the outbox rows commit or roll
// back together.
type pgRig struct {
	t       *testing.T
	pool    *pgtest.Pool
	handler http.Handler
}

func newPGRig(t *testing.T) *pgRig {
	t.Helper()
	pool := pgtest.NewPool(t)
	outbox := postgres.NewOutboxRepo(pool)
	d := usecases.Deps{
		Catalog: postgres.NewCatalogRepo(pool), Listings: postgres.NewListingRepo(pool), Feeds: postgres.NewFeedRepo(pool),
		Marks: postgres.NewNodeFeedMarks(pool), Nodes: postgres.NewNodeStatusRepo(pool), Inbox: postgres.NewProcessedEventRepo(pool),
		Outbox: postgres.NewEventOutbox(outbox, outboundkafka.NewEncoder()), UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: ids.UUID{},
	}
	h, err := NewRouter(&Server{
		CreateCatalogItem: usecases.NewCreateCatalogItem(d), DiscontinueItem: usecases.NewDiscontinueItem(d),
		BrowseCatalog: usecases.NewBrowseCatalog(d.Catalog), GetCatalogItem: usecases.NewGetCatalogItem(d.Catalog),
		CreateListing: usecases.NewCreateListing(d), ListListings: usecases.NewListListings(d.Listings),
		ApplyInventoryFeed: usecases.NewApplyInventoryFeed(d), GetInventoryFeed: usecases.NewGetInventoryFeed(d.Feeds),
		Idempotency: RequireIdempotencyKey(pool, pgtx.With), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &pgRig{t: t, pool: pool, handler: h}
}

func (r *pgRig) do(method, target, key, body string) response {
	r.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return response{code: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

func (r *pgRig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

const itemBody = `{"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340}`

func TestKeyIsRequiredAndValidated(t *testing.T) {
	r := newPGRig(t)
	r.do(http.MethodPost, "/catalog-items", "", itemBody).problem(t, http.StatusBadRequest, "idempotency-key-required")
	r.do(http.MethodPost, "/catalog-items", strings.Repeat("k", MaxIdempotencyKeyLength+1), itemBody).
		problem(t, http.StatusBadRequest, "idempotency-key-invalid")
	r.do(http.MethodPost, "/catalog-items", "bad\tkey", itemBody).problem(t, http.StatusBadRequest, "idempotency-key-invalid")
	r.do(http.MethodPost, "/catalog-items", "k", strings.Repeat("a", 70<<10)).problem(t, http.StatusRequestEntityTooLarge, "request-body-too-large")
	if n := r.count(`SELECT count(*) FROM idempotency_keys`); n != 0 {
		t.Fatalf("a refused request recorded %d keys", n)
	}
}

func TestSameKeySameBodyReplaysTheFirstAnswerAndDoesTheWorkOnce(t *testing.T) {
	r := newPGRig(t)
	first := r.do(http.MethodPost, "/catalog-items", "key-1", itemBody)
	again := r.do(http.MethodPost, "/catalog-items", "key-1", itemBody)
	if first.code != http.StatusCreated || again.code != http.StatusCreated || string(first.body) != string(again.body) {
		t.Fatalf("first %d %s, again %d %s", first.code, first.body, again.code, again.body)
	}
	if first.header.Get(IdempotencyReplayedHeader) != "" || again.header.Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replayed headers: %q, %q", first.header.Get(IdempotencyReplayedHeader), again.header.Get(IdempotencyReplayedHeader))
	}
	if items, events := r.count(`SELECT count(*) FROM catalog_items`), r.count(`SELECT count(*) FROM outbox_events`); items != 1 || events != 1 {
		t.Fatalf("%d items, %d outbox rows, want 1 and 1", items, events)
	}
}

func TestSameKeyOtherBodyOrOtherPathIsReused422(t *testing.T) {
	r := newPGRig(t)
	r.do(http.MethodPost, "/catalog-items", "key-2", itemBody)
	r.do(http.MethodPost, "/catalog-items", "key-2", strings.Replace(itemBody, "Bottle", "Mug", 1)).
		problem(t, http.StatusUnprocessableEntity, "idempotency-key-reused")
	// The route is part of the scope: the same key on another route is another request.
	if res := r.do(http.MethodPost, "/listings", "key-2", `{"catalogItemId":"x","nodeId":"n"}`); res.code != http.StatusNotFound {
		t.Fatalf("another route, same key: %d %s", res.code, res.body)
	}
}

func TestA4xxIsStoredAndItsWritesAreDropped(t *testing.T) {
	r := newPGRig(t)
	bad := strings.Replace(itemBody, "270", "0", 1)
	first := r.do(http.MethodPost, "/catalog-items", "key-3", bad)
	again := r.do(http.MethodPost, "/catalog-items", "key-3", bad)
	first.problem(t, http.StatusUnprocessableEntity, "invalid-item-dimensions")
	again.problem(t, http.StatusUnprocessableEntity, "invalid-item-dimensions")
	if again.header.Get("Content-Type") != "application/problem+json" || again.header.Get(IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replayed problem: %v", again.header)
	}
	if r.count(`SELECT count(*) FROM catalog_items`) != 0 || r.count(`SELECT count(*) FROM outbox_events`) != 0 {
		t.Fatal("a 4xx leaves nothing behind")
	}
}

func TestAFailedFeedIsKeptAlthoughItAnswers422(t *testing.T) {
	r := newPGRig(t)
	ctx := context.Background()
	if _, err := r.pool.Exec(ctx, `INSERT INTO node_status_view (node_id, status, status_at, version) VALUES ('node-1', 'ACTIVE', now(), 1)`); err != nil {
		t.Fatal(err)
	}
	item := r.do(http.MethodPost, "/catalog-items", "k-item", itemBody).json()["catalogItemId"].(string)
	lst := r.do(http.MethodPost, "/listings", "k-lst", `{"catalogItemId":"`+item+`","nodeId":"node-1"}`).json()["listingId"].(string)
	body := feedBody("bad", "node-1", "2026-10-23T10:00:00Z", false, line(lst, -5, "PHYSICAL"))

	first := r.do(http.MethodPost, "/inventory-feeds", "k-feed", body)
	first.problem(t, http.StatusUnprocessableEntity, "negative-availability")
	if n := r.count(`SELECT count(*) FROM inventory_feeds WHERE feed_id = 'bad' AND state = 'FAILED' AND failure_code = 'ErrNegativeAvailability'`); n != 1 {
		t.Fatalf("the FAILED feed is stored: %d rows", n)
	}
	if got := r.do(http.MethodGet, "/inventory-feeds/bad", "", ""); got.code != http.StatusOK || got.json()["state"] != "FAILED" {
		t.Fatalf("GET: %d %s", got.code, got.body)
	}
	again := r.do(http.MethodPost, "/inventory-feeds", "k-feed", body)
	again.problem(t, http.StatusUnprocessableEntity, "negative-availability")
	if again.header.Get(IdempotencyReplayedHeader) != "true" {
		t.Fatal("the 422 is replayed from the key")
	}
	// A FAILED feed id may be sent again under a NEW key (G23).
	ok := r.do(http.MethodPost, "/inventory-feeds", "k-feed-2", feedBody("bad", "node-1", "2026-10-23T10:00:00Z", false, line(lst, 5, "PHYSICAL")))
	if ok.code != http.StatusCreated || ok.json()["state"] != "APPLIED" {
		t.Fatalf("resend: %d %s", ok.code, ok.body)
	}
}

func TestA5xxIsNeverStoredSoTheKeyCanRetry(t *testing.T) {
	r := newPGRig(t)
	var calls atomic.Int32
	fail := true
	mw := RequireIdempotencyKey(r.pool, pgtx.With)("POST /boom")(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if fail {
			writeError(w, req, io.ErrUnexpectedEOF)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"ok": "yes"})
	}))
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/boom", strings.NewReader("{}"))
		req.Header.Set(IdempotencyKeyHeader, "key-5")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(); code != http.StatusInternalServerError {
		t.Fatalf("first: %d", code)
	}
	if r.count(`SELECT count(*) FROM idempotency_keys`) != 0 {
		t.Fatal("a 5xx must not be stored")
	}
	fail = false
	if code := send(); code != http.StatusCreated || calls.Load() != 2 {
		t.Fatalf("retry: %d after %d calls", code, calls.Load())
	}
}

func TestAPanicRollsTheKeyBack(t *testing.T) {
	r := newPGRig(t)
	mw := RequireIdempotencyKey(r.pool, pgtx.With)("POST /panic")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	req := httptest.NewRequest(http.MethodPost, "/panic", strings.NewReader("{}"))
	req.Header.Set(IdempotencyKeyHeader, "key-6")
	func() {
		defer func() { _ = recover() }()
		mw.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if r.count(`SELECT count(*) FROM idempotency_keys`) != 0 {
		t.Fatal("a panic must not leave a key behind")
	}
}

func TestConcurrentRequestsWithOneKeyDoTheWorkOnce(t *testing.T) {
	r := newPGRig(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = r.do(http.MethodPost, "/catalog-items", "key-7", itemBody).code
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("codes = %v", codes)
		}
	}
	if items := r.count(`SELECT count(*) FROM catalog_items`); items != 1 {
		t.Fatalf("%d items for one key", items)
	}
}

func TestExpiredKeysArePurgedByTheNextKeyedWrite(t *testing.T) {
	r := newPGRig(t)
	r.do(http.MethodPost, "/catalog-items", "old-key", itemBody)
	if _, err := r.pool.Exec(context.Background(), `UPDATE idempotency_keys SET created_at = now() - interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	r.do(http.MethodPost, "/catalog-items", "old-key", itemBody)
	if items := r.count(`SELECT count(*) FROM catalog_items`); items != 2 {
		t.Fatalf("an expired key is a new key: %d items", items)
	}
}

func TestDiscontinueRouteWithColonVerbOverPostgres(t *testing.T) {
	r := newPGRig(t)
	item := r.do(http.MethodPost, "/catalog-items", "a", itemBody).json()["catalogItemId"].(string)
	res := r.do(http.MethodPost, "/catalog-items/"+item+":discontinue", "b", "")
	if res.code != http.StatusOK || res.json()["state"] != "DISCONTINUED" {
		t.Fatalf("%d %s", res.code, res.body)
	}
}
