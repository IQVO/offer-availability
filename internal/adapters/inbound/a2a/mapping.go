package a2a

import (
	"fmt"
	"time"

	"github.com/claudioed/offer-availability/internal/application/views"
)

// problem is the RFC 7807/9457-shaped rejection an agent client receives
// instead of a result: domain rejections and request-shape failures are
// business answers, not transport errors, so they travel in the message
// envelope. Type is the slug vocabulary of the REST adapter (not-found,
// validation-failed, internal-error) plus the adapter-owned request-shape
// slugs (malformed-skill-request, unknown-skill, invalid-argument).
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

func problemMalformedRequest(detail string) *problem {
	return &problem{Type: "malformed-skill-request", Title: "Malformed skill request", Detail: detail, Status: 400}
}

func problemUnknownSkill(skill string) *problem {
	return &problem{Type: "unknown-skill", Title: "Unknown skill", Detail: fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill), Status: 400}
}

func problemInvalidArgument(detail string) *problem {
	return &problem{Type: "invalid-argument", Title: "Invalid skill argument", Detail: detail, Status: 400}
}

// envelope is the structured payload of every skill answer: exactly one of
// Data (a skill DTO) or Problem (why there is no result), plus the skill id
// the answer belongs to (empty when the request named none).
type envelope struct {
	Skill   string    `json:"skill"`
	Data    *skillDTO `json:"data,omitempty"`
	Problem *problem  `json:"problem,omitempty"`
}

// skillDTO is the union of the skill result DTOs: each skill fills its own
// field, keeping the envelope flat and versioned as one contract. The
// summary is the human-readable one-liner of the reply's text part; it is
// unexported, so it is not on the wire.
type skillDTO struct {
	Availability *availabilityAnswer `json:"availability,omitempty"`
	FeedOutcome  *inventoryFeedView  `json:"feedOutcome,omitempty"`
	summary      string
}

// --- views (the REST/MCP bodies, camelCase like the REST contract) ------------

type identifiersView struct {
	GTIN string `json:"gtin,omitempty"`
}

type packDimsView struct {
	LengthMm int `json:"lengthMm"`
	WidthMm  int `json:"widthMm"`
	HeightMm int `json:"heightMm"`
}

type packLevelView struct {
	Level        string        `json:"level"`
	GTIN         string        `json:"gtin"`
	ChildGTIN    string        `json:"childGtin,omitempty"`
	UnitsPerPack int           `json:"unitsPerPack"`
	DimsMm       *packDimsView `json:"dimsMm,omitempty"`
	GrossWeightG int           `json:"grossWeightG,omitempty"`
	Active       bool          `json:"active"`
}

type catalogItemView struct {
	CatalogItemID string           `json:"catalogItemId"`
	Title         string           `json:"title"`
	LengthMm      int              `json:"lengthMm"`
	WidthMm       int              `json:"widthMm"`
	HeightMm      int              `json:"heightMm"`
	WeightG       int              `json:"weightG"`
	Hazmat        bool             `json:"hazmat"`
	Fragile       bool             `json:"fragile"`
	State         string           `json:"state"`
	Identifiers   *identifiersView `json:"identifiers,omitempty"`
	Packaging     []packLevelView  `json:"packaging,omitempty"`
}

type listingAvailabilityView struct {
	AvailableQty int    `json:"availableQty"`
	Basis        string `json:"basis"`
	AsOf         string `json:"asOf,omitempty"`
	FeedID       string `json:"feedId,omitempty"`
}

type listingView struct {
	ListingID         string                  `json:"listingId"`
	CatalogItemID     string                  `json:"catalogItemId"`
	NodeID            string                  `json:"nodeId"`
	OwnerRef          string                  `json:"ownerRef,omitempty"`
	State             string                  `json:"state"`
	SuppressionReason string                  `json:"suppressionReason,omitempty"`
	Availability      listingAvailabilityView `json:"availability"`
}

// availabilityAnswer is the composed read DTO of check_availability: the
// catalogue item when the caller named one, and one page of its listings.
type availabilityAnswer struct {
	Item       *catalogItemView `json:"item,omitempty"`
	Listings   []listingView    `json:"listings"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type inventoryFeedView struct {
	FeedID       string `json:"feedId"`
	NodeID       string `json:"nodeId"`
	FullUpdate   bool   `json:"fullUpdate"`
	AsOf         string `json:"asOf"`
	State        string `json:"state"`
	LineCount    int    `json:"lineCount"`
	AppliedCount int    `json:"appliedCount"`
	StaleCount   int    `json:"staleCount"`
	ZeroedCount  int    `json:"zeroedCount"`
	ReceivedAt   string `json:"receivedAt"`
	AppliedAt    string `json:"appliedAt,omitempty"`
	FailureCode  string `json:"failureCode,omitempty"`
}

// --- mapping ------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; nil and the zero time are ""
// (omitted).
func instant(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toCatalogItem(item views.CatalogItem) *catalogItemView {
	out := &catalogItemView{
		CatalogItemID: item.CatalogItemID, Title: item.Title, LengthMm: item.LengthMm, WidthMm: item.WidthMm,
		HeightMm: item.HeightMm, WeightG: item.WeightG, Hazmat: item.Hazmat, Fragile: item.Fragile, State: item.State,
	}
	if item.Identifiers != nil {
		out.Identifiers = &identifiersView{GTIN: item.Identifiers.GTIN}
	}
	out.Packaging = make([]packLevelView, 0, len(item.Packaging))
	for _, p := range item.Packaging {
		level := packLevelView{
			Level: p.Level, GTIN: p.GTIN, ChildGTIN: p.ChildGTIN, UnitsPerPack: p.UnitsPerPack,
			GrossWeightG: p.GrossWeightG, Active: p.Active,
		}
		if p.DimsMm != nil {
			level.DimsMm = &packDimsView{LengthMm: p.DimsMm.LengthMm, WidthMm: p.DimsMm.WidthMm, HeightMm: p.DimsMm.HeightMm}
		}
		out.Packaging = append(out.Packaging, level)
	}
	return out
}

func toListing(l views.Listing) listingView {
	return listingView{
		ListingID: l.ListingID, CatalogItemID: l.CatalogItemID, NodeID: l.NodeID, OwnerRef: l.OwnerRef,
		State: l.State, SuppressionReason: l.SuppressionReason,
		Availability: listingAvailabilityView{
			AvailableQty: l.Availability.AvailableQty, Basis: l.Availability.Basis,
			AsOf: instant(l.Availability.AsOf), FeedID: l.Availability.FeedID,
		},
	}
}

// toAvailability composes the check_availability answer and its one-line
// summary from the optional item and one listing page.
func toAvailability(item *views.CatalogItem, page views.ListingPage) skillDTO {
	answer := &availabilityAnswer{Listings: make([]listingView, 0, len(page.Listings)), NextCursor: page.NextCursor}
	if item != nil {
		answer.Item = toCatalogItem(*item)
	}
	live, suppressed, qty := 0, 0, 0
	for _, l := range page.Listings {
		answer.Listings = append(answer.Listings, toListing(l))
		if l.State == "LIVE" {
			live++
			qty += l.Availability.AvailableQty
		} else {
			suppressed++
		}
	}
	summary := fmt.Sprintf("%d listings (%d LIVE, %d SUPPRESSED), %d units reported on the LIVE ones", len(page.Listings), live, suppressed, qty)
	if item != nil {
		summary = fmt.Sprintf("%s (%s): %s", item.CatalogItemID, item.State, summary)
	}
	if page.NextCursor != "" {
		summary += "; more on the next page"
	}
	return skillDTO{Availability: answer, summary: summary}
}

// toFeedOutcome maps the feed read model and writes its one-line summary.
func toFeedOutcome(f views.InventoryFeed) skillDTO {
	out := &inventoryFeedView{
		FeedID: f.FeedID, NodeID: f.NodeID, FullUpdate: f.FullUpdate, AsOf: instant(&f.AsOf), State: f.State,
		LineCount: f.LineCount, AppliedCount: f.AppliedCount, StaleCount: f.StaleCount, ZeroedCount: f.ZeroedCount,
		ReceivedAt: instant(&f.ReceivedAt), AppliedAt: instant(f.AppliedAt), FailureCode: f.FailureCode,
	}
	summary := fmt.Sprintf("%s on %s: %s, %d lines (%d applied, %d stale, %d zeroed)", out.FeedID, out.NodeID, out.State, out.LineCount, out.AppliedCount, out.StaleCount, out.ZeroedCount)
	if out.FailureCode != "" {
		summary += "; failure: " + out.FailureCode
	}
	return skillDTO{FeedOutcome: out, summary: summary}
}
