package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// The reasons a received line or a whole InboundShipmentReceived is
// acknowledged without effect (ADR 0014). They are the `reason` values of the
// availability.seller_events.ignored counter.
const (
	// SkipReceivedUnknown: the event carries no `received` list (published
	// before the producer added it), so what was received is UNKNOWN.
	SkipReceivedUnknown = "received_unknown"
	// SkipGTINUnresolved: a line without catalogItemId whose GTIN no catalogue
	// item holds (or that is not a well-formed GS1 GTIN).
	SkipGTINUnresolved = "gtin_unresolved"
	// SkipItemUnknown: a line whose catalogItemId the catalogue does not hold.
	SkipItemUnknown = "item_unknown"
)

// ReceivedLine is one entry of InboundShipmentReceived.received.
type ReceivedLine struct {
	// CatalogItemID is "" when the producer could not resolve the item.
	CatalogItemID catalog.ID
	// GTIN is the EACH GTIN when the producer resolved it, the carton GTIN
	// otherwise.
	GTIN      string
	QtyEaches int
}

// ReceivedStockEvent is one seller-inbound InboundShipmentReceived, decoded by
// the Kafka adapter.
type ReceivedStockEvent struct {
	// EventID is the CloudEvents id (the inbox key) and EventType the full type.
	EventID   string
	EventType string
	// ShipmentID is carried for the logs only.
	ShipmentID string
	SellerID   listing.OwnerRef
	NodeID     listing.NodeID
	// ReceivedPresent is false when the payload has no `received` (an event
	// published before seller-inbound's ADR 0014): UNKNOWN, nothing is created.
	ReceivedPresent bool
	Received        []ReceivedLine
}

// SkippedLine is a received line that created nothing, with its reason
// (SkipGTINUnresolved or SkipItemUnknown).
type SkippedLine struct {
	CatalogItemID catalog.ID
	GTIN          string
	Reason        string
}

// ReceivedStockResult says what one event did.
type ReceivedStockResult struct {
	// Duplicate: the CloudEvents id was already in the inbox; nothing was done.
	Duplicate bool
	// Unknown: the event carries no `received`; only the inbox row was written.
	Unknown bool
	// Created are the new listings, in line order.
	Created []listing.ID
	// Existing counts the resolved items whose listing already existed.
	Existing int
	// Skipped are the lines that resolved to no catalogue item, in line order.
	Skipped []SkippedLine
}

// ApplyReceivedStock creates the owner listings of a received inbound shipment
// (ADR 0013 rule 8, built by ADR 0014).
type ApplyReceivedStock struct{ writer }

// NewApplyReceivedStock builds the use case.
func NewApplyReceivedStock(d Deps) *ApplyReceivedStock { return &ApplyReceivedStock{writer{d}} }

// Execute handles one event in ONE unit of work, inbox row included:
//
//   - a CloudEvents id already in the inbox is skipped (Duplicate);
//   - an event without `received` creates nothing (Unknown);
//   - each line with qtyEaches > 0 resolves its item from catalogItemId, or
//     else from the GTIN through the catalogue's GTIN/packaging index (an item
//     GTIN, any pack level GTIN, retired levels included); a line that resolves
//     to nothing is Skipped, never an error;
//   - each resolved item without a listing of (item, nodeId, sellerId) gets one,
//     built from the same causes as CreateListing: ListingCreated (availability
//     0, basis UNKNOWN) and, when the node is not sellable, the item is
//     discontinued or the owner is SUSPENDED, ListingSuppressed. An existing
//     listing is not touched.
//
// The skips are counted on Deps.ReceivedMetrics after the commit. A malformed
// event (no id, type, sellerId or nodeId) is shared.ErrValidation; the adapter
// dead-letters it.
func (uc *ApplyReceivedStock) Execute(ctx context.Context, ev ReceivedStockEvent) (ReceivedStockResult, error) {
	if err := ev.validate(); err != nil {
		return ReceivedStockResult{}, err
	}
	res, err := transact(ctx, uc.UoW, func(ctx context.Context) (ReceivedStockResult, error) {
		return uc.handle(ctx, ev)
	})
	if err != nil {
		return ReceivedStockResult{}, err
	}
	uc.record(ctx, ev.EventType, res)
	return res, nil
}

func (ev ReceivedStockEvent) validate() error {
	if ev.EventID == "" || ev.EventType == "" {
		return shared.Invalid("a received-stock event needs its CloudEvents id and type")
	}
	if ev.SellerID == "" || !ev.SellerID.Valid() || !ev.NodeID.Valid() {
		return shared.Invalid("sellerId %q and nodeId %q must be valid ids", ev.SellerID, ev.NodeID)
	}
	return nil
}

func (uc *ApplyReceivedStock) handle(ctx context.Context, ev ReceivedStockEvent) (ReceivedStockResult, error) {
	first, err := uc.Inbox.MarkProcessed(ctx, ev.EventID, ev.EventType, uc.now())
	if err != nil {
		return ReceivedStockResult{}, err
	}
	if !first {
		return ReceivedStockResult{Duplicate: true}, nil
	}
	if !ev.ReceivedPresent {
		return ReceivedStockResult{Unknown: true}, nil
	}
	r, err := uc.receipt(ctx, ev)
	if err != nil {
		return ReceivedStockResult{}, err
	}
	for _, line := range ev.Received {
		if line.QtyEaches <= 0 {
			continue
		}
		if err := r.apply(ctx, line); err != nil {
			return ReceivedStockResult{}, err
		}
	}
	if err := uc.publish(ctx, r.events); err != nil {
		return ReceivedStockResult{}, err
	}
	return r.res, nil
}

// receipt is the state of one event's lines: the node and owner causes are
// read once, the items already handled are remembered.
type receipt struct {
	uc        *ApplyReceivedStock
	node      listing.NodeID
	owner     listing.OwnerRef
	active    bool
	suspended bool
	seen      map[catalog.ID]bool
	events    []shared.Event
	res       ReceivedStockResult
}

func (uc *ApplyReceivedStock) receipt(ctx context.Context, ev ReceivedStockEvent) (*receipt, error) {
	active, err := uc.nodeActive(ctx, ev.NodeID)
	if err != nil {
		return nil, err
	}
	suspended, err := uc.ownerSuspended(ctx, ev.SellerID)
	if err != nil {
		return nil, err
	}
	return &receipt{uc: uc, node: ev.NodeID, owner: ev.SellerID, active: active, suspended: suspended, seen: map[catalog.ID]bool{}}, nil
}

// apply resolves one line and creates its listing when none exists.
func (r *receipt) apply(ctx context.Context, line ReceivedLine) error {
	item, reason, err := r.uc.resolve(ctx, line)
	if err != nil {
		return err
	}
	if item == nil {
		r.res.Skipped = append(r.res.Skipped, SkippedLine{CatalogItemID: line.CatalogItemID, GTIN: line.GTIN, Reason: reason})
		return nil
	}
	if r.seen[item.ID()] {
		return nil
	}
	r.seen[item.ID()] = true
	itemID := listing.ItemID(item.ID())
	_, err = r.uc.Listings.FindByItemNodeOwner(ctx, itemID, r.node, r.owner)
	exists, err := found(err)
	if err != nil {
		return err
	}
	if exists {
		r.res.Existing++
		return nil
	}
	l, events, err := listing.CreateOwned(r.uc.IDs.NewListingID(), itemID, r.node, r.owner,
		listing.Sellability{NodeActive: r.active, ItemDiscontinued: item.Discontinued(), OwnerSuspended: r.suspended}, r.uc.now())
	if err != nil {
		return err
	}
	if err := r.uc.Listings.Save(ctx, l); err != nil {
		if errors.Is(err, listing.ErrDuplicateListing) {
			// Created by POST /listings meanwhile: run the attempt again, where
			// that listing is the no-op case.
			return fmt.Errorf("%w: %w", ports.ErrConcurrentUpdate, err)
		}
		return err
	}
	r.res.Created = append(r.res.Created, l.ID())
	r.events = append(r.events, events...)
	return nil
}

// resolve finds the catalogue item of a line: the line's catalogItemId, or
// else the holder of its GTIN. A nil item comes with the skip reason.
func (uc *ApplyReceivedStock) resolve(ctx context.Context, line ReceivedLine) (*catalog.Item, string, error) {
	id, reason := line.CatalogItemID, SkipItemUnknown
	if id == "" {
		reason = SkipGTINUnresolved
		g, err := gs1.ParseGTIN(line.GTIN)
		if err != nil {
			return nil, reason, nil
		}
		id, err = uc.Catalog.FindByGTIN(ctx, g)
		hit, err := found(err)
		if err != nil || !hit {
			return nil, reason, err
		}
	}
	if !id.Valid() {
		return nil, reason, nil
	}
	item, err := uc.Catalog.Get(ctx, id)
	hit, err := found(err)
	if err != nil || !hit {
		return nil, reason, err
	}
	return item, "", nil
}

// record counts the skips of a committed event.
func (uc *ApplyReceivedStock) record(ctx context.Context, eventType string, res ReceivedStockResult) {
	m := uc.receivedMetrics()
	if res.Unknown {
		m.ReceivedSkipped(ctx, eventType, SkipReceivedUnknown)
	}
	for _, s := range res.Skipped {
		m.ReceivedSkipped(ctx, eventType, s.Reason)
	}
}
