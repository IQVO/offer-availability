package repocontract

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
)

// ADR 0013: the SellerStatusView and the listings of one owner.

const (
	seller1 = "seller-1"
	seller2 = "seller-2"
)

var sellerStatusCases = []named{
	{caseRoundTrip, sellerRoundTrip},
	{caseGetMiss, sellerGetMiss},
	{caseDuplicateID, sellerDuplicateID},
	{caseVersionRace, sellerUpdateVersions},
}

var listingOwnerCases = []named{
	{"list by owner returns exactly the owner's listings by id", listingListByOwner},
	{"an OWNER_SUSPENDED listing round-trips", listingOwnerSuspendedRoundTrip},
}

func sellerRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	v := NewSellerView(t, seller1, sellerstatus.StatusSuspended)
	noErr(t, f.Sellers.Save(ctx, v))
	got, err := f.Sellers.Get(ctx, seller1)
	noErr(t, err)
	if !reflect.DeepEqual(got.Snapshot(), v.Snapshot()) {
		t.Fatalf("snapshot = %+v, want %+v", got.Snapshot(), v.Snapshot())
	}
}

func sellerGetMiss(t *testing.T, f Fixture) {
	_, err := f.Sellers.Get(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
}

func sellerDuplicateID(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Sellers.Save(ctx, NewSellerView(t, seller1, sellerstatus.StatusActive)))
	wantErr(t, f.Sellers.Save(ctx, NewSellerView(t, seller1, sellerstatus.StatusSuspended)), ports.ErrConcurrentUpdate)
}

func sellerUpdateVersions(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Sellers.Save(ctx, NewSellerView(t, seller1, sellerstatus.StatusActive)))
	a, err := f.Sellers.Get(ctx, seller1)
	noErr(t, err)
	b, err := f.Sellers.Get(ctx, seller1)
	noErr(t, err)
	_, err = a.Observe(sellerstatus.StatusSuspended, Now.Add(time.Hour))
	noErr(t, err)
	noErr(t, f.Sellers.Save(ctx, a))
	_, err = b.Observe(sellerstatus.StatusSuspended, Now.Add(2*time.Hour))
	noErr(t, err)
	wantErr(t, f.Sellers.Save(ctx, b), ports.ErrConcurrentUpdate)
	got, err := f.Sellers.Get(ctx, seller1)
	noErr(t, err)
	if got.Status() != sellerstatus.StatusSuspended || got.Version() != 2 || !got.AsOf().Equal(Now.Add(time.Hour)) {
		t.Fatalf("stored view = %+v", got.Snapshot())
	}
}

func listingListByOwner(t *testing.T, f Fixture) {
	ctx := context.Background()
	live := listing.Sellability{NodeActive: true}
	for _, l := range []*listing.Listing{
		NewOwnedListing(t, lst3, item1, node1, seller1, live),
		NewOwnedListing(t, lst1, item2, node2, seller1, live),
		NewOwnedListing(t, lst2, item1, node1, seller2, live),
		NewListing(t, lst4, item1, node1),
	} {
		noErr(t, f.Listings.Save(ctx, l))
	}
	got, err := f.Listings.ListByOwner(ctx, seller1)
	noErr(t, err)
	if ids := listingIDs(got); !reflect.DeepEqual(ids, []listing.ID{lst1, lst3}) {
		t.Fatalf("seller-1 listings = %v", ids)
	}
	none, err := f.Listings.ListByOwner(ctx, "seller-9")
	noErr(t, err)
	if len(none) != 0 {
		t.Fatalf("an owner without listings has none, got %v", listingIDs(none))
	}
}

func listingOwnerSuspendedRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	l := NewOwnedListing(t, lst1, item1, node1, seller1, listing.Sellability{NodeActive: true, OwnerSuspended: true})
	noErr(t, f.Listings.Save(ctx, l))
	got, err := f.Listings.Get(ctx, lst1)
	noErr(t, err)
	if !reflect.DeepEqual(got.Snapshot(), l.Snapshot()) || got.Reason() != listing.ReasonOwnerSuspended || got.OwnerRef() != seller1 {
		t.Fatalf("snapshot = %+v, want %+v", got.Snapshot(), l.Snapshot())
	}
	// The reason hands over to NODE_NOT_SELLABLE and back, persisted each time.
	l.Reconcile(listing.Sellability{OwnerSuspended: true}, Now)
	noErr(t, f.Listings.Save(ctx, l))
	l.Reconcile(listing.Sellability{NodeActive: true, OwnerSuspended: true}, Now)
	noErr(t, f.Listings.Save(ctx, l))
	got, err = f.Listings.Get(ctx, lst1)
	noErr(t, err)
	if got.Reason() != listing.ReasonOwnerSuspended || got.Version() != 3 {
		t.Fatalf("after two reconciles: %+v", got.Snapshot())
	}
}
