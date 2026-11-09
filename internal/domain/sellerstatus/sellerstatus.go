// Package sellerstatus is the SellerStatusView: the local, event-fed copy of
// each seller's status that decides whether the listings the seller owns may
// be LIVE (ADR 0013). Like nodestatus it is a read model, not an aggregate of
// the model, but it carries the same Snapshot/Restore/version discipline so its
// ordering rules are pure and tested here instead of in the Kafka adapter.
//
// Ordering (ADR 0013, rule 2): an observation older than the stored asOf is
// ignored; a repeated status changes nothing but lets the caller run the policy
// again; an unknown seller is ACTIVE and is created from its first observation.
package sellerstatus

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ID is a sellerId (the ownerRef of the listings the seller owns).
type ID string

// Valid reports whether id is a well-formed seller id.
func (id ID) Valid() bool { return shared.ValidID(string(id)) }

// Status is a seller's status as seller-inbound publishes it.
type Status string

// The seller statuses. A seller the view does not know is ACTIVE.
const (
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
)

// Valid reports whether s is one of the seller statuses.
func (s Status) Valid() bool { return s == StatusActive || s == StatusSuspended }

// Outcome says what Observe did with an observation.
type Outcome int

// The outcomes of Observe.
const (
	// Ignored: the observation is older than the stored asOf. Nothing changed;
	// do not run the policy.
	Ignored Outcome = iota
	// Repeated: the seller already has that status. Nothing changed; the policy
	// may run again and will find nothing to do.
	Repeated
	// Changed: the status changed. Save the view and run the policy.
	Changed
)

// View is the status of one seller.
type View struct {
	id      ID
	status  Status
	asOf    time.Time
	version int64
}

// Snapshot is the persisted form of a view. Restore validates it.
type Snapshot struct {
	ID      ID
	Status  Status
	AsOf    time.Time
	Version int64
}

// New creates the view of a seller first seen with status at the instant asOf
// (version 1).
func New(id ID, status Status, asOf time.Time) (*View, error) {
	if !id.Valid() {
		return nil, shared.Invalid("sellerId %q is not a valid id", id)
	}
	if !status.Valid() || asOf.IsZero() {
		return nil, shared.Invalid("a seller observation needs status ACTIVE or SUSPENDED and an instant")
	}
	return &View{id: id, status: status, asOf: shared.UTC(asOf), version: 1}, nil
}

// Observe folds a status event of the seller into the view.
func (v *View) Observe(status Status, asOf time.Time) (Outcome, error) {
	if !status.Valid() || asOf.IsZero() {
		return Ignored, shared.Invalid("a seller observation needs status ACTIVE or SUSPENDED and an instant")
	}
	asOf = shared.UTC(asOf)
	if asOf.Before(v.asOf) {
		return Ignored, nil
	}
	if v.status == status {
		return Repeated, nil
	}
	v.status, v.asOf = status, asOf
	v.version++
	return Changed, nil
}

// Suspended reports whether the seller's listings must stay off the shelf.
func (v *View) Suspended() bool { return v.status == StatusSuspended }

// Restore rebuilds a persisted view, validating every invariant.
func Restore(s Snapshot) (*View, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("seller status %q: %v", s.ID, err)
	}
	return &View{id: s.ID, status: s.Status, asOf: shared.UTC(s.AsOf), version: s.Version}, nil
}

func checkSnapshot(s Snapshot) error {
	if !s.ID.Valid() {
		return errors.New("sellerId is malformed")
	}
	if !s.Status.Valid() {
		return fmt.Errorf("status %q is unknown", s.Status)
	}
	if s.AsOf.IsZero() {
		return errors.New("asOf is required")
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	return nil
}

// ID returns the sellerId.
func (v *View) ID() ID { return v.id }

// Status returns the current status.
func (v *View) Status() Status { return v.status }

// AsOf returns the instant of the current status.
func (v *View) AsOf() time.Time { return v.asOf }

// Version returns the view version (one per saved change).
func (v *View) Version() int64 { return v.version }

// Snapshot returns the persisted form.
func (v *View) Snapshot() Snapshot {
	return Snapshot{ID: v.id, Status: v.status, AsOf: v.asOf, Version: v.version}
}
