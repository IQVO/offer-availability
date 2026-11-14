package usecases

import (
	"context"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// The reasons a return line, or a whole ReturnCompletion, is acknowledged
// without effect (ADR 0015). They are the `reason` values of the
// availability.customer_returns_events.ignored counter.
const (
	// SkipLinesAbsent: the event carries no `lines`; nothing was restocked.
	SkipLinesAbsent = "lines_absent"
	// SkipNothingRestockable: a line with qtyRestockable <= 0 (its unsellable
	// units never touch a listing).
	SkipNothingRestockable = "nothing_restockable"
	// SkipItemAbsent: a line without a usable catalogItemId.
	SkipItemAbsent = "item_absent"
	// SkipListingUnknown: a line whose listing of (catalogItemId, nodeId,
	// ownerRef) does not exist.
	SkipListingUnknown = "listing_unknown"
)

// ReturnLine is one entry of ReturnCompleted.lines.
type ReturnLine struct {
	CatalogItemID catalog.ID
	// OwnerRef is the seller whose stock was returned; "" is the
	// operator-owned listing.
	OwnerRef       listing.OwnerRef
	QtyRestockable int
	QtyUnsellable  int
}

// ReturnCompletionEvent is one customer-returns ReturnCompleted, decoded by
// the Kafka adapter.
type ReturnCompletionEvent struct {
	// EventID is the CloudEvents id (the inbox key) and EventType the full type.
	EventID   string
	EventType string
	// AuthorizationID is the return's aggregate id and the provenance stamp of
	// every restock it causes.
	AuthorizationID string
	NodeID          listing.NodeID
	// ReceivedAt is the restock instant.
	ReceivedAt time.Time
	// LinesPresent is false when the payload has no `lines`.
	LinesPresent bool
	Lines        []ReturnLine
}

// RestockedListing names one listing whose availability was raised.
type RestockedListing struct {
	ListingID     listing.ID
	CatalogItemID catalog.ID
	AddedQty      int
}

// SkippedReturnLine is a return line that restocked nothing, with its reason.
type SkippedReturnLine struct {
	CatalogItemID catalog.ID
	OwnerRef      listing.OwnerRef
	Reason        string
}

// ReturnCompletionResult says what one event did.
type ReturnCompletionResult struct {
	// Duplicate: the CloudEvents id was already in the inbox; nothing was done.
	Duplicate bool
	// Restocked are the listings whose availability was raised, in line order.
	Restocked []RestockedListing
	// Skipped are the lines that restocked nothing, in line order.
	Skipped []SkippedReturnLine
	// LinesAbsent: the event carried no `lines`; only the inbox row was written.
	LinesAbsent bool
}

// ApplyReturnCompletion restocks the owner listings of a closed return (ADR
// 0015), the twin of ApplyReceivedStock: the same one-transaction,
// inbox-idempotent shape, counting every line it cannot apply.
type ApplyReturnCompletion struct{ writer }

// NewApplyReturnCompletion builds the use case.
func NewApplyReturnCompletion(d Deps) *ApplyReturnCompletion {
	return &ApplyReturnCompletion{writer{d}}
}

// Execute handles one event in ONE unit of work, inbox row included:
//
//   - a CloudEvents id already in the inbox is skipped (Duplicate);
//   - an event without `lines` restocks nothing (LinesAbsent);
//   - each line with qtyRestockable > 0 and a usable catalogItemId raises the
//     availability of the listing of (catalogItemId, nodeId, ownerRef) by
//     qtyRestockable (Listing.Restock, one ListingAvailabilityChanged each);
//   - a line that cannot be applied is Skipped, never an error: nothing
//     restockable (the unsellable units never touch a listing), no usable
//     item, or no known listing.
//
// The skips are counted on Deps.ReturnMetrics after the commit. A malformed
// event (no id, type, authorizationId, nodeId or instant) is
// shared.ErrValidation; the adapter dead-letters it.
func (uc *ApplyReturnCompletion) Execute(ctx context.Context, ev ReturnCompletionEvent) (ReturnCompletionResult, error) {
	if err := ev.validate(); err != nil {
		return ReturnCompletionResult{}, err
	}
	res, err := transact(ctx, uc.UoW, func(ctx context.Context) (ReturnCompletionResult, error) {
		return uc.handle(ctx, ev)
	})
	if err != nil {
		return ReturnCompletionResult{}, err
	}
	uc.record(ctx, ev.EventType, res)
	return res, nil
}

func (ev ReturnCompletionEvent) validate() error {
	if ev.EventID == "" || ev.EventType == "" {
		return shared.Invalid("a return completion needs its CloudEvents id and type")
	}
	if !shared.ValidID(ev.AuthorizationID) {
		return shared.Invalid("authorizationId %q must be a valid id", ev.AuthorizationID)
	}
	if !ev.NodeID.Valid() {
		return shared.Invalid("nodeId %q must be a valid id", ev.NodeID)
	}
	if ev.ReceivedAt.IsZero() {
		return shared.Invalid("a return completion needs its instant")
	}
	return nil
}

func (uc *ApplyReturnCompletion) handle(ctx context.Context, ev ReturnCompletionEvent) (ReturnCompletionResult, error) {
	first, err := uc.Inbox.MarkProcessed(ctx, ev.EventID, ev.EventType, uc.now())
	if err != nil {
		return ReturnCompletionResult{}, err
	}
	if !first {
		return ReturnCompletionResult{Duplicate: true}, nil
	}
	if !ev.LinesPresent {
		return ReturnCompletionResult{LinesAbsent: true}, nil
	}
	res := ReturnCompletionResult{}
	var events []shared.Event
	for _, line := range ev.Lines {
		l, lineEvents, reason, err := uc.restock(ctx, ev, line)
		if err != nil {
			return ReturnCompletionResult{}, err
		}
		if l != nil {
			res.Restocked = append(res.Restocked, *l)
			events = append(events, lineEvents...)
			continue
		}
		res.Skipped = append(res.Skipped, SkippedReturnLine{CatalogItemID: line.CatalogItemID, OwnerRef: line.OwnerRef, Reason: reason})
	}
	if err := uc.publish(ctx, events); err != nil {
		return ReturnCompletionResult{}, err
	}
	return res, nil
}

// restock applies one line: the listing found is restocked and saved; a nil
// listing comes with the skip reason and no error.
func (uc *ApplyReturnCompletion) restock(ctx context.Context, ev ReturnCompletionEvent, line ReturnLine) (*RestockedListing, []shared.Event, string, error) {
	if line.QtyRestockable <= 0 {
		return nil, nil, SkipNothingRestockable, nil
	}
	if !line.CatalogItemID.Valid() {
		return nil, nil, SkipItemAbsent, nil
	}
	l, err := uc.Listings.FindByItemNodeOwner(ctx, listing.ItemID(line.CatalogItemID), ev.NodeID, line.OwnerRef)
	hit, err := found(err)
	if err != nil {
		return nil, nil, "", err
	}
	if !hit {
		return nil, nil, SkipListingUnknown, nil
	}
	raised, err := l.Restock(line.QtyRestockable, ev.ReceivedAt, ev.AuthorizationID, uc.now())
	if err != nil {
		return nil, nil, "", err
	}
	if err := uc.Listings.Save(ctx, l); err != nil {
		return nil, nil, "", err
	}
	return &RestockedListing{ListingID: l.ID(), CatalogItemID: line.CatalogItemID, AddedQty: line.QtyRestockable}, raised, "", nil
}

// record counts the skips of a committed event.
func (uc *ApplyReturnCompletion) record(ctx context.Context, eventType string, res ReturnCompletionResult) {
	m := uc.returnMetrics()
	if res.LinesAbsent {
		m.ReturnSkipped(ctx, eventType, SkipLinesAbsent)
	}
	for _, s := range res.Skipped {
		m.ReturnSkipped(ctx, eventType, s.Reason)
	}
}
