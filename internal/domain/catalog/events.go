package catalog

import "github.com/claudioed/offer-availability/internal/domain/shared"

// CatalogItemCreated is raised by Create. Its fields are the model's data list.
type CatalogItemCreated struct {
	shared.Header
	CatalogItemID ID
	Title         string
	LengthMm      int
	WidthMm       int
	HeightMm      int
	WeightG       int
	Hazmat        bool
	Fragile       bool
}

// EventName returns "CatalogItemCreated".
func (CatalogItemCreated) EventName() string { return "CatalogItemCreated" }

// Entity returns "catalogitem".
func (CatalogItemCreated) Entity() string { return "catalogitem" }

// SubjectID returns the catalogItemId.
func (e CatalogItemCreated) SubjectID() string { return string(e.CatalogItemID) }

// CatalogItemDiscontinued is raised by Discontinue.
type CatalogItemDiscontinued struct {
	shared.Header
	CatalogItemID ID
}

// EventName returns "CatalogItemDiscontinued".
func (CatalogItemDiscontinued) EventName() string { return "CatalogItemDiscontinued" }

// Entity returns "catalogitem".
func (CatalogItemDiscontinued) Entity() string { return "catalogitem" }

// SubjectID returns the catalogItemId.
func (e CatalogItemDiscontinued) SubjectID() string { return string(e.CatalogItemID) }

var (
	_ shared.Event = CatalogItemCreated{}
	_ shared.Event = CatalogItemDiscontinued{}
)
