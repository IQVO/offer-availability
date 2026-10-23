//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/application/outbox"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

var errBroker = errors.New("broker down")

func collect(into *[]string) func(context.Context, outbox.Message) error {
	return func(_ context.Context, m outbox.Message) error {
		*into = append(*into, string(m.Key))
		return nil
	}
}

func TestDrainPublishesInIdOrderAndMarksRowsPublished(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	for _, k := range []string{"a", "b", "c"} {
		pgtest.InsertOutboxRow(t, pool, "com.example.T", k)
	}
	var sent []string
	n, err := repo.Drain(context.Background(), 2, 0, collect(&sent))
	if err != nil || n != 2 || len(sent) != 2 || sent[0] != "a" || sent[1] != "b" {
		t.Fatalf("first pass: %d %v %v", n, sent, err)
	}
	n, err = repo.Drain(context.Background(), 10, 0, collect(&sent))
	if err != nil || n != 1 || sent[2] != "c" {
		t.Fatalf("second pass: %d %v %v", n, sent, err)
	}
	if n, _ := repo.Drain(context.Background(), 10, 0, collect(&sent)); n != 0 {
		t.Fatalf("a published row is never claimed again: %d", n)
	}
}

func TestDrainStopsAtTheFirstFailureAndRecordsIt(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	first := pgtest.InsertOutboxRow(t, pool, "com.example.T", "a")
	second := pgtest.InsertOutboxRow(t, pool, "com.example.T", "b")
	third := pgtest.InsertOutboxRow(t, pool, "com.example.T", "c")

	calls := 0
	n, err := repo.Drain(context.Background(), 10, 0, func(_ context.Context, m outbox.Message) error {
		calls++
		if string(m.Key) == "b" {
			return errBroker
		}
		return nil
	})
	var sf *outbox.SendFailure
	if n != 1 || !errors.As(err, &sf) || sf.RowID != second || sf.Attempts != 1 || !errors.Is(err, errBroker) || string(sf.Message.Key) != "b" {
		t.Fatalf("pass: %d %v", n, err)
	}
	if calls != 2 {
		t.Fatalf("a later event must never overtake the failed one: %d sends", calls)
	}
	if _, _, published := pgtest.OutboxRowState(t, pool, first); !published {
		t.Fatal("the row sent before the failure is kept published")
	}
	if attempts, lastErr, published := pgtest.OutboxRowState(t, pool, second); published || attempts != 1 || lastErr != errBroker.Error() {
		t.Fatalf("failed row: %d %q %v", attempts, lastErr, published)
	}
	if attempts, _, published := pgtest.OutboxRowState(t, pool, third); published || attempts != 0 {
		t.Fatalf("untouched row: %d %v", attempts, published)
	}
	var sent []string
	if n, err := repo.Drain(context.Background(), 10, 0, collect(&sent)); err != nil || n != 2 || sent[0] != "b" {
		t.Fatalf("retry: %d %v %v", n, sent, err)
	}
	if attempts, lastErr, published := pgtest.OutboxRowState(t, pool, second); !published || attempts != 2 || lastErr != "" {
		t.Fatalf("after the retry: %d %q %v", attempts, lastErr, published)
	}
}

func TestARowAtTheAttemptCapIsExcludedAndLaterRowsOvertakeIt(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	stuck := pgtest.InsertOutboxRow(t, pool, "com.example.T", "stuck")
	pgtest.InsertOutboxRow(t, pool, "com.example.T", "next")
	fail := func(_ context.Context, m outbox.Message) error {
		if string(m.Key) == "stuck" {
			return errBroker
		}
		return nil
	}
	for want := 1; want <= 2; want++ {
		_, err := repo.Drain(context.Background(), 10, 2, fail)
		var sf *outbox.SendFailure
		if !errors.As(err, &sf) || sf.Attempts != want {
			t.Fatalf("pass %d: %v", want, err)
		}
	}
	var sent []string
	n, err := repo.Drain(context.Background(), 10, 2, collect(&sent))
	if err != nil || n != 1 || sent[0] != "next" {
		t.Fatalf("with the cap reached: %d %v %v", n, sent, err)
	}
	if attempts, lastErr, published := pgtest.OutboxRowState(t, pool, stuck); published || attempts != 2 || lastErr == "" {
		t.Fatalf("the stuck row stays for inspection: %d %q %v", attempts, lastErr, published)
	}
	// Re-armed (attempts = 0) after the cause is fixed, it is claimed again.
	if _, err := pool.Exec(context.Background(), `UPDATE outbox_events SET attempts = 0 WHERE id = $1`, stuck); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.Drain(context.Background(), 10, 2, collect(&sent)); err != nil || n != 1 || sent[1] != "stuck" {
		t.Fatalf("re-armed: %d %v %v", n, sent, err)
	}
}

func TestOverlappingRelaysNeverClaimTheSameRow(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	for i := range 20 {
		pgtest.InsertOutboxRow(t, pool, "com.example.T", string(rune('a'+i)))
	}
	var (
		mu   sync.Mutex
		seen = map[string]int{}
		wg   sync.WaitGroup
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := repo.Drain(context.Background(), 3, 0, func(_ context.Context, m outbox.Message) error {
					mu.Lock()
					seen[string(m.Key)]++
					mu.Unlock()
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("published %d distinct rows, want 20", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("row %s was sent %d times", k, n)
		}
	}
}

func TestInsertStoresTheEncodedMessageAndItsHeaders(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	in := outbox.Message{
		EventID: "00000000-0000-4000-8000-000000000001", Topic: "t.events", EventType: "com.example.thing.Happened", Subject: "s-1",
		Key: []byte("s-1"), DataSchema: "urn:example:Happened:v1", Value: []byte(`{"a":1}`),
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	}
	if err := repo.Insert(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(context.Background()); err != nil {
		t.Fatalf("an empty insert is a no-op: %v", err)
	}
	var got outbox.Message
	if _, err := repo.Drain(context.Background(), 10, 0, func(_ context.Context, m outbox.Message) error { got = m; return nil }); err != nil {
		t.Fatal(err)
	}
	if got.EventID != in.EventID || got.Topic != in.Topic || got.EventType != in.EventType || got.Subject != in.Subject || string(got.Key) != "s-1" ||
		got.DataSchema != in.DataSchema || string(got.Value) != `{"a":1}` || len(got.Headers) != 1 || got.Headers[0] != in.Headers[0] {
		t.Fatalf("round trip = %+v", got)
	}
	// The CloudEvents id is unique: a second row with it is refused.
	if err := repo.Insert(context.Background(), in); err == nil {
		t.Fatal("a duplicate event id must be refused")
	}
}

func TestRetentionPurgesOnlyRowsPastTheirWindow(t *testing.T) {
	pool := pgtest.NewPool(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO idempotency_keys (route, key, request_hash, created_at) VALUES ('r', 'old', 'h', now() - interval '25 hours'), ('r', 'new', 'h', now() - interval '23 hours')`,
		`INSERT INTO inventory_feeds (feed_id, node_id, full_update, as_of, line_count, state, applied_count, stale_count, zeroed_count, received_at, version)
		 VALUES ('f-old', 'n', false, now(), 0, 'FAILED', 0, 0, 0, now() - interval '31 days', 1), ('f-new', 'n', false, now(), 0, 'FAILED', 0, 0, 0, now() - interval '29 days', 1)`,
		`INSERT INTO processed_events (event_id, event_type, processed_at) VALUES ('e-old', 't', now() - interval '31 days'), ('e-new', 't', now() - interval '29 days')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Failure codes are required for FAILED rows by the domain, not by the table.
	got, err := postgres.NewRetention(pool).Purge(ctx)
	if err != nil || got != (postgres.Purged{IdempotencyKeys: 1, Feeds: 1, ProcessedEvents: 1}) {
		t.Fatalf("purged %+v, %v", got, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM idempotency_keys) + (SELECT count(*) FROM inventory_feeds) + (SELECT count(*) FROM processed_events)`).Scan(&left); err != nil || left != 3 {
		t.Fatalf("%d rows left, %v", left, err)
	}
	if again, err := postgres.NewRetention(pool).Purge(ctx); err != nil || again != (postgres.Purged{}) {
		t.Fatalf("a second purge: %+v %v", again, err)
	}
}
