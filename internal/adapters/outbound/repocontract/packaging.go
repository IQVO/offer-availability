package repocontract

import (
	"context"
	"reflect"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// Valid 14-digit GTINs used by the packaging cases.
const (
	gtinEach  = "06141410000120"
	gtinInner = "06141410000298"
	gtinCase  = "06141410000366"
	gtinOther = "06141410000571"
)

var packagingCases = []named{
	{"pack levels and the item gtin round-trip, retired ones included", packRoundTrip},
	{"an update rewrites the pack levels under the version rule", packUpdate},
	{"find by gtin answers the item of an identifier or of a pack level", packFindByGTIN},
	{"a gtin held by another item is ErrGtinAlreadyAssigned and stores nothing", packGTINUnique},
	{"listings are unique per (item, node, owner) and round-trip their owner", listingOwner},
}

func mustGTIN(t testing.TB, s string) gs1.GTIN {
	t.Helper()
	g, err := catalog.ParseGTIN(s)
	if err != nil {
		t.Fatalf("ParseGTIN(%s): %v", s, err)
	}
	return g
}

// savePackaged stores an ACTIVE item (own gtin when gtin != "") at version 1,
// then declares and saves an EACH (version 2) and an INNER level (version 3).
func savePackaged(t *testing.T, f Fixture, id catalog.ID, gtin string) *catalog.Item {
	t.Helper()
	ctx := context.Background()
	spec := catalog.Spec{Title: "Item " + string(id), LengthMm: 10, WidthMm: 10, HeightMm: 10, WeightG: 10}
	if gtin != "" {
		spec.GTIN = mustGTIN(t, gtin)
	}
	it, _, err := catalog.Create(id, spec, Now)
	noErr(t, err)
	noErr(t, f.Catalog.Save(ctx, it))
	each := catalog.PackSpec{GTIN: mustGTIN(t, gtinEach), UnitsPerPack: 1, GrossWeightG: 10}
	inner := catalog.PackSpec{GTIN: mustGTIN(t, gtinInner), ChildGTIN: mustGTIN(t, gtinEach), UnitsPerPack: 6, LengthMm: 90, WidthMm: 60, HeightMm: 40, GrossWeightG: 70}
	for _, d := range []struct {
		level catalog.PackLevel
		spec  catalog.PackSpec
	}{{catalog.PackLevelEach, each}, {catalog.PackLevelInner, inner}} {
		_, err := it.DeclarePackLevel(d.level, d.spec, Now)
		noErr(t, err)
		noErr(t, f.Catalog.Save(ctx, it))
	}
	return it
}

func packRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	it := savePackaged(t, f, "item-1", gtinCase)
	if _, err := it.RetirePackLevel(catalog.PackLevelInner, Now); err != nil {
		t.Fatal(err)
	}
	noErr(t, f.Catalog.Save(ctx, it))
	got, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	if !reflect.DeepEqual(got.PackLevels(), it.PackLevels()) || got.Snapshot() != it.Snapshot() {
		t.Fatalf("round trip lost data:\n got  %+v %+v\n want %+v %+v", got.PackLevels(), got.Snapshot(), it.PackLevels(), it.Snapshot())
	}
	listed, err := f.Catalog.List(ctx, repository.Page{Limit: 10})
	noErr(t, err)
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].PackLevels(), it.PackLevels()) {
		t.Fatalf("list lost the pack levels: %+v", listed)
	}
}

func packUpdate(t *testing.T, f Fixture) {
	ctx := context.Background()
	savePackaged(t, f, "item-1", "")
	loaded, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	stale, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	if _, err := loaded.DeclarePackLevel(catalog.PackLevelCase, catalog.PackSpec{GTIN: mustGTIN(t, gtinCase), ChildGTIN: mustGTIN(t, gtinInner), UnitsPerPack: 24}, Now); err != nil {
		t.Fatal(err)
	}
	noErr(t, f.Catalog.Save(ctx, loaded))
	if _, err := stale.RetirePackLevel(catalog.PackLevelInner, Now); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.Catalog.Save(ctx, stale), ports.ErrConcurrentUpdate)
	got, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	if levels := got.PackLevels(); len(levels) != 3 || !levels[1].Active || levels[2].Level != catalog.PackLevelCase {
		t.Fatalf("stored levels = %+v", levels)
	}
	// supersede: one save retires and declares
	if _, err := got.DeclarePackLevel(catalog.PackLevelCase, catalog.PackSpec{GTIN: mustGTIN(t, gtinOther), ChildGTIN: mustGTIN(t, gtinInner), UnitsPerPack: 12}, Now); err != nil {
		t.Fatal(err)
	}
	noErr(t, f.Catalog.Save(ctx, got))
	again, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	if levels := again.PackLevels(); len(levels) != 4 || levels[2].Active || !levels[3].Active {
		t.Fatalf("after supersede = %+v", levels)
	}
}

func packFindByGTIN(t *testing.T, f Fixture) {
	ctx := context.Background()
	savePackaged(t, f, "item-1", gtinCase)
	for _, g := range []string{gtinCase, gtinEach, gtinInner} {
		id, err := f.Catalog.FindByGTIN(ctx, mustGTIN(t, g))
		noErr(t, err)
		if id != "item-1" {
			t.Fatalf("FindByGTIN(%s) = %s", g, id)
		}
	}
	_, err := f.Catalog.FindByGTIN(ctx, mustGTIN(t, gtinOther))
	wantErr(t, err, ports.ErrNotFound)
}

func packGTINUnique(t *testing.T, f Fixture) {
	ctx := context.Background()
	savePackaged(t, f, "item-1", gtinCase)
	// the same identifier on another item
	dup := NewItem(t, "item-2")
	other, _, err := catalog.Create("item-3", catalog.Spec{Title: "x", LengthMm: 1, WidthMm: 1, HeightMm: 1, WeightG: 1, GTIN: mustGTIN(t, gtinCase)}, Now)
	noErr(t, err)
	wantErr(t, f.Catalog.Save(ctx, other), catalog.ErrGtinAlreadyAssigned)
	if _, err := f.Catalog.Get(ctx, "item-3"); err == nil {
		t.Fatalf("the refused save stored the item")
	}
	// a pack level GTIN another item holds
	noErr(t, f.Catalog.Save(ctx, dup))
	if _, err := dup.DeclarePackLevel(catalog.PackLevelEach, catalog.PackSpec{GTIN: mustGTIN(t, gtinEach), UnitsPerPack: 1}, Now); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.Catalog.Save(ctx, dup), catalog.ErrGtinAlreadyAssigned)
	got, err := f.Catalog.Get(ctx, "item-2")
	noErr(t, err)
	if got.Version() != 1 || len(got.PackLevels()) != 0 {
		t.Fatalf("the refused save changed the item: v%d %+v", got.Version(), got.PackLevels())
	}
}

func listingOwner(t *testing.T, f Fixture) {
	ctx := context.Background()
	mk := func(id listing.ID, owner listing.OwnerRef) *listing.Listing {
		l, _, err := listing.CreateOwned(id, "item-1", "node-1", owner, listing.Sellability{NodeActive: true}, Now)
		noErr(t, err)
		return l
	}
	noErr(t, f.Listings.Save(ctx, mk("lst-1", "")))
	noErr(t, f.Listings.Save(ctx, mk("lst-2", "seller-7")))
	noErr(t, f.Listings.Save(ctx, mk("lst-3", "seller-8")))
	wantErr(t, f.Listings.Save(ctx, mk("lst-4", "seller-7")), listing.ErrDuplicateListing)
	wantErr(t, f.Listings.Save(ctx, mk("lst-5", "")), listing.ErrDuplicateListing)
	got, err := f.Listings.FindByItemNodeOwner(ctx, "item-1", "node-1", "seller-7")
	noErr(t, err)
	if got.ID() != "lst-2" || got.OwnerRef() != "seller-7" {
		t.Fatalf("seller-7 listing = %s %q", got.ID(), got.OwnerRef())
	}
	plain, err := f.Listings.FindByItemAndNode(ctx, "item-1", "node-1")
	noErr(t, err)
	if plain.ID() != "lst-1" || plain.OwnerRef() != "" {
		t.Fatalf("the operator-owned lookup answered %s %q", plain.ID(), plain.OwnerRef())
	}
	_, err = f.Listings.FindByItemNodeOwner(ctx, "item-1", "node-1", "seller-9")
	wantErr(t, err, ports.ErrNotFound)
	if got := listingIDs(mustList(t, f)); !reflect.DeepEqual(got, []listing.ID{"lst-1", "lst-2", "lst-3"}) {
		t.Fatalf("listings = %v", got)
	}
}

func mustList(t *testing.T, f Fixture) []*listing.Listing {
	t.Helper()
	ls, err := f.Listings.ListByItem(context.Background(), "item-1")
	noErr(t, err)
	return ls
}
