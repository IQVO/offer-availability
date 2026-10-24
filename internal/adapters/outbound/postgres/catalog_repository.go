package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// CatalogRepo is the pgx-backed ports.CatalogRepository.
type CatalogRepo struct{ pool *pgxpool.Pool }

var _ ports.CatalogRepository = (*CatalogRepo)(nil)

// NewCatalogRepo constructs a CatalogRepo over pool.
func NewCatalogRepo(pool *pgxpool.Pool) *CatalogRepo { return &CatalogRepo{pool: pool} }

const catalogColumns = `catalog_item_id, title, length_mm, width_mm, height_mm, weight_g, hazmat, fragile, state, version, gtin`

const packColumns = `catalog_item_id, level, gtin, child_gtin, units_per_pack, length_mm, width_mm, height_mm, gross_weight_g, active, declared_at, retired_at`

func scanCatalogSnapshot(row pgx.Row) (catalog.Snapshot, error) {
	var s catalog.Snapshot
	var state string
	var gtin *string
	if err := row.Scan(&s.ID, &s.Title, &s.LengthMm, &s.WidthMm, &s.HeightMm, &s.WeightG, &s.Hazmat, &s.Fragile, &state, &s.Version, &gtin); err != nil {
		return s, notFound(err)
	}
	s.State = catalog.State(state)
	if gtin != nil {
		g, err := gs1.ParseGTIN(*gtin)
		if err != nil {
			return s, fmt.Errorf("catalogue item %q: stored gtin: %w", s.ID, err)
		}
		s.GTIN = g
	}
	return s, nil
}

// packsOf loads the pack levels of the items in declaration order.
func packsOf(ctx context.Context, q querier, ids []string) (map[catalog.ID][]catalog.PackLevelDef, error) {
	out := map[catalog.ID][]catalog.PackLevelDef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT `+packColumns+` FROM catalog_item_pack_levels WHERE catalog_item_id = ANY($1) ORDER BY catalog_item_id, seq`, ids)
	if err != nil {
		return nil, fmt.Errorf("query pack levels: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		id, def, err := scanPack(rows)
		if err != nil {
			return nil, err
		}
		out[id] = append(out[id], def)
	}
	return out, rows.Err()
}

func scanPack(row pgx.Row) (catalog.ID, catalog.PackLevelDef, error) {
	var (
		id, level, gtin string
		child           *string
		retired         *time.Time
		d               catalog.PackLevelDef
	)
	if err := row.Scan(&id, &level, &gtin, &child, &d.UnitsPerPack, &d.LengthMm, &d.WidthMm, &d.HeightMm, &d.GrossWeightG, &d.Active, &d.DeclaredAt, &retired); err != nil {
		return "", d, fmt.Errorf("scan pack level: %w", err)
	}
	var err error
	d.Level = catalog.PackLevel(level)
	if d.GTIN, err = gs1.ParseGTIN(gtin); err != nil {
		return "", d, fmt.Errorf("catalogue item %q: stored gtin: %w", id, err)
	}
	if child != nil {
		if d.ChildGTIN, err = gs1.ParseGTIN(*child); err != nil {
			return "", d, fmt.Errorf("catalogue item %q: stored child gtin: %w", id, err)
		}
	}
	d.DeclaredAt = d.DeclaredAt.UTC()
	if retired != nil {
		d.RetiredAt = retired.UTC()
	}
	return catalog.ID(id), d, nil
}

// Get implements ports.CatalogRepository.
func (r *CatalogRepo) Get(ctx context.Context, id catalog.ID) (*catalog.Item, error) {
	q := queryFor(ctx, r.pool)
	snap, err := scanCatalogSnapshot(q.QueryRow(ctx,
		`SELECT `+catalogColumns+` FROM catalog_items WHERE catalog_item_id = $1`, string(id)))
	if err != nil {
		return nil, err
	}
	packs, err := packsOf(ctx, q, []string{string(id)})
	if err != nil {
		return nil, err
	}
	return catalog.RestorePackaged(snap, packs[id])
}

// FindByGTIN implements ports.CatalogRepository.
func (r *CatalogRepo) FindByGTIN(ctx context.Context, gtin gs1.GTIN) (catalog.ID, error) {
	var id string
	err := queryFor(ctx, r.pool).QueryRow(ctx, `
		SELECT catalog_item_id FROM catalog_items WHERE gtin = $1
		UNION ALL
		SELECT catalog_item_id FROM catalog_item_pack_levels WHERE gtin = $1
		LIMIT 1`, gtin.String()).Scan(&id)
	if err != nil {
		return "", notFound(err)
	}
	return catalog.ID(id), nil
}

// List implements ports.CatalogRepository.
func (r *CatalogRepo) List(ctx context.Context, page repository.Page) ([]*catalog.Item, error) {
	q := queryFor(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT `+catalogColumns+` FROM catalog_items WHERE catalog_item_id > $1 ORDER BY catalog_item_id LIMIT $2`,
		page.After, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("list catalogue items: %w", err)
	}
	snaps := []catalog.Snapshot{}
	ids := []string{}
	for rows.Next() {
		s, err := scanCatalogSnapshot(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		snaps = append(snaps, s)
		ids = append(ids, string(s.ID))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	packs, err := packsOf(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*catalog.Item, 0, len(snaps))
	for _, s := range snaps {
		it, err := catalog.RestorePackaged(s, packs[s.ID])
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Save implements ports.CatalogRepository: INSERT for version 1, else an UPDATE
// guarded by the previous version; the pack levels are rewritten with the item
// (same transaction), and a GTIN another item holds is ErrGtinAlreadyAssigned.
func (r *CatalogRepo) Save(ctx context.Context, i *catalog.Item) error {
	return inTx(ctx, r.pool, func(q querier) error { return saveItem(ctx, q, i) })
}

func saveItem(ctx context.Context, q querier, i *catalog.Item) error {
	s := i.Snapshot()
	if s.Version == 1 {
		var gtin *string
		if !s.GTIN.IsZero() {
			v := s.GTIN.String()
			gtin = &v
		}
		tag, err := q.Exec(ctx, `
			INSERT INTO catalog_items (`+catalogColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			string(s.ID), s.Title, s.LengthMm, s.WidthMm, s.HeightMm, s.WeightG, s.Hazmat, s.Fragile, string(s.State), s.Version, gtin)
		return gtinConflict(saveResult(tag, err, s.Version, "catalog_items_pkey"), err)
	}
	tag, err := q.Exec(ctx, `
		UPDATE catalog_items SET state = $2, version = $3 WHERE catalog_item_id = $1 AND version = $4`,
		string(s.ID), string(s.State), s.Version, s.Version-1)
	if err := saveResult(tag, err, s.Version, "catalog_items_pkey"); err != nil {
		return err
	}
	return savePacks(ctx, q, s.ID, i.PackLevels())
}

// savePacks rewrites the pack levels of one item.
func savePacks(ctx context.Context, q querier, id catalog.ID, defs []catalog.PackLevelDef) error {
	if _, err := q.Exec(ctx, `DELETE FROM catalog_item_pack_levels WHERE catalog_item_id = $1`, string(id)); err != nil {
		return fmt.Errorf("clear pack levels: %w", err)
	}
	for seq, d := range defs {
		var child *string
		if !d.ChildGTIN.IsZero() {
			v := d.ChildGTIN.String()
			child = &v
		}
		var retired *time.Time
		if !d.Active {
			t := d.RetiredAt
			retired = &t
		}
		_, err := q.Exec(ctx, `INSERT INTO catalog_item_pack_levels (seq, `+packColumns+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			seq, string(id), string(d.Level), d.GTIN.String(), child, d.UnitsPerPack, d.LengthMm, d.WidthMm, d.HeightMm,
			d.GrossWeightG, d.Active, d.DeclaredAt, retired)
		if err != nil {
			return gtinConflict(fmt.Errorf("insert pack level: %w", err), err)
		}
	}
	return nil
}

// gtinConflict turns the unique violation of a GTIN into
// catalog.ErrGtinAlreadyAssigned; any other result passes through.
func gtinConflict(result, cause error) error {
	if code, constraint := constraintOf(cause); code == uniqueViolation &&
		(constraint == "catalog_items_gtin_key" || constraint == "catalog_item_pack_levels_gtin_key") {
		return catalog.ErrGtinAlreadyAssigned
	}
	return result
}
