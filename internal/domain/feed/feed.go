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
	"slices"
	"strings"
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

// Result is what applying a feed to its listings produced.
type Result struct {
	// Events are the ListingAvailabilityChanged events, in processing order.
	Events []shared.Event
	// Changed are the listings that must be saved, in processing order.
	Changed []*listing.Listing
	// Applied counts lines that replaced a listing's availability.
	Applied int
	// Stale counts lines skipped because their asOf was not after the listing's.
	Stale int
	// Zeroed counts listings set to 0 by FullFeedZeroing.
	Zeroed int
}

// Apply evaluates the submission against the listings the use case loaded
// (every listing the lines name and, for a full feed, every listing of the
// node). It validates every line first and returns the first offending line:
// ErrListingNotOnNode, or listing.ErrNegativeAvailability. Nothing is applied
// then. Otherwise each line is applied (a stale line is skipped and counted) and,
// for a full feed, the omitted listings of the node are zeroed (A10). The feed
// itself is not changed: call MarkApplied or MarkFailed. After an error the
// loaded listings must be discarded, never saved.
func (f *InventoryFeed) Apply(sub Submission, listings map[listing.ID]*listing.Listing, now time.Time) (Result, error) {
	if err := f.requireReceived(); err != nil {
		return Result{}, err
	}
	if !f.matches(sub) {
		return Result{}, shared.Invalid("submission does not match feed %s", f.id)
	}
	if err := f.validateLines(sub, listings); err != nil {
		return Result{}, err
	}
	var res Result
	covered := make(map[listing.ID]struct{}, len(sub.Lines))
	for _, line := range sub.Lines {
		covered[line.ListingID] = struct{}{}
		l := listings[line.ListingID]
		events, err := l.ApplyAvailability(line.Qty, line.Basis, sub.AsOf, string(f.id), now)
		if errors.Is(err, listing.ErrStaleAvailability) {
			res.Stale++
			continue
		}
		if err != nil {
			return Result{}, err
		}
		res.Applied++
		res.Events = append(res.Events, events...)
		res.Changed = append(res.Changed, l)
	}
	if f.full {
		if err := f.zeroOmitted(sub, listings, covered, now, &res); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// matches reports whether sub is the submission this feed recorded.
func (f *InventoryFeed) matches(sub Submission) bool {
	return sub.ID == f.id && sub.NodeID == f.node && sub.Full == f.full &&
		len(sub.Lines) == f.lineCount && sub.AsOf.Equal(f.asOf)
}

// validateLines checks every line before anything is applied: it names a
// listing of the node (ErrListingNotOnNode) and its quantity is not negative.
func (f *InventoryFeed) validateLines(sub Submission, listings map[listing.ID]*listing.Listing) error {
	for _, line := range sub.Lines {
		l, ok := listings[line.ListingID]
		if !ok || l.NodeID() != f.node {
			return fmt.Errorf("%w: listing %s is not a listing of node %s", ErrListingNotOnNode, line.ListingID, f.node)
		}
		if line.Qty < 0 {
			return fmt.Errorf("%w: listing %s, availableQty %d", listing.ErrNegativeAvailability, line.ListingID, line.Qty)
		}
	}
	return nil
}

// zeroOmitted is FullFeedZeroing: it zeroes, in listing id order, every listing
// of the node that has no line.
func (f *InventoryFeed) zeroOmitted(sub Submission, listings map[listing.ID]*listing.Listing, covered map[listing.ID]struct{}, now time.Time, res *Result) error {
	omitted := make([]*listing.Listing, 0, len(listings))
	for id, l := range listings {
		if _, ok := covered[id]; !ok && l.NodeID() == f.node {
			omitted = append(omitted, l)
		}
	}
	slices.SortFunc(omitted, func(a, b *listing.Listing) int { return strings.Compare(string(a.ID()), string(b.ID())) })
	for _, l := range omitted {
		events, err := l.ZeroOut(sub.AsOf, string(f.id), now)
		if errors.Is(err, listing.ErrStaleAvailability) {
			continue
		}
		if err != nil {
			return err
		}
		if len(events) == 0 {
			continue
		}
		res.Zeroed++
		res.Events = append(res.Events, events...)
		res.Changed = append(res.Changed, l)
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

// Restore rebuilds a persisted feed, validating every invariant.
func Restore(s Snapshot) (*InventoryFeed, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("feed %q: %v", s.ID, err)
	}
	return &InventoryFeed{
		id: s.ID, node: s.NodeID, full: s.FullUpdate, asOf: shared.UTC(s.AsOf), lineCount: s.LineCount,
		state: s.State, applied: s.AppliedCount, stale: s.StaleCount, zeroed: s.ZeroedCount, failure: s.FailureCode,
		receivedAt: shared.UTC(s.ReceivedAt), appliedAt: shared.UTC(s.AppliedAt), version: s.Version,
	}, nil
}

func checkSnapshot(s Snapshot) error {
	if !s.ID.Valid() || !s.NodeID.Valid() {
		return errors.New("feedId and nodeId must be valid ids")
	}
	if !s.State.Valid() {
		return fmt.Errorf("state %q is unknown", s.State)
	}
	if s.AsOf.IsZero() || s.ReceivedAt.IsZero() {
		return errors.New("asOf and receivedAt are required")
	}
	if s.LineCount < 0 || s.LineCount > MaxLines || (s.LineCount == 0 && !s.FullUpdate) {
		return errors.New("lineCount is out of range")
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	return checkOutcome(s)
}

// checkOutcome checks the counters, failure code and appliedAt against the state.
func checkOutcome(s Snapshot) error {
	counters := s.AppliedCount + s.StaleCount
	if s.AppliedCount < 0 || s.StaleCount < 0 || s.ZeroedCount < 0 || counters > s.LineCount {
		return errors.New("counters are out of range")
	}
	if s.State != StateApplied && (counters != 0 || s.ZeroedCount != 0) {
		return errors.New("only an APPLIED feed has counters")
	}
	if s.ZeroedCount > 0 && !s.FullUpdate {
		return errors.New("only a full feed zeroes listings")
	}
	if (s.State == StateApplied) == s.AppliedAt.IsZero() {
		return errors.New("appliedAt is set exactly when the feed is APPLIED")
	}
	validCode := s.FailureCode == CodeListingNotOnNode || s.FailureCode == CodeNegativeAvailability
	if (s.State == StateFailed) != (s.FailureCode != "") || (s.State == StateFailed && !validCode) {
		return errors.New("a failure code is set exactly when the feed is FAILED")
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
