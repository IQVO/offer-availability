package feed

import (
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// InventoryFeedApplied is raised by MarkApplied. Its fields are the model's data
// list.
type InventoryFeedApplied struct {
	shared.Header
	FeedID     ID
	NodeID     listing.NodeID
	FullUpdate bool
	LineCount  int
	AppliedAt  time.Time
}

// EventName returns "InventoryFeedApplied".
func (InventoryFeedApplied) EventName() string { return "InventoryFeedApplied" }

// Entity returns "inventoryfeed".
func (InventoryFeedApplied) Entity() string { return "inventoryfeed" }

// SubjectID returns the feedId.
func (e InventoryFeedApplied) SubjectID() string { return string(e.FeedID) }

var _ shared.Event = InventoryFeedApplied{}
