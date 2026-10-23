package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// NodeEvent is one node-topology lifecycle event, decoded by the Kafka adapter.
type NodeEvent struct {
	// EventID is the CloudEvents id (the inbox key) and EventType the full type.
	EventID   string
	EventType string
	NodeID    nodestatus.ID
	// Status is PROPOSED for NodeRegistered, else the status the event announces.
	Status nodestatus.Status
	// At is the instant of the event: activatedAt, suspendedAt, retiredAt, or the
	// CloudEvents time for NodeRegistered.
	At time.Time
}

// ApplyNodeEvent folds a node-topology event into the NodeStatusView and runs
// the policy SuppressOnNodeUnavailable (the model's SuppressListing and
// RestoreListing commands).
type ApplyNodeEvent struct{ writer }

// NewApplyNodeEvent builds the use case.
func NewApplyNodeEvent(d Deps) *ApplyNodeEvent { return &ApplyNodeEvent{writer{d}} }

// Execute handles one event in ONE unit of work, inbox row included (ADR 0003):
//
//   - a CloudEvents id already in the inbox is skipped;
//   - NodeRegistered creates the view as PROPOSED when the node is unknown and is
//     a no-op otherwise;
//   - NodeActivated, NodeSuspended and NodeRetired go through
//     nodestatus.View.Observe; an Ignored observation (older than the status, or
//     the node is RETIRED) touches nothing;
//   - a Changed or Repeated observation runs the policy: ACTIVE restores the
//     listings of the node suppressed for NODE_NOT_SELLABLE, SUSPENDED and
//     RETIRED suppress every LIVE listing (ADR 0002, A13 and A14).
//
// A malformed event is shared.ErrValidation; the adapter dead-letters it.
func (uc *ApplyNodeEvent) Execute(ctx context.Context, ev NodeEvent) error {
	_, err := transact(ctx, uc.UoW, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, uc.handle(ctx, ev)
	})
	return err
}

func (uc *ApplyNodeEvent) handle(ctx context.Context, ev NodeEvent) error {
	if ev.EventID == "" || ev.EventType == "" {
		return shared.Invalid("a node event needs its CloudEvents id and type")
	}
	first, err := uc.Inbox.MarkProcessed(ctx, ev.EventID, ev.EventType, uc.now())
	if err != nil {
		return err
	}
	if !first {
		return nil
	}
	view, outcome, err := uc.observe(ctx, ev)
	if err != nil || outcome == nodestatus.Ignored {
		return err
	}
	var events []shared.Event
	if view.Sellable() {
		events, err = uc.restoreListings(ctx, view)
	} else {
		events, err = uc.suppressListings(ctx, view)
	}
	if err != nil {
		return err
	}
	return uc.publish(ctx, events)
}

// observe updates and saves the view. For NodeRegistered it returns Ignored
// (the policy has nothing to do for a PROPOSED node) unless nothing was known.
func (uc *ApplyNodeEvent) observe(ctx context.Context, ev NodeEvent) (*nodestatus.View, nodestatus.Outcome, error) {
	v, err := uc.Nodes.Get(ctx, ev.NodeID)
	known, err := found(err)
	if err != nil {
		return nil, nodestatus.Ignored, err
	}
	registered := ev.Status == nodestatus.StatusProposed
	switch {
	case !known:
		v, err = nodestatus.New(ev.NodeID, ev.Status, ev.At)
		if err != nil {
			return nil, nodestatus.Ignored, err
		}
		out := nodestatus.Changed
		if registered {
			out = nodestatus.Ignored
		}
		return v, out, uc.Nodes.Save(ctx, v)
	case registered:
		return v, nodestatus.Ignored, nil
	}
	out, err := v.Observe(ev.Status, ev.At)
	if err != nil || out != nodestatus.Changed {
		return v, out, err
	}
	return v, out, uc.Nodes.Save(ctx, v)
}

// suppressListings is SuppressListing for every LIVE listing of the node.
func (uc *ApplyNodeEvent) suppressListings(ctx context.Context, v *nodestatus.View) ([]shared.Event, error) {
	return uc.eachListing(ctx, v, func(l *listing.Listing) ([]shared.Event, error) {
		return l.Suppress(listing.ReasonNodeNotSellable, uc.now())
	})
}

// restoreListings is RestoreListing for every listing of the node suppressed for
// NODE_NOT_SELLABLE.
func (uc *ApplyNodeEvent) restoreListings(ctx context.Context, v *nodestatus.View) ([]shared.Event, error) {
	return uc.eachListing(ctx, v, func(l *listing.Listing) ([]shared.Event, error) {
		return l.Restore(v.Sellable(), uc.now())
	})
}

// eachListing applies command to every listing of the node, skipping the ones
// for which it is illegal or refused (a replay is a no-op, ADR 0002, A14), saves
// the changed ones in bulk and returns their events.
func (uc *ApplyNodeEvent) eachListing(
	ctx context.Context, v *nodestatus.View, command func(*listing.Listing) ([]shared.Event, error),
) ([]shared.Event, error) {
	listings, err := uc.Listings.ListByNode(ctx, listing.NodeID(v.ID()))
	if err != nil {
		return nil, err
	}
	var (
		changed []*listing.Listing
		events  []shared.Event
	)
	for _, l := range listings {
		ev, err := command(l)
		if errors.Is(err, shared.ErrIllegalTransition) || errors.Is(err, listing.ErrNodeNotSellable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		changed = append(changed, l)
		events = append(events, ev...)
	}
	if err := uc.Listings.SaveMany(ctx, changed); err != nil {
		return nil, err
	}
	return events, nil
}
