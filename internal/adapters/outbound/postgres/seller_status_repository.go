package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
)

// SellerStatusRepo is the pgx-backed ports.SellerStatusRepository (ADR 0013).
type SellerStatusRepo struct{ pool *pgxpool.Pool }

var _ ports.SellerStatusRepository = (*SellerStatusRepo)(nil)

// NewSellerStatusRepo constructs a SellerStatusRepo over pool.
func NewSellerStatusRepo(pool *pgxpool.Pool) *SellerStatusRepo { return &SellerStatusRepo{pool: pool} }

// Get implements ports.SellerStatusRepository.
func (r *SellerStatusRepo) Get(ctx context.Context, id sellerstatus.ID) (*sellerstatus.View, error) {
	var (
		s      sellerstatus.Snapshot
		status string
	)
	err := queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT seller_id, status, as_of, version FROM seller_status_view WHERE seller_id = $1`, string(id)).
		Scan(&s.ID, &status, &s.AsOf, &s.Version)
	if err != nil {
		return nil, notFound(err)
	}
	s.Status = sellerstatus.Status(status)
	return sellerstatus.Restore(s)
}

// Save implements ports.SellerStatusRepository.
func (r *SellerStatusRepo) Save(ctx context.Context, v *sellerstatus.View) error {
	s := v.Snapshot()
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		tag, err := q.Exec(ctx, `INSERT INTO seller_status_view (seller_id, status, as_of, version) VALUES ($1, $2, $3, $4)`,
			string(s.ID), string(s.Status), s.AsOf, s.Version)
		return saveResult(tag, err, s.Version, "seller_status_view_pkey")
	}
	tag, err := q.Exec(ctx, `UPDATE seller_status_view SET status = $2, as_of = $3, version = $4 WHERE seller_id = $1 AND version = $5`,
		string(s.ID), string(s.Status), s.AsOf, s.Version, s.Version-1)
	return saveResult(tag, err, s.Version, "seller_status_view_pkey")
}
