// Package listing is the Listing aggregate: one catalogue item offered from one
// node, with the node's claimed available quantity, its basis and the feed it
// came from.
//
// Two states, LIVE and SUPPRESSED; a SUPPRESSED listing carries the reason it
// is not sellable. Transitions: LIVE -> SUPPRESSED (SuppressListing) and
// SUPPRESSED -> LIVE (RestoreListing). ApplyAvailability replaces the
// Availability value object and never changes the state (ADR 0002, A8).
// Invariants: availableQty >= 0 (ErrNegativeAvailability); an update with
// asOf <= the current asOf is ignored (ErrStaleAvailability); a listing on a
// node that is not ACTIVE is SUPPRESSED (ErrNodeNotSellable); one listing per
// (catalogItemId, nodeId) (ErrDuplicateListing, enforced by the repository).
package listing

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// The Listing invariants of the model, by the model's names.
var (
	// ErrNegativeAvailability: availableQty >= 0 (422).
	ErrNegativeAvailability = errors.New("negative availability")
	// ErrStaleAvailability: an update not newer than the current asOf is ignored.
	ErrStaleAvailability = errors.New("stale availability")
	// ErrNodeNotSellable: a listing on a node that is not ACTIVE is SUPPRESSED (409).
	ErrNodeNotSellable = errors.New("node not sellable")
	// ErrDuplicateListing: one listing per (catalogItemId, nodeId) (409).
	ErrDuplicateListing = errors.New("duplicate listing")
)

// MaxQty is the Postgres integer ceiling for an available quantity.
const MaxQty = 2147483647

// ID identifies a listing (listingId).
type ID string

// ItemID is the catalogItemId a listing offers.
type ItemID string

// NodeID is the node a listing is offered from.
type NodeID string

// Valid reports whether id is a well-formed aggregate id.
func (id ID) Valid() bool { return shared.ValidID(string(id)) }

// Valid reports whether id is a well-formed catalogue item id.
func (id ItemID) Valid() bool { return shared.ValidID(string(id)) }

// Valid reports whether id is a well-formed node id.
func (id NodeID) Valid() bool { return shared.ValidID(string(id)) }

// State is the lifecycle state of a listing.
type State string

// The listing states of the model.
const (
	StateLive       State = "LIVE"
	StateSuppressed State = "SUPPRESSED"
)

// Valid reports whether s is one of the model's states.
func (s State) Valid() bool { return s == StateLive || s == StateSuppressed }

// Reason says why a listing is SUPPRESSED (ADR 0002, A4 and A14).
type Reason string

// The suppression reasons. DISCONTINUED is final.
const (
	ReasonNodeNotSellable Reason = "NODE_NOT_SELLABLE"
	ReasonDiscontinued    Reason = "DISCONTINUED"
)

// Valid reports whether r is a suppression reason.
func (r Reason) Valid() bool { return r == ReasonNodeNotSellable || r == ReasonDiscontinued }

// Basis is why the node gave the quantity, kept as the vendor declared it.
type Basis string

// The availability bases of the model.
const (
	BasisPhysical              Basis = "PHYSICAL"
	BasisThroughputConstrained Basis = "THROUGHPUT_CONSTRAINED"
	BasisUnknown               Basis = "UNKNOWN"
)

// Valid reports whether b is one of the model's bases.
func (b Basis) Valid() bool {
	return b == BasisPhysical || b == BasisThroughputConstrained || b == BasisUnknown
}

// Availability is the value object {availableQty, basis, asOf, feedId}. Before
// the first feed it is 0, UNKNOWN, with a zero AsOf and no FeedID (A7).
type Availability struct {
	Qty    int
	Basis  Basis
	AsOf   time.Time
	FeedID string
}

// Sellability is what the use case read about the neighbours of a new listing.
type Sellability struct {
	// NodeActive is true only when the local NodeStatusView says ACTIVE.
	NodeActive bool
	// ItemDiscontinued is true when the catalogue item is DISCONTINUED.
	ItemDiscontinued bool
}

// Listing is the aggregate root.
type Listing struct {
	id      ID
	item    ItemID
	node    NodeID
	state   State
	reason  Reason
	avail   Availability
	version int64
}

// Snapshot is the persisted form of a listing. Restore validates it.
type Snapshot struct {
	ID      ID
	ItemID  ItemID
	NodeID  NodeID
	State   State
	Reason  Reason
	Qty     int
	Basis   Basis
	AsOf    time.Time
	FeedID  string
	Version int64
}

// Create builds a listing (aggregate version 1) and raises ListingCreated. It
// starts LIVE only when the node is ACTIVE and the item is not discontinued;
// otherwise it starts SUPPRESSED, DISCONTINUED winning over NODE_NOT_SELLABLE,
// and also raises ListingSuppressed (A5, A6).
func Create(id ID, item ItemID, node NodeID, s Sellability, now time.Time) (*Listing, []shared.Event, error) {
	if !id.Valid() || !item.Valid() || !node.Valid() {
		return nil, nil, shared.Invalid("listingId, catalogItemId and nodeId must be valid ids")
	}
	at := shared.UTC(now)
	l := &Listing{id: id, item: item, node: node, state: StateLive, avail: Availability{Basis: BasisUnknown}, version: 1}
	events := []shared.Event{ListingCreated{
		Header:    shared.Header{Version: 1, At: at},
		ListingID: id, CatalogItemID: item, NodeID: node,
	}}
	switch {
	case s.ItemDiscontinued:
		l.state, l.reason = StateSuppressed, ReasonDiscontinued
	case !s.NodeActive:
		l.state, l.reason = StateSuppressed, ReasonNodeNotSellable
	default:
		return l, events, nil
	}
	events = append(events, ListingSuppressed{Header: shared.Header{Version: 1, At: at}, ListingID: id, NodeID: node, Reason: l.reason})
	return l, events, nil
}

// ApplyAvailability replaces the availability and raises
// ListingAvailabilityChanged. Check order (A9): the inputs are well formed
// (ErrValidation), qty >= 0 (ErrNegativeAvailability), then asOf is later than
// the current one (ErrStaleAvailability). It changes the receiver only on
// success. The state is not touched (A8).
func (l *Listing) ApplyAvailability(qty int, basis Basis, asOf time.Time, feedID string, now time.Time) ([]shared.Event, error) {
	if !basis.Valid() || asOf.IsZero() || !shared.ValidID(feedID) {
		return nil, shared.Invalid("basis, asOf and feedId are required and must be valid")
	}
	if qty > MaxQty {
		return nil, shared.Invalid("availableQty %d is above %d", qty, MaxQty)
	}
	if qty < 0 {
		return nil, fmt.Errorf("%w: listing %s, availableQty %d", ErrNegativeAvailability, l.id, qty)
	}
	if !l.avail.AsOf.IsZero() && !asOf.After(l.avail.AsOf) {
		return nil, fmt.Errorf("%w: listing %s, asOf %s is not after %s", ErrStaleAvailability, l.id,
			asOf.UTC().Format(time.RFC3339), l.avail.AsOf.Format(time.RFC3339))
	}
	l.avail = Availability{Qty: qty, Basis: basis, AsOf: shared.UTC(asOf), FeedID: feedID}
	l.version++
	return []shared.Event{ListingAvailabilityChanged{
		Header:    shared.Header{Version: l.version, At: shared.UTC(now)},
		ListingID: l.id, CatalogItemID: l.item, NodeID: l.node,
		AvailableQty: qty, Basis: basis, AsOf: l.avail.AsOf, FeedID: feedID,
	}}, nil
}

// ZeroOut is FullFeedZeroing for one listing: availableQty becomes 0, keeping
// the basis and stamping the feed's asOf and id. A listing already at 0 is
// left untouched (nil events, nil error); a listing whose asOf is not before
// the feed's returns ErrStaleAvailability (A10).
func (l *Listing) ZeroOut(asOf time.Time, feedID string, now time.Time) ([]shared.Event, error) {
	if l.avail.Qty == 0 {
		return nil, nil
	}
	return l.ApplyAvailability(0, l.avail.Basis, asOf, feedID, now)
}

// Suppress moves LIVE -> SUPPRESSED with the reason and raises
// ListingSuppressed. A listing suppressed for NODE_NOT_SELLABLE may be
// suppressed again for DISCONTINUED (the reason changes, A4); every other
// suppression of a SUPPRESSED listing is ErrIllegalTransition.
func (l *Listing) Suppress(reason Reason, now time.Time) ([]shared.Event, error) {
	if !reason.Valid() {
		return nil, shared.Invalid("suppression reason %q is unknown", reason)
	}
	upgrade := l.state == StateSuppressed && l.reason == ReasonNodeNotSellable && reason == ReasonDiscontinued
	if l.state == StateSuppressed && !upgrade {
		return nil, fmt.Errorf("%w: listing %s is already SUPPRESSED (%s)", shared.ErrIllegalTransition, l.id, l.reason)
	}
	l.state, l.reason = StateSuppressed, reason
	l.version++
	return []shared.Event{ListingSuppressed{
		Header:    shared.Header{Version: l.version, At: shared.UTC(now)},
		ListingID: l.id, NodeID: l.node, Reason: reason,
	}}, nil
}

// Restore moves SUPPRESSED -> LIVE and raises ListingRestored. Check order: the
// listing is SUPPRESSED, its reason is not DISCONTINUED (both
// ErrIllegalTransition), then the node is sellable (ErrNodeNotSellable).
func (l *Listing) Restore(nodeSellable bool, now time.Time) ([]shared.Event, error) {
	if l.state != StateSuppressed {
		return nil, fmt.Errorf("%w: listing %s is not SUPPRESSED", shared.ErrIllegalTransition, l.id)
	}
	if l.reason == ReasonDiscontinued {
		return nil, fmt.Errorf("%w: listing %s is suppressed for DISCONTINUED, which is final", shared.ErrIllegalTransition, l.id)
	}
	if !nodeSellable {
		return nil, fmt.Errorf("%w: node %s", ErrNodeNotSellable, l.node)
	}
	l.state, l.reason = StateLive, ""
	l.version++
	return []shared.Event{ListingRestored{
		Header:    shared.Header{Version: l.version, At: shared.UTC(now)},
		ListingID: l.id, NodeID: l.node,
	}}, nil
}

// Restore rebuilds a persisted listing, validating every invariant.
func Restore(s Snapshot) (*Listing, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("listing %q: %v", s.ID, err)
	}
	return &Listing{
		id: s.ID, item: s.ItemID, node: s.NodeID, state: s.State, reason: s.Reason, version: s.Version,
		avail: Availability{Qty: s.Qty, Basis: s.Basis, AsOf: shared.UTC(s.AsOf), FeedID: s.FeedID},
	}, nil
}

func checkSnapshot(s Snapshot) error {
	if !s.ID.Valid() || !s.ItemID.Valid() || !s.NodeID.Valid() {
		return errors.New("listingId, catalogItemId and nodeId must be valid ids")
	}
	if err := checkStateReason(s.State, s.Reason); err != nil {
		return err
	}
	if s.Qty < 0 || s.Qty > MaxQty || !s.Basis.Valid() {
		return errors.New("availableQty or basis is out of range")
	}
	if s.AsOf.IsZero() != (s.FeedID == "") || (s.FeedID != "" && !shared.ValidID(s.FeedID)) {
		return errors.New("asOf and feedId are set together and feedId must be a valid id")
	}
	if s.AsOf.IsZero() && (s.Qty != 0 || s.Basis != BasisUnknown) {
		return errors.New("a listing that never had a feed has availableQty 0 and basis UNKNOWN")
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	return nil
}

func checkStateReason(st State, r Reason) error {
	if !st.Valid() {
		return fmt.Errorf("state %q is unknown", st)
	}
	if st == StateLive && r != "" {
		return errors.New("a LIVE listing has no suppression reason")
	}
	if st == StateSuppressed && !r.Valid() {
		return errors.New("a SUPPRESSED listing needs a valid suppression reason")
	}
	return nil
}

// ID returns the listingId.
func (l *Listing) ID() ID { return l.id }

// ItemID returns the catalogItemId.
func (l *Listing) ItemID() ItemID { return l.item }

// NodeID returns the node the listing is offered from.
func (l *Listing) NodeID() NodeID { return l.node }

// State returns the lifecycle state.
func (l *Listing) State() State { return l.state }

// Reason returns the suppression reason, empty while LIVE.
func (l *Listing) Reason() Reason { return l.reason }

// Availability returns the current availability value object.
func (l *Listing) Availability() Availability { return l.avail }

// Version returns the aggregate version (one per saved change).
func (l *Listing) Version() int64 { return l.version }

// Snapshot returns the persisted form.
func (l *Listing) Snapshot() Snapshot {
	return Snapshot{
		ID: l.id, ItemID: l.item, NodeID: l.node, State: l.state, Reason: l.reason,
		Qty: l.avail.Qty, Basis: l.avail.Basis, AsOf: l.avail.AsOf, FeedID: l.avail.FeedID, Version: l.version,
	}
}
