package listing

import (
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ListingCreated is raised by Create. Its fields are the model's data list.
type ListingCreated struct {
	shared.Header
	ListingID     ID
	CatalogItemID ItemID
	NodeID        NodeID
	// OwnerRef is the optional owning seller; empty = operator-owned.
	OwnerRef OwnerRef
}

// EventName returns "ListingCreated".
func (ListingCreated) EventName() string { return "ListingCreated" }

// Entity returns "listing".
func (ListingCreated) Entity() string { return "listing" }

// SubjectID returns the listingId.
func (e ListingCreated) SubjectID() string { return string(e.ListingID) }

// ListingAvailabilityChanged is raised by ApplyAvailability and ZeroOut.
type ListingAvailabilityChanged struct {
	shared.Header
	ListingID     ID
	CatalogItemID ItemID
	NodeID        NodeID
	OwnerRef      OwnerRef
	AvailableQty  int
	Basis         Basis
	AsOf          time.Time
	FeedID        string
}

// EventName returns "ListingAvailabilityChanged".
func (ListingAvailabilityChanged) EventName() string { return "ListingAvailabilityChanged" }

// Entity returns "listing".
func (ListingAvailabilityChanged) Entity() string { return "listing" }

// SubjectID returns the listingId.
func (e ListingAvailabilityChanged) SubjectID() string { return string(e.ListingID) }

// ListingSuppressed is raised by Suppress and by Create on a non-sellable node
// or a discontinued item.
type ListingSuppressed struct {
	shared.Header
	ListingID ID
	NodeID    NodeID
	Reason    Reason
}

// EventName returns "ListingSuppressed".
func (ListingSuppressed) EventName() string { return "ListingSuppressed" }

// Entity returns "listing".
func (ListingSuppressed) Entity() string { return "listing" }

// SubjectID returns the listingId.
func (e ListingSuppressed) SubjectID() string { return string(e.ListingID) }

// ListingRestored is raised by Restore.
type ListingRestored struct {
	shared.Header
	ListingID ID
	NodeID    NodeID
}

// EventName returns "ListingRestored".
func (ListingRestored) EventName() string { return "ListingRestored" }

// Entity returns "listing".
func (ListingRestored) Entity() string { return "listing" }

// SubjectID returns the listingId.
func (e ListingRestored) SubjectID() string { return string(e.ListingID) }

var (
	_ shared.Event = ListingCreated{}
	_ shared.Event = ListingAvailabilityChanged{}
	_ shared.Event = ListingSuppressed{}
	_ shared.Event = ListingRestored{}
)
