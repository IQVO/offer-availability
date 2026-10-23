package usecases

import (
	"context"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// The page size bounds of the list reads (ADR 0002, A16).
const (
	DefaultLimit = 100
	MaxLimit     = 500
)

// PageQuery is the keyset page of a list read. A nil Limit is DefaultLimit.
type PageQuery struct {
	Limit *int
	After string
}

// limit validates the page size.
func (p PageQuery) limit() (int, error) {
	if p.Limit == nil {
		return DefaultLimit, nil
	}
	if *p.Limit < 1 || *p.Limit > MaxLimit {
		return 0, shared.Invalid("limit must be between 1 and %d", MaxLimit)
	}
	return *p.Limit, nil
}

// page is the repository page that fetches one row more than asked for, to know
// whether a next page exists.
func (p PageQuery) page() (repository.Page, int, error) {
	limit, err := p.limit()
	if err != nil {
		return repository.Page{}, 0, err
	}
	return repository.Page{After: p.After, Limit: limit + 1}, limit, nil
}

// BrowseCatalog lists catalogue items (Open Host Service).
type BrowseCatalog struct{ items ports.CatalogRepository }

// NewBrowseCatalog builds the query.
func NewBrowseCatalog(items ports.CatalogRepository) *BrowseCatalog { return &BrowseCatalog{items} }

// Execute returns one page of items by id ascending; an invalid limit is
// shared.ErrValidation.
func (q *BrowseCatalog) Execute(ctx context.Context, p PageQuery) (views.CatalogPage, error) {
	page, limit, err := p.page()
	if err != nil {
		return views.CatalogPage{}, err
	}
	items, err := q.items.List(ctx, page)
	if err != nil {
		return views.CatalogPage{}, err
	}
	out := views.CatalogPage{Items: []views.CatalogItem{}}
	for i, it := range items {
		if i == limit {
			out.NextCursor = out.Items[limit-1].CatalogItemID
			break
		}
		out.Items = append(out.Items, views.CatalogItemFrom(it))
	}
	return out, nil
}

// GetCatalogItem reads one catalogue item (Open Host Service).
type GetCatalogItem struct{ items ports.CatalogRepository }

// NewGetCatalogItem builds the query.
func NewGetCatalogItem(items ports.CatalogRepository) *GetCatalogItem { return &GetCatalogItem{items} }

// Execute returns the item, or ports.ErrNotFound.
func (q *GetCatalogItem) Execute(ctx context.Context, id catalog.ID) (views.CatalogItem, error) {
	it, err := q.items.Get(ctx, id)
	if err != nil {
		return views.CatalogItem{}, err
	}
	return views.CatalogItemFrom(it), nil
}

// ListListingsQuery filters ListListings; an empty field does not filter and the
// set ones combine with AND.
type ListListingsQuery struct {
	NodeID        listing.NodeID
	CatalogItemID listing.ItemID
	Page          PageQuery
}

// ListListings lists listings by node and/or catalogue item.
type ListListings struct{ listings ports.ListingRepository }

// NewListListings builds the query.
func NewListListings(listings ports.ListingRepository) *ListListings { return &ListListings{listings} }

// Execute returns one page of the matching listings by id ascending.
func (q *ListListings) Execute(ctx context.Context, query ListListingsQuery) (views.ListingPage, error) {
	page, limit, err := query.Page.page()
	if err != nil {
		return views.ListingPage{}, err
	}
	rows, err := q.listings.List(ctx, repository.ListingFilter{NodeID: query.NodeID, ItemID: query.CatalogItemID}, page)
	if err != nil {
		return views.ListingPage{}, err
	}
	out := views.ListingPage{Listings: []views.Listing{}}
	for i, l := range rows {
		if i == limit {
			out.NextCursor = out.Listings[limit-1].ListingID
			break
		}
		out.Listings = append(out.Listings, views.ListingFrom(l))
	}
	return out, nil
}

// GetInventoryFeed reads one feed and its outcome.
type GetInventoryFeed struct{ feeds ports.FeedRepository }

// NewGetInventoryFeed builds the query.
func NewGetInventoryFeed(feeds ports.FeedRepository) *GetInventoryFeed {
	return &GetInventoryFeed{feeds}
}

// Execute returns the feed, or ports.ErrNotFound.
func (q *GetInventoryFeed) Execute(ctx context.Context, id feed.ID) (views.InventoryFeed, error) {
	f, err := q.feeds.Get(ctx, id)
	if err != nil {
		return views.InventoryFeed{}, err
	}
	return views.InventoryFeedFrom(f), nil
}
