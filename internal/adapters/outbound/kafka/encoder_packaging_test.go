package kafka

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

func mustGTIN(t *testing.T, s string) gs1.GTIN {
	t.Helper()
	g, err := gs1.ParseGTIN(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// packagingGoldenCases are the packaging and ownerRef variants of the wire:
// the optional fields present, and absent where the optional field is omitted.
func packagingGoldenCases(t *testing.T) map[string]shared.Event {
	hdr := shared.Header{Version: 4, At: at}
	declared := time.Date(2026, 10, 24, 9, 0, 0, 123000000, time.UTC)
	return map[string]shared.Event{
		"CatalogItemPackagingDeclared": catalog.CatalogItemPackagingDeclared{
			Header: hdr, CatalogItemID: "item-004512", Level: catalog.PackLevelInner, GTIN: mustGTIN(t, "06141410000298"),
			ChildGTIN: mustGTIN(t, "06141410000120"), UnitsPerPack: 6, DeclaredAt: declared,
		},
		"CatalogItemPackagingDeclared.each": catalog.CatalogItemPackagingDeclared{
			Header: hdr, CatalogItemID: "item-004512", Level: catalog.PackLevelEach, GTIN: mustGTIN(t, "06141410000120"),
			UnitsPerPack: 1, DeclaredAt: declared,
		},
		"CatalogItemPackLevelRetired": catalog.CatalogItemPackLevelRetired{
			Header: hdr, CatalogItemID: "item-004512", Level: catalog.PackLevelCase, GTIN: mustGTIN(t, "06141410000366"), RetiredAt: at,
		},
		"CatalogItemCreated.gtin": catalog.CatalogItemCreated{
			Header: hdr, CatalogItemID: "item-004512", Title: "Stainless steel water bottle 750 ml", LengthMm: 270, WidthMm: 75,
			HeightMm: 75, WeightG: 340, Fragile: true, GTIN: mustGTIN(t, "6141410000120"),
		},
		"ListingCreated.owned": listing.ListingCreated{Header: hdr, ListingID: "lst-000185", CatalogItemID: "item-004512", NodeID: "node-0f3a91", OwnerRef: "seller-0042"},
		"ListingAvailabilityChanged.owned": listing.ListingAvailabilityChanged{
			Header: hdr, ListingID: "lst-000185", CatalogItemID: "item-004512", NodeID: "node-0f3a91", OwnerRef: "seller-0042",
			AvailableQty: 12, Basis: listing.BasisPhysical, AsOf: asOf, FeedID: "feed-20261023-0008",
		},
	}
}

func TestPackagingEventsMatchTheGoldenJSON(t *testing.T) {
	for name, ev := range packagingGoldenCases(t) {
		value, topic, key, typ, _, _ := encodeOne(t, ev)
		if topic != Topic || key != ev.SubjectID() {
			t.Errorf("%s: topic %q key %q", name, topic, key)
		}
		if want := "com.retailnetwork.commerce.offer-availability.catalogitem." + ev.EventName(); ev.Entity() == "catalogitem" && typ != want {
			t.Errorf("%s: type %q, want %q", name, typ, want)
		}
		path := filepath.Join("testdata", name+".golden.json")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, append(value, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := string(value) + "\n"; got != string(want) {
			t.Errorf("%s:\n got  %s\n want %s", name, got, want)
		}
	}
}

func TestAbsentOptionalFieldsAreOmittedNotNull(t *testing.T) {
	for _, ev := range []shared.Event{
		catalog.CatalogItemCreated{Header: shared.Header{Version: 1, At: at}, CatalogItemID: "item-1", Title: "x", LengthMm: 1, WidthMm: 1, HeightMm: 1, WeightG: 1},
		listing.ListingCreated{Header: shared.Header{Version: 1, At: at}, ListingID: "lst-1", CatalogItemID: "item-1", NodeID: "node-1"},
		catalog.CatalogItemPackagingDeclared{Header: shared.Header{Version: 2, At: at}, CatalogItemID: "item-1", Level: catalog.PackLevelEach, GTIN: mustGTIN(t, "06141410000120"), UnitsPerPack: 1, DeclaredAt: at},
	} {
		value, _, _, _, _, _ := encodeOne(t, ev)
		for _, field := range []string{`"gtin":"",`, `"ownerRef"`, `"childGtin"`, `null`} {
			if ev.EventName() == "CatalogItemPackagingDeclared" && field == `"gtin":"",` {
				continue
			}
			if strings.Contains(string(value), field) {
				t.Errorf("%s carries %s: %s", ev.EventName(), field, value)
			}
		}
	}
}
