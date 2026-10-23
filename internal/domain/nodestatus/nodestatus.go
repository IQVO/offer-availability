// Package nodestatus is the NodeStatusView: the local, event-fed copy of each
// node's lifecycle status that decides whether a listing is sellable (ADR 0003).
// It is a read model, not an aggregate of the model, but it carries the same
// Snapshot/Restore/version discipline so that its ordering rules are pure and
// tested here instead of in the Kafka adapter.
//
// Ordering (ADR 0002, A13): an observation older than the instant of the current
// status is ignored; RETIRED is terminal; a repeated status changes nothing but
// lets the caller run the policy again; an unknown node is created from its
// first observation.
package nodestatus

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ID is a nodeId.
type ID string

// Valid reports whether id is a well-formed node id.
func (id ID) Valid() bool { return shared.ValidID(string(id)) }

// Status is a node's lifecycle status as node-topology publishes it.
type Status string

// The node statuses.
const (
	StatusProposed  Status = "PROPOSED"
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusRetired   Status = "RETIRED"
)

// Valid reports whether s is one of the node statuses.
func (s Status) Valid() bool {
	return s == StatusProposed || s == StatusActive || s == StatusSuspended || s == StatusRetired
}

// Outcome says what Observe did with an observation.
type Outcome int

// The outcomes of Observe.
const (
	// Ignored: the observation is older than the current status, or the node is
	// RETIRED and the observation is not. Nothing changed; do not run the policy.
	Ignored Outcome = iota
	// Repeated: the node already has that status. Nothing changed; the policy may
	// run again and will find nothing to do.
	Repeated
	// Changed: the status changed. Save the view and run the policy.
	Changed
)

// View is the status of one node.
type View struct {
	id      ID
	status  Status
	at      time.Time
	version int64
}

// Snapshot is the persisted form of a view. Restore validates it.
type Snapshot struct {
	ID       ID
	Status   Status
	StatusAt time.Time
	Version  int64
}

// New creates the view of a node first seen with status at the instant at
// (aggregate version 1).
func New(id ID, status Status, at time.Time) (*View, error) {
	if !id.Valid() {
		return nil, shared.Invalid("nodeId %q is not a valid id", id)
	}
	if !status.Valid() || at.IsZero() {
		return nil, shared.Invalid("a node observation needs a known status and an instant")
	}
	return &View{id: id, status: status, at: shared.UTC(at), version: 1}, nil
}

// Observe folds a status event of the node into the view. Only ACTIVE,
// SUSPENDED and RETIRED are observed this way (PROPOSED is the creation status).
func (v *View) Observe(status Status, at time.Time) (Outcome, error) {
	if (status != StatusActive && status != StatusSuspended && status != StatusRetired) || at.IsZero() {
		return Ignored, shared.Invalid("a node observation needs status ACTIVE, SUSPENDED or RETIRED and an instant")
	}
	if v.status == StatusRetired && status != StatusRetired {
		return Ignored, nil
	}
	if at.Before(v.at) {
		return Ignored, nil
	}
	if v.status == status {
		return Repeated, nil
	}
	v.status, v.at = status, shared.UTC(at)
	v.version++
	return Changed, nil
}

// Sellable reports whether listings on this node may be LIVE: only ACTIVE.
func (v *View) Sellable() bool { return v.status == StatusActive }

// Restore rebuilds a persisted view, validating every invariant.
func Restore(s Snapshot) (*View, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("node status %q: %v", s.ID, err)
	}
	return &View{id: s.ID, status: s.Status, at: shared.UTC(s.StatusAt), version: s.Version}, nil
}

func checkSnapshot(s Snapshot) error {
	if !s.ID.Valid() {
		return errors.New("nodeId is malformed")
	}
	if !s.Status.Valid() {
		return fmt.Errorf("status %q is unknown", s.Status)
	}
	if s.StatusAt.IsZero() {
		return errors.New("statusAt is required")
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	return nil
}

// ID returns the nodeId.
func (v *View) ID() ID { return v.id }

// Status returns the current status.
func (v *View) Status() Status { return v.status }

// StatusAt returns the instant of the current status.
func (v *View) StatusAt() time.Time { return v.at }

// Version returns the view version (one per saved change).
func (v *View) Version() int64 { return v.version }

// Snapshot returns the persisted form.
func (v *View) Snapshot() Snapshot {
	return Snapshot{ID: v.id, Status: v.status, StatusAt: v.at, Version: v.version}
}
