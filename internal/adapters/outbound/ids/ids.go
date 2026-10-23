// Package ids is the adapter of ports.IDGenerator: a kind prefix plus a UUID v4
// ("item-<uuid>", "lst-<uuid>"). Every id passes shared.ValidID (at most 64
// characters from A-Z a-z 0-9 . _ : -).
package ids

import (
	"github.com/google/uuid"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// UUID mints ids as "<kind>-<uuid v4>".
type UUID struct{}

var _ ports.IDGenerator = UUID{}

// NewCatalogItemID implements ports.IDGenerator.
func (UUID) NewCatalogItemID() catalog.ID { return catalog.ID("item-" + uuid.NewString()) }

// NewListingID implements ports.IDGenerator.
func (UUID) NewListingID() listing.ID { return listing.ID("lst-" + uuid.NewString()) }
