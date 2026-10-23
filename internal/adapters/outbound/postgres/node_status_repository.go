package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
)

// NodeStatusRepo is the pgx-backed ports.NodeStatusRepository.
type NodeStatusRepo struct{ pool *pgxpool.Pool }

var _ ports.NodeStatusRepository = (*NodeStatusRepo)(nil)

// NewNodeStatusRepo constructs a NodeStatusRepo over pool.
func NewNodeStatusRepo(pool *pgxpool.Pool) *NodeStatusRepo { return &NodeStatusRepo{pool: pool} }

// Get implements ports.NodeStatusRepository.
func (r *NodeStatusRepo) Get(ctx context.Context, id nodestatus.ID) (*nodestatus.View, error) {
	var (
		s      nodestatus.Snapshot
		status string
	)
	err := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT node_id, status, status_at, version FROM node_status_view WHERE node_id = $1`, string(id)).
		Scan(&s.ID, &status, &s.StatusAt, &s.Version)
	if err != nil {
		return nil, notFound(err)
	}
	s.Status = nodestatus.Status(status)
	return nodestatus.Restore(s)
}

// Save implements ports.NodeStatusRepository.
func (r *NodeStatusRepo) Save(ctx context.Context, v *nodestatus.View) error {
	s := v.Snapshot()
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		tag, err := q.Exec(ctx, `INSERT INTO node_status_view (node_id, status, status_at, version) VALUES ($1, $2, $3, $4)`,
			string(s.ID), string(s.Status), s.StatusAt, s.Version)
		return saveResult(tag, err, s.Version, "node_status_view_pkey")
	}
	tag, err := q.Exec(ctx, `UPDATE node_status_view SET status = $2, status_at = $3, version = $4 WHERE node_id = $1 AND version = $5`,
		string(s.ID), string(s.Status), s.StatusAt, s.Version, s.Version-1)
	return saveResult(tag, err, s.Version, "node_status_view_pkey")
}

// ProcessedEventRepo is the pgx-backed consumer inbox (ports.ProcessedEvents).
type ProcessedEventRepo struct{ pool *pgxpool.Pool }

var _ ports.ProcessedEvents = (*ProcessedEventRepo)(nil)

// NewProcessedEventRepo constructs a ProcessedEventRepo over pool.
func NewProcessedEventRepo(pool *pgxpool.Pool) *ProcessedEventRepo {
	return &ProcessedEventRepo{pool: pool}
}

// MarkProcessed implements ports.ProcessedEvents. Called inside a UnitOfWork the
// INSERT is part of the SAME transaction as the event's effects, so a rollback
// un-claims it and a redelivery is processed, not skipped. A concurrent claim of
// the same id blocks on the primary key until the other transaction ends.
func (r *ProcessedEventRepo) MarkProcessed(ctx context.Context, eventID, eventType string, at time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO processed_events (event_id, event_type, processed_at) VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, eventID, eventType, at.UTC())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
