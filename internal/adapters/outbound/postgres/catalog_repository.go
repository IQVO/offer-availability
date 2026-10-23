package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
)

// CatalogRepo is the pgx-backed ports.CatalogRepository.
type CatalogRepo struct{ pool *pgxpool.Pool }

var _ ports.CatalogRepository = (*CatalogRepo)(nil)

// NewCatalogRepo constructs a CatalogRepo over pool.
func NewCatalogRepo(pool *pgxpool.Pool) *CatalogRepo { return &CatalogRepo{pool: pool} }

const catalogColumns = `catalog_item_id, title, length_mm, width_mm, height_mm, weight_g, hazmat, fragile, state, version`

func scanCatalog(row pgx.Row) (*catalog.Item, error) {
	var s catalog.Snapshot
	var state string
	if err := row.Scan(&s.ID, &s.Title, &s.LengthMm, &s.WidthMm, &s.HeightMm, &s.WeightG, &s.Hazmat, &s.Fragile, &state, &s.Version); err != nil {
		return nil, notFound(err)
	}
	s.State = catalog.State(state)
	return catalog.Restore(s)
}

// Get implements ports.CatalogRepository.
func (r *CatalogRepo) Get(ctx context.Context, id catalog.ID) (*catalog.Item, error) {
	return scanCatalog(queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+catalogColumns+` FROM catalog_items WHERE catalog_item_id = $1`, string(id)))
}

// List implements ports.CatalogRepository.
func (r *CatalogRepo) List(ctx context.Context, page repository.Page) ([]*catalog.Item, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx,
		`SELECT `+catalogColumns+` FROM catalog_items WHERE catalog_item_id > $1 ORDER BY catalog_item_id LIMIT $2`,
		page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list catalogue items: %w", err)
	}
	defer rows.Close()
	out := []*catalog.Item{}
	for rows.Next() {
		it, err := scanCatalog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Save implements ports.CatalogRepository: INSERT for version 1, else an UPDATE
// guarded by the previous version.
func (r *CatalogRepo) Save(ctx context.Context, i *catalog.Item) error {
	s := i.Snapshot()
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		tag, err := q.Exec(ctx, `
			INSERT INTO catalog_items (`+catalogColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			string(s.ID), s.Title, s.LengthMm, s.WidthMm, s.HeightMm, s.WeightG, s.Hazmat, s.Fragile, string(s.State), s.Version)
		return saveResult(tag, err, s.Version, "catalog_items_pkey")
	}
	tag, err := q.Exec(ctx, `
		UPDATE catalog_items SET state = $2, version = $3 WHERE catalog_item_id = $1 AND version = $4`,
		string(s.ID), string(s.State), s.Version, s.Version-1)
	return saveResult(tag, err, s.Version, "catalog_items_pkey")
}
