package catalog

import (
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

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
	// GTIN is the optional identifiers.gtin; the zero value is absent.
	GTIN gs1.GTIN
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

// CatalogItemPackagingDeclared is raised when a pack level is declared (or its
// dimensions change in place). UnitsPerPack is in eaches; ChildGTIN is zero for EACH.
type CatalogItemPackagingDeclared struct {
	shared.Header
	CatalogItemID ID
	Level         PackLevel
	GTIN          gs1.GTIN
	ChildGTIN     gs1.GTIN
	UnitsPerPack  int
	DeclaredAt    time.Time
}

// EventName returns "CatalogItemPackagingDeclared".
func (CatalogItemPackagingDeclared) EventName() string { return "CatalogItemPackagingDeclared" }

// Entity returns "catalogitem".
func (CatalogItemPackagingDeclared) Entity() string { return "catalogitem" }

// SubjectID returns the catalogItemId.
func (e CatalogItemPackagingDeclared) SubjectID() string { return string(e.CatalogItemID) }

// CatalogItemPackLevelRetired is raised when a pack level is retired, by the
// retire command or because a new GTIN superseded it.
type CatalogItemPackLevelRetired struct {
	shared.Header
	CatalogItemID ID
	Level         PackLevel
	GTIN          gs1.GTIN
	RetiredAt     time.Time
}

// EventName returns "CatalogItemPackLevelRetired".
func (CatalogItemPackLevelRetired) EventName() string { return "CatalogItemPackLevelRetired" }

// Entity returns "catalogitem".
func (CatalogItemPackLevelRetired) Entity() string { return "catalogitem" }

// SubjectID returns the catalogItemId.
func (e CatalogItemPackLevelRetired) SubjectID() string { return string(e.CatalogItemID) }

var (
	_ shared.Event = CatalogItemPackagingDeclared{}
	_ shared.Event = CatalogItemPackLevelRetired{}
	_ shared.Event = CatalogItemCreated{}
	_ shared.Event = CatalogItemDiscontinued{}
)
