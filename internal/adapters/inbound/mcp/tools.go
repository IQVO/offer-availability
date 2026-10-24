package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the SAME read use cases the REST adapter uses (GET /catalog,
// GET /catalog/{catalogItemId}, GET /listings, GET /inventory-feeds/{feedId});
// the adapter never constructs an outbound adapter itself, and it holds no
// write use case at all (docs/adr/0006). POST /catalog-items,
// POST /catalog-items/{id}:discontinue, POST /listings and
// POST /inventory-feeds stay REST-only.
type Deps struct {
	BrowseCatalog    *usecases.BrowseCatalog
	GetCatalogItem   *usecases.GetCatalogItem
	ListListings     *usecases.ListListings
	GetInventoryFeed *usecases.GetInventoryFeed
}

// --- inputs -------------------------------------------------------------------

type catalogItemIDInput struct {
	CatalogItemID string `json:"catalogItemId" jsonschema:"the catalogue item to read, e.g. item-1"`
}

type browseCatalogInput struct {
	Limit int    `json:"limit,omitempty" jsonschema:"page size, 1..500; optional, omitted or 0 means the default page size (100)"`
	After string `json:"after,omitempty" jsonschema:"the previous page's nextCursor; optional, omitted means the first page"`
}

type listListingsInput struct {
	NodeID        string `json:"nodeId,omitempty" jsonschema:"keep only the listings of this node; optional, omitted means every node"`
	CatalogItemID string `json:"catalogItemId,omitempty" jsonschema:"keep only the listings of this catalogue item; optional, omitted means every item"`
	Limit         int    `json:"limit,omitempty" jsonschema:"page size, 1..500; optional, omitted or 0 means the default page size (100)"`
	After         string `json:"after,omitempty" jsonschema:"the previous page's nextCursor; optional, omitted means the first page"`
}

type feedIDInput struct {
	FeedID string `json:"feedId" jsonschema:"the inventory feed to read, e.g. feed-7"`
}

// --- outputs (the REST bodies, camelCase like the REST contract) --------------

type catalogItemView struct {
	CatalogItemID string `json:"catalogItemId"`
	Title         string `json:"title"`
	LengthMm      int    `json:"lengthMm"`
	WidthMm       int    `json:"widthMm"`
	HeightMm      int    `json:"heightMm"`
	WeightG       int    `json:"weightG"`
	Hazmat        bool   `json:"hazmat"`
	Fragile       bool   `json:"fragile"`
	State         string `json:"state"`
}

type catalogPageView struct {
	Items      []catalogItemView `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type availabilityView struct {
	AvailableQty int    `json:"availableQty"`
	Basis        string `json:"basis"`
	AsOf         string `json:"asOf,omitempty"`
	FeedID       string `json:"feedId,omitempty"`
}

type listingView struct {
	ListingID         string           `json:"listingId"`
	CatalogItemID     string           `json:"catalogItemId"`
	NodeID            string           `json:"nodeId"`
	State             string           `json:"state"`
	SuppressionReason string           `json:"suppressionReason,omitempty"`
	Availability      availabilityView `json:"availability"`
}

type listingPageView struct {
	Listings   []listingView `json:"listings"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

type inventoryFeedView struct {
	FeedID       string `json:"feedId"`
	NodeID       string `json:"nodeId"`
	FullUpdate   bool   `json:"fullUpdate"`
	AsOf         string `json:"asOf"`
	State        string `json:"state"`
	LineCount    int    `json:"lineCount"`
	AppliedCount int    `json:"appliedCount"`
	StaleCount   int    `json:"staleCount"`
	ZeroedCount  int    `json:"zeroedCount"`
	ReceivedAt   string `json:"receivedAt"`
	AppliedAt    string `json:"appliedAt,omitempty"`
	FailureCode  string `json:"failureCode,omitempty"`
}

// --- mapping ------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// limitPtr turns an optional limit into the use case's pointer form; 0 means
// the default page size.
func limitPtr(limit int) *int {
	if limit == 0 {
		return nil
	}
	return &limit
}

func toCatalogItem(item views.CatalogItem) catalogItemView {
	return catalogItemView{
		CatalogItemID: item.CatalogItemID, Title: item.Title, LengthMm: item.LengthMm, WidthMm: item.WidthMm,
		HeightMm: item.HeightMm, WeightG: item.WeightG, Hazmat: item.Hazmat, Fragile: item.Fragile, State: item.State,
	}
}

func toCatalogPage(page views.CatalogPage) catalogPageView {
	items := make([]catalogItemView, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, toCatalogItem(item))
	}
	return catalogPageView{Items: items, NextCursor: page.NextCursor}
}

func toAvailability(a views.Availability) availabilityView {
	return availabilityView{
		AvailableQty: a.AvailableQty, Basis: a.Basis, AsOf: instant(deref(a.AsOf)), FeedID: a.FeedID,
	}
}

func toListing(l views.Listing) listingView {
	return listingView{
		ListingID: l.ListingID, CatalogItemID: l.CatalogItemID, NodeID: l.NodeID, State: l.State,
		SuppressionReason: l.SuppressionReason, Availability: toAvailability(l.Availability),
	}
}

func toListingPage(page views.ListingPage) listingPageView {
	listings := make([]listingView, 0, len(page.Listings))
	for _, l := range page.Listings {
		listings = append(listings, toListing(l))
	}
	return listingPageView{Listings: listings, NextCursor: page.NextCursor}
}

func toInventoryFeed(f views.InventoryFeed) inventoryFeedView {
	return inventoryFeedView{
		FeedID: f.FeedID, NodeID: f.NodeID, FullUpdate: f.FullUpdate, AsOf: instant(f.AsOf), State: f.State,
		LineCount: f.LineCount, AppliedCount: f.AppliedCount, StaleCount: f.StaleCount, ZeroedCount: f.ZeroedCount,
		ReceivedAt: instant(f.ReceivedAt), AppliedAt: instant(deref(f.AppliedAt)), FailureCode: f.FailureCode,
	}
}

// deref returns *t, or the zero time when t is nil.
func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// --- handlers -----------------------------------------------------------------

func (d Deps) browseCatalog(ctx context.Context, in browseCatalogInput) (catalogPageView, error) {
	page, err := d.BrowseCatalog.Execute(ctx, usecases.PageQuery{Limit: limitPtr(in.Limit), After: in.After})
	if err != nil {
		return catalogPageView{}, mapError(err)
	}
	return toCatalogPage(page), nil
}

func (d Deps) getCatalogItem(ctx context.Context, in catalogItemIDInput) (catalogItemView, error) {
	item, err := d.GetCatalogItem.Execute(ctx, catalog.ID(in.CatalogItemID))
	if err != nil {
		return catalogItemView{}, mapError(err)
	}
	return toCatalogItem(item), nil
}

func (d Deps) listListings(ctx context.Context, in listListingsInput) (listingPageView, error) {
	page, err := d.ListListings.Execute(ctx, usecases.ListListingsQuery{
		NodeID:        listing.NodeID(in.NodeID),
		CatalogItemID: listing.ItemID(in.CatalogItemID),
		Page:          usecases.PageQuery{Limit: limitPtr(in.Limit), After: in.After},
	})
	if err != nil {
		return listingPageView{}, mapError(err)
	}
	return toListingPage(page), nil
}

func (d Deps) getInventoryFeed(ctx context.Context, in feedIDInput) (inventoryFeedView, error) {
	f, err := d.GetInventoryFeed.Execute(ctx, feed.ID(in.FeedID))
	if err != nil {
		return inventoryFeedView{}, mapError(err)
	}
	return toInventoryFeed(f), nil
}

// --- registry -----------------------------------------------------------------

// registerTools adds the four read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name (docs/adr/0006).
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "browse_catalog",
		Description: "Browse the catalogue of sellable items, one keyset page at a time (catalogItemId ascending): each " +
			"item's title, dimensions in mm (lengthMm, widthMm, heightMm), weightG, its hazmat and fragile flags and its " +
			"state (ACTIVE, or DISCONTINUED once it was discontinued). Pass the returned nextCursor as after to get the " +
			"next page; it is absent on the last page. Read-only; a bad limit fails with validation-failed.",
		Annotations: readOnly,
	}, d.browseCatalog)

	addTool(server, &mcp.Tool{
		Name: "get_catalog_item",
		Description: "Read one catalogue item by id: title, dimensions in mm (lengthMm, widthMm, heightMm), weightG, " +
			"the hazmat and fragile flags, and its state (ACTIVE, or DISCONTINUED). Read-only; an unknown id fails with " +
			"not-found.",
		Annotations: readOnly,
	}, d.getCatalogItem)

	addTool(server, &mcp.Tool{
		Name: "list_listings",
		Description: "List which nodes sell what, one keyset page at a time (listingId ascending), optionally narrowed " +
			"to one node (nodeId) and/or one catalogue item (catalogItemId). Each listing says whether it is LIVE or " +
			"SUPPRESSED (with the reason: NODE_NOT_SELLABLE or DISCONTINUED) and the availability a vendor's feed last " +
			"gave it (availableQty, the vendor's basis, asOf and the feedId). Pass the returned nextCursor as after to " +
			"get the next page. Read-only; a bad limit fails with validation-failed.",
		Annotations: readOnly,
	}, d.listListings)

	addTool(server, &mcp.Tool{
		Name: "get_inventory_feed",
		Description: "Read one vendor inventory feed and its outcome by id: the node it was sent for (nodeId), whether " +
			"it was a fullUpdate, its asOf, and its state APPLIED, SUPERSEDED or FAILED with the counters (lineCount, " +
			"appliedCount, staleCount, zeroedCount), appliedAt, and the failureCode (listing-not-on-node or " +
			"negative-availability) when it FAILED. Read-only; an unknown id fails with not-found.",
		Annotations: readOnly,
	}, d.getInventoryFeed)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
