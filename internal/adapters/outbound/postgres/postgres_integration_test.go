//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/outbox"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// stubEncoder is the smallest ports.EventEncoder: one outbox row per event, so
// the contract can count rows. The real encoder has its own golden tests.
type stubEncoder struct{}

var _ ports.EventEncoder = stubEncoder{}

func (stubEncoder) Encode(events ...shared.Event) ([]outbox.Message, error) {
	out := make([]outbox.Message, len(events))
	for i, ev := range events {
		out[i] = outbox.Message{
			EventID: fmt.Sprintf("%s-%d-%d", ev.SubjectID(), ev.AggregateVersion(), i), Topic: "test.integration",
			EventType: ev.EventName(), Subject: ev.SubjectID(), Key: []byte(ev.SubjectID()), DataSchema: "urn:test", Value: []byte(`{}`),
		}
	}
	return out, nil
}

// TestRepositoryContract runs the SAME repository contract the memory doubles
// pass, against real Postgres.
func TestRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Fixture {
		pool := pgtest.NewPool(t)
		return repocontract.Fixture{
			Catalog: postgres.NewCatalogRepo(pool), Listings: postgres.NewListingRepo(pool), Feeds: postgres.NewFeedRepo(pool),
			Marks: postgres.NewNodeFeedMarks(pool), Nodes: postgres.NewNodeStatusRepo(pool), Inbox: postgres.NewProcessedEventRepo(pool),
			Outbox: postgres.NewEventOutbox(postgres.NewOutboxRepo(pool), stubEncoder{}), UoW: postgres.NewUnitOfWork(pool),
			OutboxCount: func(ctx context.Context) (int, error) {
				var n int
				err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n)
				return n, err
			},
		}
	})
}

// TestMigrationsUpDownUp proves the embedded migrations apply, revert cleanly
// and apply again, and that a second Up is a no-op.
func TestMigrationsUpDownUp(t *testing.T) {
	dsn := pgtest.EmptyURL(t)
	for step, run := range []func(string) error{
		postgres.RunMigrations, postgres.RunMigrations, postgres.RevertMigrations, postgres.RevertMigrations, postgres.RunMigrations,
	} {
		if err := run(dsn); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	pool, err := postgres.NewPool(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tables int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name IN
		('catalog_items','listings','inventory_feeds','node_feed_marks','node_status_view','processed_events','outbox_events','idempotency_keys')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 8 {
		t.Fatalf("tables after up/down/up = %d, want 8", tables)
	}
}
