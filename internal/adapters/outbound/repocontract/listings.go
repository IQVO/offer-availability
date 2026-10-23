package repocontract

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

var listingCases = []named{
	{"save then get round-trips the snapshot with its availability", listingRoundTrip},
	{"get of an unknown id is ErrNotFound", listingGetMiss},
	{"find by item and node hits and misses", listingFind},
	{"a second listing for the same item and node is ErrDuplicateListing", listingDuplicate},
	{"creating an id that exists is ErrConcurrentUpdate", listingDuplicateID},
	{"an update expects the stored version to be version-1", listingUpdateVersions},
	{"get many returns the stored ones by id and ignores the rest", listingGetMany},
	{"list by node and by item return every listing by id", listingListByKey},
	{"list filters by node and item and pages by id", listingListFiltered},
	{"save many is all or none", listingSaveMany},
	{"save many of nothing succeeds", listingSaveManyEmpty},
}

func seedListings(t *testing.T, f Fixture, specs ...[3]string) {
	t.Helper()
	for _, s := range specs {
		noErr(t, f.Listings.Save(context.Background(), NewListing(t, listing.ID(s[0]), listing.ItemID(s[1]), listing.NodeID(s[2]))))
	}
}

func listingIDs(ls []*listing.Listing) []listing.ID {
	ids := []listing.ID{}
	for _, l := range ls {
		ids = append(ids, l.ID())
	}
	return ids
}

func listingRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	// Store the first version the way the service does, then one save per change.
	l := NewListing(t, "lst-1", "item-1", "node-1")
	noErr(t, f.Listings.Save(ctx, l))
	Fed(t, l, 12, listing.BasisThroughputConstrained, Now.Add(time.Hour))
	noErr(t, f.Listings.Save(ctx, l))
	if _, err := l.Suppress(listing.ReasonNodeNotSellable, Now); err != nil {
		t.Fatal(err)
	}
	noErr(t, f.Listings.Save(ctx, l))
	got, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	if !reflect.DeepEqual(got.Snapshot(), l.Snapshot()) {
		t.Fatalf("snapshot = %+v, want %+v", got.Snapshot(), l.Snapshot())
	}
}

func listingGetMiss(t *testing.T, f Fixture) {
	_, err := f.Listings.Get(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
}

func listingFind(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-1", "item-1", "node-1"})
	got, err := f.Listings.FindByItemAndNode(ctx, "item-1", "node-1")
	noErr(t, err)
	if got.ID() != "lst-1" {
		t.Fatalf("id = %s, want lst-1", got.ID())
	}
	for _, pair := range [][2]string{{"item-1", "node-2"}, {"item-2", "node-1"}} {
		_, err = f.Listings.FindByItemAndNode(ctx, listing.ItemID(pair[0]), listing.NodeID(pair[1]))
		wantErr(t, err, ports.ErrNotFound)
	}
}

func listingDuplicate(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-1", "item-1", "node-1"})
	wantErr(t, f.Listings.Save(ctx, NewListing(t, "lst-2", "item-1", "node-1")), listing.ErrDuplicateListing)
	if _, err := f.Listings.Get(ctx, "lst-2"); err == nil {
		t.Fatal("the rejected listing was stored")
	}
	noErr(t, f.Listings.Save(ctx, NewListing(t, "lst-3", "item-1", "node-2")))
	noErr(t, f.Listings.Save(ctx, NewListing(t, "lst-4", "item-2", "node-1")))
}

func listingDuplicateID(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-1", "item-1", "node-1"})
	wantErr(t, f.Listings.Save(ctx, NewListing(t, "lst-1", "item-2", "node-2")), ports.ErrConcurrentUpdate)
}

func listingUpdateVersions(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-1", "item-1", "node-1"})
	loaded, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	stale, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	noErr(t, f.Listings.Save(ctx, Fed(t, loaded, 5, listing.BasisPhysical, Now.Add(time.Hour))))
	wantErr(t, f.Listings.Save(ctx, Fed(t, stale, 6, listing.BasisPhysical, Now.Add(time.Hour))), ports.ErrConcurrentUpdate)
	got, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	if got.Availability().Qty != 5 || got.Version() != 2 {
		t.Fatalf("stored listing = qty %d v%d", got.Availability().Qty, got.Version())
	}
	ghost := Fed(t, NewListing(t, "lst-ghost", "item-9", "node-9"), 1, listing.BasisPhysical, Now.Add(time.Hour))
	wantErr(t, f.Listings.Save(ctx, ghost), ports.ErrConcurrentUpdate)
}

func listingGetMany(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-c", "item-1", "node-1"}, [3]string{"lst-a", "item-2", "node-1"}, [3]string{"lst-b", "item-3", "node-2"})
	got, err := f.Listings.GetMany(ctx, []listing.ID{"lst-c", "lst-zzz", "lst-a"})
	noErr(t, err)
	if want := []listing.ID{"lst-a", "lst-c"}; !reflect.DeepEqual(listingIDs(got), want) {
		t.Fatalf("ids = %v, want %v", listingIDs(got), want)
	}
	none, err := f.Listings.GetMany(ctx, nil)
	noErr(t, err)
	if none == nil || len(none) != 0 {
		t.Fatalf("get many of nothing = %#v, want a non-nil empty list", none)
	}
}

func listingListByKey(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-c", "item-1", "node-1"}, [3]string{"lst-a", "item-2", "node-1"}, [3]string{"lst-b", "item-1", "node-2"})
	byNode, err := f.Listings.ListByNode(ctx, "node-1")
	noErr(t, err)
	if want := []listing.ID{"lst-a", "lst-c"}; !reflect.DeepEqual(listingIDs(byNode), want) {
		t.Fatalf("by node = %v, want %v", listingIDs(byNode), want)
	}
	byItem, err := f.Listings.ListByItem(ctx, "item-1")
	noErr(t, err)
	if want := []listing.ID{"lst-b", "lst-c"}; !reflect.DeepEqual(listingIDs(byItem), want) {
		t.Fatalf("by item = %v, want %v", listingIDs(byItem), want)
	}
	none, err := f.Listings.ListByNode(ctx, "node-9")
	noErr(t, err)
	if none == nil || len(none) != 0 {
		t.Fatalf("by unknown node = %#v, want a non-nil empty list", none)
	}
}

func listingListFiltered(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-1", "item-1", "node-1"}, [3]string{"lst-2", "item-2", "node-1"},
		[3]string{"lst-3", "item-1", "node-2"}, [3]string{"lst-4", "item-3", "node-1"})
	list := func(fl repository.ListingFilter, p repository.Page) []listing.ID {
		rows, err := f.Listings.List(ctx, fl, p)
		noErr(t, err)
		return listingIDs(rows)
	}
	cases := []struct {
		name string
		fl   repository.ListingFilter
		p    repository.Page
		want []listing.ID
	}{
		{"no filter", repository.ListingFilter{}, repository.Page{Limit: 10}, []listing.ID{"lst-1", "lst-2", "lst-3", "lst-4"}},
		{"node", repository.ListingFilter{NodeID: "node-1"}, repository.Page{Limit: 10}, []listing.ID{"lst-1", "lst-2", "lst-4"}},
		{"item", repository.ListingFilter{ItemID: "item-1"}, repository.Page{Limit: 10}, []listing.ID{"lst-1", "lst-3"}},
		{"node and item", repository.ListingFilter{NodeID: "node-1", ItemID: "item-1"}, repository.Page{Limit: 10}, []listing.ID{"lst-1"}},
		{"no match", repository.ListingFilter{NodeID: "node-9"}, repository.Page{Limit: 10}, []listing.ID{}},
		{"first page", repository.ListingFilter{NodeID: "node-1"}, repository.Page{Limit: 2}, []listing.ID{"lst-1", "lst-2"}},
		{"next page", repository.ListingFilter{NodeID: "node-1"}, repository.Page{After: "lst-2", Limit: 2}, []listing.ID{"lst-4"}},
	}
	for _, c := range cases {
		if got := list(c.fl, c.p); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ids = %v, want %v", c.name, got, c.want)
		}
	}
}

func listingSaveMany(t *testing.T, f Fixture) {
	ctx := context.Background()
	seedListings(t, f, [3]string{"lst-1", "item-1", "node-1"}, [3]string{"lst-2", "item-2", "node-1"})
	a, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	b, err := f.Listings.Get(ctx, "lst-2")
	noErr(t, err)
	noErr(t, f.Listings.SaveMany(ctx, []*listing.Listing{
		Fed(t, a, 1, listing.BasisPhysical, Now.Add(time.Hour)), Fed(t, b, 2, listing.BasisPhysical, Now.Add(time.Hour)),
	}))
	a2, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	b2, err := f.Listings.Get(ctx, "lst-2")
	noErr(t, err)
	if a2.Availability().Qty != 1 || b2.Availability().Qty != 2 {
		t.Fatalf("quantities = %d, %d", a2.Availability().Qty, b2.Availability().Qty)
	}
	// The second entry is stale: the first must not be stored either.
	first, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	stale, err := f.Listings.Get(ctx, "lst-2")
	noErr(t, err)
	moved, err := f.Listings.Get(ctx, "lst-2")
	noErr(t, err)
	noErr(t, f.Listings.Save(ctx, Fed(t, moved, 7, listing.BasisPhysical, Now.Add(2*time.Hour))))
	wantErr(t, f.Listings.SaveMany(ctx, []*listing.Listing{
		Fed(t, first, 10, listing.BasisPhysical, Now.Add(2*time.Hour)), Fed(t, stale, 99, listing.BasisPhysical, Now.Add(3*time.Hour)),
	}), ports.ErrConcurrentUpdate)
	again, err := f.Listings.Get(ctx, "lst-1")
	noErr(t, err)
	if again.Availability().Qty != 1 {
		t.Fatalf("a failed SaveMany stored the first listing: qty %d", again.Availability().Qty)
	}
}

func listingSaveManyEmpty(t *testing.T, f Fixture) {
	noErr(t, f.Listings.SaveMany(context.Background(), nil))
}
