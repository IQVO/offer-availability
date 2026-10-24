package http

import (
	"time"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// createCatalogItemBody is the CreateCatalogItemRequest schema.
type createCatalogItemBody struct {
	Title    string `json:"title"`
	LengthMm int    `json:"lengthMm"`
	WidthMm  int    `json:"widthMm"`
	HeightMm int    `json:"heightMm"`
	WeightG  int    `json:"weightG"`
	Hazmat   bool   `json:"hazmat"`
	Fragile  bool   `json:"fragile"`
	// Identifiers is optional; identifiers.gtin is validated by the domain.
	Identifiers *struct {
		GTIN string `json:"gtin"`
	} `json:"identifiers"`
}

func (b createCatalogItemBody) command() usecases.CreateCatalogItemCommand {
	cmd := usecases.CreateCatalogItemCommand{
		Title: b.Title, LengthMm: b.LengthMm, WidthMm: b.WidthMm, HeightMm: b.HeightMm,
		WeightG: b.WeightG, Hazmat: b.Hazmat, Fragile: b.Fragile,
	}
	if b.Identifiers != nil {
		cmd.GTIN = b.Identifiers.GTIN
	}
	return cmd
}

// declarePackLevelBody is the DeclarePackLevelRequest schema (PUT body; the
// level is in the path).
type declarePackLevelBody struct {
	GTIN         string `json:"gtin"`
	ChildGTIN    string `json:"childGtin"`
	UnitsPerPack int    `json:"unitsPerPack"`
	DimsMm       *struct {
		LengthMm int `json:"lengthMm"`
		WidthMm  int `json:"widthMm"`
		HeightMm int `json:"heightMm"`
	} `json:"dimsMm"`
	GrossWeightG int `json:"grossWeightG"`
}

func (b declarePackLevelBody) command(id catalog.ID, level string) usecases.DeclarePackLevelCommand {
	cmd := usecases.DeclarePackLevelCommand{
		CatalogItemID: id, Level: level, GTIN: b.GTIN, ChildGTIN: b.ChildGTIN,
		UnitsPerPack: b.UnitsPerPack, GrossWeightG: b.GrossWeightG,
	}
	if b.DimsMm != nil {
		cmd.LengthMm, cmd.WidthMm, cmd.HeightMm = b.DimsMm.LengthMm, b.DimsMm.WidthMm, b.DimsMm.HeightMm
	}
	return cmd
}

// createListingBody is the CreateListingRequest schema.
type createListingBody struct {
	CatalogItemID string `json:"catalogItemId"`
	NodeID        string `json:"nodeId"`
	OwnerRef      string `json:"ownerRef"`
}

func (b createListingBody) command() usecases.CreateListingCommand {
	return usecases.CreateListingCommand{
		CatalogItemID: catalog.ID(b.CatalogItemID), NodeID: listing.NodeID(b.NodeID), OwnerRef: listing.OwnerRef(b.OwnerRef),
	}
}

// feedLineBody is the FeedLine schema.
type feedLineBody struct {
	ListingID    string `json:"listingId"`
	AvailableQty int    `json:"availableQty"`
	Basis        string `json:"basis"`
}

// applyInventoryFeedBody is the ApplyInventoryFeedRequest schema. The pointers
// tell a missing required field (validation-failed) from its zero value.
type applyInventoryFeedBody struct {
	FeedID     string          `json:"feedId"`
	NodeID     string          `json:"nodeId"`
	FullUpdate *bool           `json:"fullUpdate"`
	AsOf       time.Time       `json:"asOf"`
	Lines      *[]feedLineBody `json:"lines"`
}

func (b applyInventoryFeedBody) command() usecases.ApplyInventoryFeedCommand {
	lines := []feed.Line{}
	if b.Lines != nil {
		for _, l := range *b.Lines {
			lines = append(lines, feed.Line{ListingID: listing.ID(l.ListingID), Qty: l.AvailableQty, Basis: listing.Basis(l.Basis)})
		}
	}
	return usecases.ApplyInventoryFeedCommand{
		FeedID: feed.ID(b.FeedID), NodeID: listing.NodeID(b.NodeID), FullUpdate: b.FullUpdate != nil && *b.FullUpdate,
		AsOf: b.AsOf, Lines: lines,
	}
}
