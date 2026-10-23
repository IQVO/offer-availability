package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// FeedRepo is the pgx-backed ports.FeedRepository.
type FeedRepo struct{ pool *pgxpool.Pool }

var _ ports.FeedRepository = (*FeedRepo)(nil)

// NewFeedRepo constructs a FeedRepo over pool.
func NewFeedRepo(pool *pgxpool.Pool) *FeedRepo { return &FeedRepo{pool: pool} }

const feedColumns = `feed_id, node_id, full_update, as_of, line_count, state, applied_count, stale_count, zeroed_count, failure_code, received_at, applied_at, version`

func scanFeed(row pgx.Row) (*feed.InventoryFeed, error) {
	var (
		s         feed.Snapshot
		state     string
		failure   *string
		appliedAt *time.Time
	)
	if err := row.Scan(&s.ID, &s.NodeID, &s.FullUpdate, &s.AsOf, &s.LineCount, &state, &s.AppliedCount, &s.StaleCount,
		&s.ZeroedCount, &failure, &s.ReceivedAt, &appliedAt, &s.Version); err != nil {
		return nil, notFound(err)
	}
	s.State = feed.State(state)
	if failure != nil {
		s.FailureCode = *failure
	}
	if appliedAt != nil {
		s.AppliedAt = *appliedAt
	}
	return feed.Restore(s)
}

// Get implements ports.FeedRepository.
func (r *FeedRepo) Get(ctx context.Context, id feed.ID) (*feed.InventoryFeed, error) {
	return scanFeed(queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+feedColumns+` FROM inventory_feeds WHERE feed_id = $1`, string(id)))
}

// Save implements ports.FeedRepository: INSERT for version 1, else an UPDATE
// guarded by the previous version. A resubmitted FAILED feed rewrites the whole
// row, so the UPDATE sets every column.
func (r *FeedRepo) Save(ctx context.Context, f *feed.InventoryFeed) error {
	s := f.Snapshot()
	var failure *string
	if s.FailureCode != "" {
		failure = &s.FailureCode
	}
	var appliedAt *time.Time
	if !s.AppliedAt.IsZero() {
		appliedAt = &s.AppliedAt
	}
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		tag, err := q.Exec(ctx, `INSERT INTO inventory_feeds (`+feedColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			string(s.ID), string(s.NodeID), s.FullUpdate, s.AsOf, s.LineCount, string(s.State), s.AppliedCount, s.StaleCount,
			s.ZeroedCount, failure, s.ReceivedAt, appliedAt, s.Version)
		return saveResult(tag, err, s.Version, "inventory_feeds_pkey")
	}
	tag, err := q.Exec(ctx, `
		UPDATE inventory_feeds SET full_update = $2, as_of = $3, line_count = $4, state = $5, applied_count = $6, stale_count = $7,
		       zeroed_count = $8, failure_code = $9, received_at = $10, applied_at = $11, version = $12
		WHERE feed_id = $1 AND version = $13`,
		string(s.ID), s.FullUpdate, s.AsOf, s.LineCount, string(s.State), s.AppliedCount, s.StaleCount,
		s.ZeroedCount, failure, s.ReceivedAt, appliedAt, s.Version, s.Version-1)
	return saveResult(tag, err, s.Version, "inventory_feeds_pkey")
}

// NodeFeedMarks is the pgx-backed ports.NodeFeedMarks.
type NodeFeedMarks struct{ pool *pgxpool.Pool }

var _ ports.NodeFeedMarks = (*NodeFeedMarks)(nil)

// NewNodeFeedMarks constructs NodeFeedMarks over pool.
func NewNodeFeedMarks(pool *pgxpool.Pool) *NodeFeedMarks { return &NodeFeedMarks{pool: pool} }

// Lock implements ports.NodeFeedMarks: it creates the node's row if needed and
// reads it FOR UPDATE, so a second feed of the same node waits for the first
// transaction to end.
func (m *NodeFeedMarks) Lock(ctx context.Context, node listing.NodeID) (time.Time, error) {
	q := queryFor(ctx, m.pool)
	if _, err := q.Exec(ctx, `INSERT INTO node_feed_marks (node_id) VALUES ($1) ON CONFLICT (node_id) DO NOTHING`, string(node)); err != nil {
		return time.Time{}, err
	}
	var mark *time.Time
	if err := q.QueryRow(ctx, `SELECT last_applied_as_of FROM node_feed_marks WHERE node_id = $1 FOR UPDATE`, string(node)).Scan(&mark); err != nil {
		return time.Time{}, err
	}
	if mark == nil {
		return time.Time{}, nil
	}
	return mark.UTC(), nil
}

// Advance implements ports.NodeFeedMarks (GREATEST ignores the NULL of a node
// without an applied feed).
func (m *NodeFeedMarks) Advance(ctx context.Context, node listing.NodeID, asOf time.Time) error {
	_, err := queryFor(ctx, m.pool).Exec(ctx, `
		INSERT INTO node_feed_marks (node_id, last_applied_as_of) VALUES ($1, $2)
		ON CONFLICT (node_id) DO UPDATE
		SET last_applied_as_of = GREATEST(node_feed_marks.last_applied_as_of, EXCLUDED.last_applied_as_of)`,
		string(node), asOf.UTC())
	return err
}
