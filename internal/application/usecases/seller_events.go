package usecases

import (
	"context"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// SellerEvent is one seller-inbound status event (SellerSuspended or
// SellerReinstated), decoded by the Kafka adapter.
type SellerEvent struct {
	// EventID is the CloudEvents id (the inbox key) and EventType the full type.
	EventID   string
	EventType string
	SellerID  sellerstatus.ID
	// Status is SUSPENDED for SellerSuspended and ACTIVE for SellerReinstated.
	Status sellerstatus.Status
	// At is suspendedAt or reinstatedAt.
	At time.Time
}

// ApplySellerEvent folds a seller status event into the SellerStatusView and
// runs the policy of ADR 0013 over the listings the seller owns.
type ApplySellerEvent struct{ writer }

// NewApplySellerEvent builds the use case.
func NewApplySellerEvent(d Deps) *ApplySellerEvent { return &ApplySellerEvent{writer{d}} }

// Execute handles one event in ONE unit of work, inbox row included:
//
//   - a CloudEvents id already in the inbox is skipped;
//   - the event goes through sellerstatus.View.Observe (an unknown seller is
//     created from it); an Ignored observation (older than the stored asOf)
//     touches nothing;
//   - a Changed or Repeated SUSPENDED suppresses every LIVE listing of the
//     seller with OWNER_SUSPENDED (rule 4); a listing already SUPPRESSED keeps
//     its reason, and the view itself is the record that the owner is suspended;
//   - a Changed or Repeated ACTIVE reconciles every listing of the seller whose
//     reason is OWNER_SUSPENDED: restored when no other cause holds, otherwise
//     switched to the remaining highest cause (rule 5).
//
// A malformed event is shared.ErrValidation; the adapter dead-letters it.
func (uc *ApplySellerEvent) Execute(ctx context.Context, ev SellerEvent) error {
	_, err := transact(ctx, uc.UoW, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, uc.handle(ctx, ev)
	})
	return err
}

func (uc *ApplySellerEvent) handle(ctx context.Context, ev SellerEvent) error {
	if ev.EventID == "" || ev.EventType == "" {
		return shared.Invalid("a seller event needs its CloudEvents id and type")
	}
	first, err := uc.Inbox.MarkProcessed(ctx, ev.EventID, ev.EventType, uc.now())
	if err != nil || !first {
		return err
	}
	view, outcome, err := uc.observe(ctx, ev)
	if err != nil || outcome == sellerstatus.Ignored {
		return err
	}
	command := uc.reinstate
	if view.Suspended() {
		command = uc.suspend
	}
	events, err := uc.eachOwned(ctx, listing.OwnerRef(view.ID()), command)
	if err != nil {
		return err
	}
	return uc.publish(ctx, events)
}

// observe updates and saves the view; an unknown seller is created.
func (uc *ApplySellerEvent) observe(ctx context.Context, ev SellerEvent) (*sellerstatus.View, sellerstatus.Outcome, error) {
	v, err := uc.Sellers.Get(ctx, ev.SellerID)
	known, err := found(err)
	if err != nil {
		return nil, sellerstatus.Ignored, err
	}
	if !known {
		v, err = sellerstatus.New(ev.SellerID, ev.Status, ev.At)
		if err != nil {
			return nil, sellerstatus.Ignored, err
		}
		return v, sellerstatus.Changed, uc.Sellers.Save(ctx, v)
	}
	out, err := v.Observe(ev.Status, ev.At)
	if err != nil || out != sellerstatus.Changed {
		return v, out, err
	}
	return v, out, uc.Sellers.Save(ctx, v)
}

// suspend is SuppressListing(OWNER_SUSPENDED) for a LIVE listing; any other
// listing keeps its (higher or equal) reason.
func (uc *ApplySellerEvent) suspend(_ context.Context, l *listing.Listing) ([]shared.Event, error) {
	if l.State() != listing.StateLive {
		return nil, nil
	}
	return l.Suppress(listing.ReasonOwnerSuspended, uc.now())
}

// reinstate reconciles a listing suppressed for OWNER_SUSPENDED against its
// remaining causes; every other listing is left alone.
func (uc *ApplySellerEvent) reinstate(ctx context.Context, l *listing.Listing) ([]shared.Event, error) {
	if l.Reason() != listing.ReasonOwnerSuspended {
		return nil, nil
	}
	s, err := uc.sellability(ctx, l)
	if err != nil {
		return nil, err
	}
	return l.Reconcile(s, uc.now()), nil
}

// eachOwned applies command to every listing of the owner, saves the changed
// ones in bulk and returns their events in listing id order.
func (uc *ApplySellerEvent) eachOwned(
	ctx context.Context, owner listing.OwnerRef, command func(context.Context, *listing.Listing) ([]shared.Event, error),
) ([]shared.Event, error) {
	listings, err := uc.Listings.ListByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	var (
		changed []*listing.Listing
		events  []shared.Event
	)
	for _, l := range listings {
		ev, err := command(ctx, l)
		if err != nil {
			return nil, err
		}
		if len(ev) == 0 {
			continue
		}
		changed = append(changed, l)
		events = append(events, ev...)
	}
	if err := uc.Listings.SaveMany(ctx, changed); err != nil {
		return nil, err
	}
	return events, nil
}
