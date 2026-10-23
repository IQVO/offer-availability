package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention windows (ADR 0002, A19). Idempotency keys live 24 hours (the model
// conventions); inventory feeds and processed (inbox) events 30 days.
const (
	IdempotencyKeyTTL = "24 hours"
	FeedRetention     = "30 days"
	InboxRetention    = "30 days"
)

// Retention deletes the rows past their retention window.
type Retention struct{ pool *pgxpool.Pool }

// NewRetention constructs a Retention over pool.
func NewRetention(pool *pgxpool.Pool) *Retention { return &Retention{pool: pool} }

// Purged counts the rows one Purge deleted.
type Purged struct {
	IdempotencyKeys int64
	Feeds           int64
	ProcessedEvents int64
}

// Purge deletes, in separate statements (none of them locks the rows a request
// is using: an expired row is never read again), every idempotency key older
// than IdempotencyKeyTTL, every inventory feed received before FeedRetention and
// every processed event older than InboxRetention. It stops at the first error.
func (r *Retention) Purge(ctx context.Context) (Purged, error) {
	var out Purged
	steps := []struct {
		what  string
		sql   string
		count *int64
	}{
		{"idempotency keys", `DELETE FROM idempotency_keys WHERE created_at < now() - interval '` + IdempotencyKeyTTL + `'`, &out.IdempotencyKeys},
		{"inventory feeds", `DELETE FROM inventory_feeds WHERE received_at < now() - interval '` + FeedRetention + `'`, &out.Feeds},
		{"processed events", `DELETE FROM processed_events WHERE processed_at < now() - interval '` + InboxRetention + `'`, &out.ProcessedEvents},
	}
	for _, s := range steps {
		tag, err := r.pool.Exec(ctx, s.sql)
		if err != nil {
			return out, fmt.Errorf("purge %s: %w", s.what, err)
		}
		*s.count = tag.RowsAffected()
	}
	return out, nil
}
