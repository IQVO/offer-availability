package repocontract

import (
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// Now is the fixed instant every builder uses.
var Now = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

// NewItem builds an ACTIVE catalogue item (version 1).
func NewItem(t testing.TB, id catalog.ID) *catalog.Item {
	t.Helper()
	it, _, err := catalog.Create(id, catalog.Spec{
		Title: "Item " + string(id), LengthMm: 270, WidthMm: 75, HeightMm: 75, WeightG: 340, Hazmat: true,
	}, Now)
	if err != nil {
		t.Fatalf("catalog.Create: %v", err)
	}
	return it
}

// NewListing builds a LIVE listing (version 1) that never had a feed.
func NewListing(t testing.TB, id listing.ID, item listing.ItemID, node listing.NodeID) *listing.Listing {
	t.Helper()
	l, _, err := listing.Create(id, item, node, listing.Sellability{NodeActive: true}, Now)
	if err != nil {
		t.Fatalf("listing.Create: %v", err)
	}
	return l
}

// Fed applies one availability to l (version 2).
func Fed(t testing.TB, l *listing.Listing, qty int, basis listing.Basis, asOf time.Time) *listing.Listing {
	t.Helper()
	if _, err := l.ApplyAvailability(qty, basis, asOf, "feed-0", Now); err != nil {
		t.Fatalf("ApplyAvailability: %v", err)
	}
	return l
}

// NewFeed builds a RECEIVED feed (version 1) of a partial submission with one line.
func NewFeed(t testing.TB, id feed.ID, node listing.NodeID, asOf time.Time) *feed.InventoryFeed {
	t.Helper()
	sub, err := feed.NewSubmission(id, node, false, asOf, []feed.Line{{ListingID: "lst-1", Qty: 1, Basis: listing.BasisPhysical}})
	if err != nil {
		t.Fatalf("feed.NewSubmission: %v", err)
	}
	return feed.Receive(sub, Now)
}

// NewNodeView builds a node status view (version 1).
func NewNodeView(t testing.TB, id nodestatus.ID, status nodestatus.Status) *nodestatus.View {
	t.Helper()
	v, err := nodestatus.New(id, status, Now)
	if err != nil {
		t.Fatalf("nodestatus.New: %v", err)
	}
	return v
}

// SomeEvents returns real domain events for the outbox cases.
func SomeEvents(t testing.TB) []shared.Event {
	t.Helper()
	_, events, err := catalog.Create("item-9", catalog.Spec{Title: "x", LengthMm: 1, WidthMm: 1, HeightMm: 1, WeightG: 1}, Now)
	if err != nil {
		t.Fatalf("catalog.Create: %v", err)
	}
	return events
}
