// Package feed is the InventoryFeed aggregate: one vendor inventory submission
// for one node, full or partial, evaluated in one transaction.
//
// States: RECEIVED (initial), APPLIED, FAILED and SUPERSEDED (Gap Register G23).
// Transitions: RECEIVED -> APPLIED, RECEIVED -> FAILED, RECEIVED -> SUPERSEDED
// (ApplyInventoryFeed) and FAILED -> RECEIVED (the same feedId sent again).
// APPLIED and SUPERSEDED are terminal. Invariants: every line names a listing of
// the feed's node (ErrListingNotOnNode); a feed is applied at most once
// (ErrFeedAlreadyApplied); a quantity is not negative
// (listing.ErrNegativeAvailability). The aggregate stores counters, never lines
// (ADR 0002, A11).
package feed

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// The InventoryFeed invariants of the model, by the model's names.
var (
	// ErrListingNotOnNode: every line names a listing of the feed's node (422).
	ErrListingNotOnNode = errors.New("listing not on node")
	// ErrFeedAlreadyApplied: a feed is applied at most once (409).
	ErrFeedAlreadyApplied = errors.New("feed already applied")
)

// MaxLines bounds a feed (Gap Register G23); larger feeds are split by the vendor.
const MaxLines = 10000

// The failure codes a FAILED feed stores: the Err identifiers of the model.
const (
	CodeListingNotOnNode     = "ErrListingNotOnNode"
	CodeNegativeAvailability = "ErrNegativeAvailability"
)

// ID identifies a feed (feedId), chosen by the caller.
type ID string

// Valid reports whether id is a well-formed aggregate id.
func (id ID) Valid() bool { return shared.ValidID(string(id)) }

// State is the lifecycle state of a feed.
type State string

// The feed states.
const (
	StateReceived   State = "RECEIVED"
	StateApplied    State = "APPLIED"
	StateFailed     State = "FAILED"
	StateSuperseded State = "SUPERSEDED"
)

// Valid reports whether s is one of the feed states.
func (s State) Valid() bool {
	return s == StateReceived || s == StateApplied || s == StateFailed || s == StateSuperseded
}

// Line is one listing's declared availability.
type Line struct {
	ListingID listing.ID
	Qty       int
	Basis     listing.Basis
}

// Submission is a validated feed request.
type Submission struct {
	ID     ID
	NodeID listing.NodeID
	Full   bool
	AsOf   time.Time
	Lines  []Line
}

// NewSubmission validates a feed request: ids, asOf, at most MaxLines lines, a
// partial feed has at least one line, at most one line per listing, a valid
// basis and a quantity not above listing.MaxQty. A negative quantity is NOT
// rejected here: it fails the feed (ErrNegativeAvailability).
func NewSubmission(id ID, node listing.NodeID, full bool, asOf time.Time, lines []Line) (Submission, error) {
	if !id.Valid() {
		return Submission{}, shared.Invalid("feedId %q is not a valid id", id)
	}
	if !node.Valid() {
		return Submission{}, shared.Invalid("nodeId %q is not a valid id", node)
	}
	if asOf.IsZero() {
		return Submission{}, shared.Invalid("asOf is required")
	}
	if len(lines) > MaxLines {
		return Submission{}, shared.Invalid("a feed has at most %d lines, got %d", MaxLines, len(lines))
	}
	if len(lines) == 0 && !full {
		return Submission{}, shared.Invalid("a partial feed needs at least one line")
	}
	seen := make(map[listing.ID]struct{}, len(lines))
	for i, l := range lines {
		if err := checkLine(l); err != nil {
			return Submission{}, fmt.Errorf("line %d: %w", i, err)
		}
		if _, dup := seen[l.ListingID]; dup {
			return Submission{}, shared.Invalid("line %d: listing %s appears twice", i, l.ListingID)
		}
		seen[l.ListingID] = struct{}{}
	}
	return Submission{ID: id, NodeID: node, Full: full, AsOf: shared.UTC(asOf), Lines: append([]Line(nil), lines...)}, nil
}

func checkLine(l Line) error {
	if !l.ListingID.Valid() {
		return shared.Invalid("listingId %q is not a valid id", l.ListingID)
	}
	if !l.Basis.Valid() {
		return shared.Invalid("basis %q is unknown", l.Basis)
	}
	if l.Qty > listing.MaxQty {
		return shared.Invalid("availableQty %d is above %d", l.Qty, listing.MaxQty)
	}
	return nil
}

// Superseded is the node-level rule of G23: a feed whose asOf is strictly older
// than the asOf of the last APPLIED feed of the node is ignored. A zero
// lastApplied means the node has no applied feed yet.
func Superseded(asOf, lastApplied time.Time) bool {
	return !lastApplied.IsZero() && asOf.Before(lastApplied)
}

// InventoryFeed is the aggregate root.
type InventoryFeed struct {
	id         ID
	node       listing.NodeID
	full       bool
	asOf       time.Time
	lineCount  int
	state      State
	applied    int
	stale      int
	zeroed     int
	failure    string
	receivedAt time.Time
	appliedAt  time.Time
	version    int64
}

// Snapshot is the persisted form of a feed. Restore validates it.
type Snapshot struct {
	ID           ID
	NodeID       listing.NodeID
	FullUpdate   bool
	AsOf         time.Time
	LineCount    int
	State        State
	AppliedCount int
	StaleCount   int
	ZeroedCount  int
	FailureCode  string
	ReceivedAt   time.Time
	AppliedAt    time.Time
	Version      int64
}

// Receive records a submission as a RECEIVED feed (aggregate version 1).
func Receive(sub Submission, now time.Time) *InventoryFeed {
	return &InventoryFeed{
		id: sub.ID, node: sub.NodeID, full: sub.Full, asOf: sub.AsOf, lineCount: len(sub.Lines),
		state: StateReceived, receivedAt: shared.UTC(now), version: 1,
	}
}

// Resubmit takes a FAILED feed back to RECEIVED with the corrected submission
// (A11). The node must not change. An APPLIED or SUPERSEDED feed is
// ErrFeedAlreadyApplied; a RECEIVED one is ErrIllegalTransition.
func (f *InventoryFeed) Resubmit(sub Submission, now time.Time) error {
	switch f.state {
	case StateApplied, StateSuperseded:
		return fmt.Errorf("%w: feed %s is %s", ErrFeedAlreadyApplied, f.id, f.state)
	case StateReceived:
		return fmt.Errorf("%w: feed %s is still RECEIVED", shared.ErrIllegalTransition, f.id)
	}
	if sub.ID != f.id || sub.NodeID != f.node {
		return shared.Invalid("feed %s was sent for node %s and cannot change it", f.id, f.node)
	}
	*f = InventoryFeed{
		id: f.id, node: f.node, full: sub.Full, asOf: sub.AsOf, lineCount: len(sub.Lines),
		state: StateReceived, receivedAt: shared.UTC(now), version: f.version + 1,
	}
	return nil
}

// MarkApplied moves RECEIVED -> APPLIED with the counters of res and raises
// InventoryFeedApplied.
func (f *InventoryFeed) MarkApplied(res Result, now time.Time) ([]shared.Event, error) {
	if err := f.requireReceived(); err != nil {
		return nil, err
	}
	f.state, f.applied, f.stale, f.zeroed = StateApplied, res.Applied, res.Stale, res.Zeroed
	f.appliedAt = shared.UTC(now)
	f.version++
	return []shared.Event{InventoryFeedApplied{
		Header: shared.Header{Version: f.version, At: f.appliedAt},
		FeedID: f.id, NodeID: f.node, FullUpdate: f.full, LineCount: f.lineCount, AppliedAt: f.appliedAt,
	}}, nil
}

// MarkFailed moves RECEIVED -> FAILED, recording the invariant that broke. cause
// must be ErrListingNotOnNode or listing.ErrNegativeAvailability.
func (f *InventoryFeed) MarkFailed(cause error) error {
	if err := f.requireReceived(); err != nil {
		return err
	}
	var code string
	switch {
	case errors.Is(cause, ErrListingNotOnNode):
		code = CodeListingNotOnNode
	case errors.Is(cause, listing.ErrNegativeAvailability):
		code = CodeNegativeAvailability
	default:
		return shared.Invalid("a feed fails only on %s or %s", CodeListingNotOnNode, CodeNegativeAvailability)
	}
	f.state, f.failure = StateFailed, code
	f.version++
	return nil
}

// MarkSuperseded moves RECEIVED -> SUPERSEDED: an older feed than the last
// applied one of the node, ignored.
func (f *InventoryFeed) MarkSuperseded() error {
	if err := f.requireReceived(); err != nil {
		return err
	}
	f.state = StateSuperseded
	f.version++
	return nil
}

func (f *InventoryFeed) requireReceived() error {
	if f.state != StateReceived {
		return fmt.Errorf("%w: feed %s is %s", shared.ErrIllegalTransition, f.id, f.state)
	}
	return nil
}

// ID returns the feedId.
func (f *InventoryFeed) ID() ID { return f.id }

// NodeID returns the node the feed is for.
func (f *InventoryFeed) NodeID() listing.NodeID { return f.node }

// Full reports whether the feed is a full update.
func (f *InventoryFeed) Full() bool { return f.full }

// AsOf returns the instant the numbers are true for.
func (f *InventoryFeed) AsOf() time.Time { return f.asOf }

// LineCount returns the lines as received.
func (f *InventoryFeed) LineCount() int { return f.lineCount }

// State returns the lifecycle state.
func (f *InventoryFeed) State() State { return f.state }

// FailureCode returns the Err identifier of a FAILED feed, empty otherwise.
func (f *InventoryFeed) FailureCode() string { return f.failure }

// Version returns the aggregate version (one per saved change).
func (f *InventoryFeed) Version() int64 { return f.version }

// Snapshot returns the persisted form.
func (f *InventoryFeed) Snapshot() Snapshot {
	return Snapshot{
		ID: f.id, NodeID: f.node, FullUpdate: f.full, AsOf: f.asOf, LineCount: f.lineCount, State: f.state,
		AppliedCount: f.applied, StaleCount: f.stale, ZeroedCount: f.zeroed, FailureCode: f.failure,
		ReceivedAt: f.receivedAt, AppliedAt: f.appliedAt, Version: f.version,
	}
}
