// Package views holds the read DTOs the use cases return: one per schema of
// apis/openapi.yaml (CatalogItem, Listing, InventoryFeed and their pages). The
// JSON tags are the REST wire names, so the HTTP adapter marshals them as they
// are. A view is a copy; nothing in it aliases an aggregate. Optional fields
// are omitted when absent (never null).
package views

import (
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// CatalogItem is the CatalogItem schema.
type CatalogItem struct {
	CatalogItemID string `json:"catalogItemId"`
	Title         string `json:"title"`
	LengthMm      int    `json:"lengthMm"`
	WidthMm       int    `json:"widthMm"`
	HeightMm      int    `json:"heightMm"`
	WeightG       int    `json:"weightG"`
	Hazmat        bool   `json:"hazmat"`
	Fragile       bool   `json:"fragile"`
	State         string `json:"state"`
	// Identifiers and Packaging are omitted when the item has none.
	Identifiers *Identifiers `json:"identifiers,omitempty"`
	Packaging   []PackLevel  `json:"packaging,omitempty"`
}

// Identifiers is the CatalogItemIdentifiers schema.
type Identifiers struct {
	GTIN string `json:"gtin,omitempty"`
}

// DimsMm is the PackDimensions schema.
type DimsMm struct {
	LengthMm int `json:"lengthMm"`
	WidthMm  int `json:"widthMm"`
	HeightMm int `json:"heightMm"`
}

// PackLevel is the PackLevel schema: one declared pack level. UnitsPerPack is
// in eaches.
type PackLevel struct {
	Level        string  `json:"level"`
	GTIN         string  `json:"gtin"`
	ChildGTIN    string  `json:"childGtin,omitempty"`
	UnitsPerPack int     `json:"unitsPerPack"`
	DimsMm       *DimsMm `json:"dimsMm,omitempty"`
	GrossWeightG int     `json:"grossWeightG,omitempty"`
	Active       bool    `json:"active"`
}

// CatalogItemFrom copies an item.
func CatalogItemFrom(i *catalog.Item) CatalogItem {
	s := i.Spec()
	v := CatalogItem{
		CatalogItemID: string(i.ID()), Title: s.Title, LengthMm: s.LengthMm, WidthMm: s.WidthMm,
		HeightMm: s.HeightMm, WeightG: s.WeightG, Hazmat: s.Hazmat, Fragile: s.Fragile, State: string(i.State()),
	}
	if !i.GTIN().IsZero() {
		v.Identifiers = &Identifiers{GTIN: i.GTIN().String()}
	}
	for _, d := range i.Packaging() {
		p := PackLevel{
			Level: string(d.Level), GTIN: d.GTIN.String(), ChildGTIN: d.ChildGTIN.String(),
			UnitsPerPack: d.UnitsPerPack, GrossWeightG: d.GrossWeightG, Active: d.Active,
		}
		if d.LengthMm > 0 {
			p.DimsMm = &DimsMm{LengthMm: d.LengthMm, WidthMm: d.WidthMm, HeightMm: d.HeightMm}
		}
		v.Packaging = append(v.Packaging, p)
	}
	return v
}

// CatalogPage is the CatalogPage schema.
type CatalogPage struct {
	Items      []CatalogItem `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// Availability is the Availability schema.
type Availability struct {
	AvailableQty int        `json:"availableQty"`
	Basis        string     `json:"basis"`
	AsOf         *time.Time `json:"asOf,omitempty"`
	FeedID       string     `json:"feedId,omitempty"`
}

// Listing is the Listing schema.
type Listing struct {
	ListingID         string       `json:"listingId"`
	CatalogItemID     string       `json:"catalogItemId"`
	NodeID            string       `json:"nodeId"`
	OwnerRef          string       `json:"ownerRef,omitempty"`
	State             string       `json:"state"`
	SuppressionReason string       `json:"suppressionReason,omitempty"`
	Availability      Availability `json:"availability"`
}

// ListingFrom copies a listing.
func ListingFrom(l *listing.Listing) Listing {
	a := l.Availability()
	av := Availability{AvailableQty: a.Qty, Basis: string(a.Basis), FeedID: a.FeedID}
	if !a.AsOf.IsZero() {
		asOf := a.AsOf.UTC()
		av.AsOf = &asOf
	}
	return Listing{
		ListingID: string(l.ID()), CatalogItemID: string(l.ItemID()), NodeID: string(l.NodeID()), OwnerRef: string(l.OwnerRef()),
		State: string(l.State()), SuppressionReason: string(l.Reason()), Availability: av,
	}
}

// ListingPage is the ListingPage schema.
type ListingPage struct {
	Listings   []Listing `json:"listings"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

// InventoryFeed is the InventoryFeed schema.
type InventoryFeed struct {
	FeedID       string     `json:"feedId"`
	NodeID       string     `json:"nodeId"`
	FullUpdate   bool       `json:"fullUpdate"`
	AsOf         time.Time  `json:"asOf"`
	State        string     `json:"state"`
	LineCount    int        `json:"lineCount"`
	AppliedCount int        `json:"appliedCount"`
	StaleCount   int        `json:"staleCount"`
	ZeroedCount  int        `json:"zeroedCount"`
	ReceivedAt   time.Time  `json:"receivedAt"`
	AppliedAt    *time.Time `json:"appliedAt,omitempty"`
	FailureCode  string     `json:"failureCode,omitempty"`
}

// InventoryFeedFrom copies a feed.
func InventoryFeedFrom(f *feed.InventoryFeed) InventoryFeed {
	s := f.Snapshot()
	v := InventoryFeed{
		FeedID: string(s.ID), NodeID: string(s.NodeID), FullUpdate: s.FullUpdate, AsOf: s.AsOf.UTC(),
		State: string(s.State), LineCount: s.LineCount, AppliedCount: s.AppliedCount, StaleCount: s.StaleCount,
		ZeroedCount: s.ZeroedCount, ReceivedAt: s.ReceivedAt.UTC(), FailureCode: s.FailureCode,
	}
	if !s.AppliedAt.IsZero() {
		at := s.AppliedAt.UTC()
		v.AppliedAt = &at
	}
	return v
}
