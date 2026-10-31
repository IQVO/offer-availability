// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary is the
// two sentinels of errors.go and the filters of internal/application/repository.
//
// Optimistic concurrency: every Save expects the stored version to be
// aggregate.Version()-1 (ADR 0002, A15); a create (Version() 1) expects that
// the id is not stored. Any mismatch, including an update of a row that no
// longer exists, is ErrConcurrentUpdate. Inside a UnitOfWork every repository
// joins the transaction carried by the ctx the unit handed to its function.
package ports

import (
	"context"
	"time"

	"github.com/claudioed/offer-availability/internal/application/outbox"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// CatalogRepository persists CatalogItem aggregates.
type CatalogRepository interface {
	// Get returns the item, or ErrNotFound.
	Get(ctx context.Context, id catalog.ID) (*catalog.Item, error)
	// List returns the items of the page ordered by id ascending; no match is
	// an empty list, never an error.
	List(ctx context.Context, page repository.Page) ([]*catalog.Item, error)
	// FindByGTIN returns the id of the item whose own GTIN or pack level GTIN
	// is gtin, or ErrNotFound. It backs the GTIN uniqueness pre-check.
	FindByGTIN(ctx context.Context, gtin gs1.GTIN) (catalog.ID, error)
	// Save persists the item and its pack levels under the concurrency rule of
	// the package comment. A GTIN another item holds is
	// catalog.ErrGtinAlreadyAssigned (unique index).
	Save(ctx context.Context, i *catalog.Item) error
}

// ListingRepository persists Listing aggregates.
type ListingRepository interface {
	// Get returns the listing, or ErrNotFound.
	Get(ctx context.Context, id listing.ID) (*listing.Listing, error)
	// FindByItemAndNode returns the operator-owned listing of the pair, or
	// ErrNotFound.
	FindByItemAndNode(ctx context.Context, item listing.ItemID, node listing.NodeID) (*listing.Listing, error)
	// FindByItemNodeOwner returns the listing of the triple, or ErrNotFound; an
	// empty owner is the operator-owned listing, which is what
	// FindByItemAndNode answers.
	FindByItemNodeOwner(ctx context.Context, item listing.ItemID, node listing.NodeID, owner listing.OwnerRef) (*listing.Listing, error)
	// GetMany returns the stored listings among ids, ordered by id ascending;
	// an id that is not stored is simply absent (never an error).
	GetMany(ctx context.Context, ids []listing.ID) ([]*listing.Listing, error)
	// ListByNode returns every listing of the node ordered by id ascending.
	ListByNode(ctx context.Context, node listing.NodeID) ([]*listing.Listing, error)
	// ListByItem returns every listing of the item ordered by id ascending.
	ListByItem(ctx context.Context, item listing.ItemID) ([]*listing.Listing, error)
	// List returns the listings matching filter on the page, ordered by id
	// ascending; no match is an empty list, never an error.
	List(ctx context.Context, filter repository.ListingFilter, page repository.Page) ([]*listing.Listing, error)
	// Save persists the listing under the concurrency rule of the package
	// comment. A second listing for the same (item, node, owner) is
	// listing.ErrDuplicateListing (unique index).
	Save(ctx context.Context, l *listing.Listing) error
	// SaveMany persists the listings under the same rule, all or none.
	SaveMany(ctx context.Context, ls []*listing.Listing) error
}

// FeedRepository persists InventoryFeed aggregates.
type FeedRepository interface {
	// Get returns the feed, or ErrNotFound.
	Get(ctx context.Context, id feed.ID) (*feed.InventoryFeed, error)
	// Save persists the feed under the concurrency rule of the package comment.
	Save(ctx context.Context, f *feed.InventoryFeed) error
}

// NodeFeedMarks keeps, per node, the asOf of the last APPLIED feed: the
// high-water mark of the node-level staleness rule (ADR 0002, A11).
type NodeFeedMarks interface {
	// Lock serialises the feeds of one node until the unit of work ends and
	// returns the mark; the zero time means no feed was applied yet. It must be
	// called inside a UnitOfWork.
	Lock(ctx context.Context, node listing.NodeID) (time.Time, error)
	// Advance raises the mark to asOf when asOf is later; a lower or equal asOf
	// leaves it unchanged.
	Advance(ctx context.Context, node listing.NodeID, asOf time.Time) error
}

// NodeStatusRepository persists the NodeStatusView read model.
type NodeStatusRepository interface {
	// Get returns the view of the node, or ErrNotFound.
	Get(ctx context.Context, id nodestatus.ID) (*nodestatus.View, error)
	// Save persists the view under the concurrency rule of the package comment.
	Save(ctx context.Context, v *nodestatus.View) error
}

// ProcessedEvents is the consumer inbox: the CloudEvents ids already handled.
type ProcessedEvents interface {
	// MarkProcessed records the event id and reports whether it was new. A
	// repeated id returns false and no error. It must be called inside the same
	// UnitOfWork as the effects of the event.
	MarkProcessed(ctx context.Context, eventID, eventType string, at time.Time) (bool, error)
}

// EventOutbox is the write side of the transactional outbox. Called with the
// ctx handed to UnitOfWork.Do it joins the SAME transaction as the aggregate
// Save calls, so the aggregates and their raised events commit or roll back
// together. The adapter encodes each event into its CloudEvents envelope (the
// id is minted once, at append) and stores one row per event in order; a relay
// drains the rows to Kafka afterwards. Appending no events is a no-op.
type EventOutbox interface {
	Append(ctx context.Context, events ...shared.Event) error
}

// EventEncoder turns domain events into already-encoded outbox messages: the
// CloudEvents envelope, the topic, the Kafka key and headers. The CloudEvents id
// is minted here, once. An event it does not publish is an error.
type EventEncoder interface {
	Encode(events ...shared.Event) ([]outbox.Message, error)
}

// UnitOfWork runs fn as ONE atomic unit: every repository and outbox call fn
// makes with the ctx it is handed commits together when fn returns nil, or
// rolls back together when fn returns an error (or panics).
//
//   - fn MUST use the ctx passed to it for every call that should participate;
//     the unit travels in that ctx.
//   - Do is re-entrant: when ctx already carries an open unit, Do joins it and
//     the outermost Do owns commit and rollback.
//   - The error fn returns is returned from Do unchanged (errors.Is keeps
//     working); a commit failure is returned wrapped.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Clock is the service's notion of "now"; use cases stamp aggregates with it.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints the opaque ids of new aggregates (ADR 0002, A1). A feed has
// no minted id: the caller chooses it.
type IDGenerator interface {
	NewCatalogItemID() catalog.ID
	NewListingID() listing.ID
}

// FeedMetrics records the business outcome of every inventory feed attempt (the
// Tier 2 metric of the fleet metrics convention; ApplyInventoryFeed is the
// command this context exists for). The use case calls exactly one method per
// attempt:
//
//   - FeedApplied: the feed was applied and its listings changed;
//   - FeedSuperseded: a newer feed of the node had already been applied, so the
//     feed was stored as SUPERSEDED and changed nothing;
//   - FeedFailed: the feed was stored as FAILED because a line broke an
//     invariant; reason is the problem slug ("listing-not-on-node",
//     "negative-availability");
//   - FeedRejected: the request was refused and nothing was recorded; reason is
//     the problem slug ("validation-failed", "feed-already-applied",
//     "concurrent-update", "internal-error", ...).
//
// The implementation lives in internal/adapters/outbound/telemetry; a nil
// Deps.Metrics records nothing.
type FeedMetrics interface {
	FeedApplied(ctx context.Context)
	FeedSuperseded(ctx context.Context)
	FeedFailed(ctx context.Context, reason string)
	FeedRejected(ctx context.Context, reason string)
}
