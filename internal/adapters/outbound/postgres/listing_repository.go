package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// ListingRepo is the pgx-backed ports.ListingRepository.
type ListingRepo struct{ pool *pgxpool.Pool }

var _ ports.ListingRepository = (*ListingRepo)(nil)

// NewListingRepo constructs a ListingRepo over pool.
func NewListingRepo(pool *pgxpool.Pool) *ListingRepo { return &ListingRepo{pool: pool} }

const listingColumns = `listing_id, catalog_item_id, node_id, owner_ref, state, suppression_reason, available_qty, basis, as_of, feed_id, version`

func scanListing(row pgx.Row) (*listing.Listing, error) {
	var (
		s              listing.Snapshot
		state, basis   string
		reason, feedID *string
		asOf           *time.Time
	)
	if err := row.Scan(&s.ID, &s.ItemID, &s.NodeID, &s.OwnerRef, &state, &reason, &s.Qty, &basis, &asOf, &feedID, &s.Version); err != nil {
		return nil, notFound(err)
	}
	s.State, s.Basis = listing.State(state), listing.Basis(basis)
	if reason != nil {
		s.Reason = listing.Reason(*reason)
	}
	if asOf != nil {
		s.AsOf = *asOf
	}
	if feedID != nil {
		s.FeedID = *feedID
	}
	return listing.Restore(s)
}

func (r *ListingRepo) many(ctx context.Context, sql string, args ...any) ([]*listing.Listing, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query listings: %w", err)
	}
	defer rows.Close()
	out := []*listing.Listing{}
	for rows.Next() {
		l, err := scanListing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get implements ports.ListingRepository.
func (r *ListingRepo) Get(ctx context.Context, id listing.ID) (*listing.Listing, error) {
	return scanListing(queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+listingColumns+` FROM listings WHERE listing_id = $1`, string(id)))
}

// FindByItemAndNode implements ports.ListingRepository.
func (r *ListingRepo) FindByItemAndNode(ctx context.Context, item listing.ItemID, node listing.NodeID) (*listing.Listing, error) {
	return r.FindByItemNodeOwner(ctx, item, node, "")
}

// FindByItemNodeOwner implements ports.ListingRepository.
func (r *ListingRepo) FindByItemNodeOwner(ctx context.Context, item listing.ItemID, node listing.NodeID, owner listing.OwnerRef) (*listing.Listing, error) {
	return scanListing(queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+listingColumns+` FROM listings WHERE catalog_item_id = $1 AND node_id = $2 AND owner_ref = $3`,
		string(item), string(node), string(owner)))
}

// GetMany implements ports.ListingRepository.
func (r *ListingRepo) GetMany(ctx context.Context, ids []listing.ID) ([]*listing.Listing, error) {
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = string(id)
	}
	return r.many(ctx, `SELECT `+listingColumns+` FROM listings WHERE listing_id = ANY($1) ORDER BY listing_id`, keys)
}

// ListByNode implements ports.ListingRepository.
func (r *ListingRepo) ListByNode(ctx context.Context, node listing.NodeID) ([]*listing.Listing, error) {
	return r.many(ctx, `SELECT `+listingColumns+` FROM listings WHERE node_id = $1 ORDER BY listing_id`, string(node))
}

// ListByItem implements ports.ListingRepository.
func (r *ListingRepo) ListByItem(ctx context.Context, item listing.ItemID) ([]*listing.Listing, error) {
	return r.many(ctx, `SELECT `+listingColumns+` FROM listings WHERE catalog_item_id = $1 ORDER BY listing_id`, string(item))
}

// List implements ports.ListingRepository. Empty filter fields do not filter.
func (r *ListingRepo) List(ctx context.Context, f repository.ListingFilter, page repository.Page) ([]*listing.Listing, error) {
	return r.many(ctx, `
		SELECT `+listingColumns+` FROM listings
		WHERE listing_id > $1 AND ($2 = '' OR node_id = $2) AND ($3 = '' OR catalog_item_id = $3)
		ORDER BY listing_id LIMIT $4`,
		page.After, string(f.NodeID), string(f.ItemID), page.Limit)
}

const (
	insertListing = `
		INSERT INTO listings (` + listingColumns + `) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	updateListing = `
		UPDATE listings SET state = $2, suppression_reason = $3, available_qty = $4, basis = $5, as_of = $6, feed_id = $7, version = $8
		WHERE listing_id = $1 AND version = $9`
)

// args returns the statement and arguments that persist l.
func listingStatement(l *listing.Listing) (string, []any) {
	s := l.Snapshot()
	var reason, feedID *string
	if s.Reason != "" {
		v := string(s.Reason)
		reason = &v
	}
	var asOf *time.Time
	if !s.AsOf.IsZero() {
		asOf = &s.AsOf
		feedID = &s.FeedID
	}
	if s.Version == 1 {
		return insertListing, []any{string(s.ID), string(s.ItemID), string(s.NodeID), string(s.OwnerRef), string(s.State), reason, s.Qty, string(s.Basis), asOf, feedID, s.Version}
	}
	return updateListing, []any{string(s.ID), string(s.State), reason, s.Qty, string(s.Basis), asOf, feedID, s.Version, s.Version - 1}
}

// duplicateListing returns listing.ErrDuplicateListing when err is the unique
// violation of (catalog_item_id, node_id), else nil.
func duplicateListing(err error) error {
	if code, constraint := constraintOf(err); code == uniqueViolation && constraint == "listings_item_node_owner_key" {
		return listing.ErrDuplicateListing
	}
	return nil
}

// settle turns the outcome of one listing statement into the port contract.
func settleListing(tag pgconn.CommandTag, err error, version int64) error {
	if dup := duplicateListing(err); dup != nil {
		return dup
	}
	return saveResult(tag, err, version, "listings_pkey")
}

// Save implements ports.ListingRepository.
func (r *ListingRepo) Save(ctx context.Context, l *listing.Listing) error {
	sql, args := listingStatement(l)
	tag, err := queryFor(ctx, r.pool).Exec(ctx, sql, args...)
	return settleListing(tag, err, l.Version())
}

// SaveMany implements ports.ListingRepository: one batch in one transaction, so
// a 10000-listing feed is a single round trip and either all rows change or none.
func (r *ListingRepo) SaveMany(ctx context.Context, ls []*listing.Listing) error {
	if len(ls) == 0 {
		return nil
	}
	return inTx(ctx, r.pool, func(q querier) error {
		batch := &pgx.Batch{}
		for _, l := range ls {
			sql, args := listingStatement(l)
			batch.Queue(sql, args...)
		}
		results := q.SendBatch(ctx, batch)
		defer func() { _ = results.Close() }()
		for _, l := range ls {
			tag, err := results.Exec()
			if err := settleListing(tag, err, l.Version()); err != nil {
				return err
			}
		}
		return nil
	})
}
