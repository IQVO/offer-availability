package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ApplySellerEvent: inbox, ordering and validation (ADR 0013, rules 1 and 2).

func TestSellerDuplicateEventIDIsSkipped(t *testing.T) {
	e := sellerEnv(t)
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-1", sellerstatus.StatusSuspended, asOf1)))
	before := e.store.Counts()
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-1", sellerstatus.StatusActive, asOf2)))
	e.assertNothingStored(before)
	if !sellerView(t, e, "seller-1").Suspended() {
		t.Fatal("a redelivered id must change nothing")
	}
}

func TestSellerOlderInstantIsIgnored(t *testing.T) {
	e := sellerEnv(t)
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-1", sellerstatus.StatusSuspended, asOf2)))
	before := e.store.Counts()
	mustNot(t, handleSeller(e, sellerEvent("evt-2", "seller-1", sellerstatus.StatusActive, asOf1)))
	after := e.store.Counts()
	if after.Processed != before.Processed+1 || after.OutboxEvents != before.OutboxEvents || after.VersionSum != before.VersionSum {
		t.Fatalf("an old event only lands in the inbox: before %+v after %+v", before, after)
	}
	if v := sellerView(t, e, "seller-1"); !v.Suspended() || !v.AsOf().Equal(asOf2) {
		t.Fatalf("the view must keep the newer status: %+v", v.Snapshot())
	}
	if e.listing("lst-s").Reason() != listing.ReasonOwnerSuspended {
		t.Fatal("an ignored reinstatement must not restore the listing")
	}
}

func TestSellerUnknownReinstatedIsCreatedActive(t *testing.T) {
	e := sellerEnv(t)
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-9", sellerstatus.StatusActive, asOf1)))
	if v := sellerView(t, e, "seller-9"); v.Status() != sellerstatus.StatusActive || v.Version() != 1 || !v.AsOf().Equal(asOf1) {
		t.Fatalf("view = %+v", v.Snapshot())
	}
	e.assertEvents()
}

func TestSellerRepeatedSuspensionHealsALiveListing(t *testing.T) {
	e := sellerEnv(t)
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-1", sellerstatus.StatusSuspended, asOf1)))
	e.seedOwned("lst-late", "item-2", "node-1", "seller-1", listing.Sellability{NodeActive: true})
	mustNot(t, handleSeller(e, sellerEvent("evt-2", "seller-1", sellerstatus.StatusSuspended, asOf2)))
	if l := e.listing("lst-late"); l.Reason() != listing.ReasonOwnerSuspended {
		t.Fatalf("the repeated event must suppress the LIVE listing: %s %s", l.State(), l.Reason())
	}
	if v := sellerView(t, e, "seller-1"); v.Version() != 1 || !v.AsOf().Equal(asOf1) {
		t.Fatalf("a repeated status does not save the view again: %+v", v.Snapshot())
	}
	e.assertEvents(evSuppressed, evSuppressed, evSuppressed)
}

func TestSellerOperatorListingsAreNeverTouched(t *testing.T) {
	e := sellerEnv(t)
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-2", sellerstatus.StatusSuspended, asOf1)))
	e.assertEvents(evSuppressed)
	if e.listing("lst-o").State() != listing.StateLive || e.listing("lst-s").State() != listing.StateLive {
		t.Fatal("only seller-2's listing may be suppressed")
	}
	if ev := e.events()[0].(listing.ListingSuppressed); ev.ListingID != "lst-t" || ev.Reason != listing.ReasonOwnerSuspended {
		t.Fatalf("event = %+v", ev)
	}
}

func TestSellerMalformedEventIsAValidationError(t *testing.T) {
	cases := []usecases.SellerEvent{
		{EventType: typeSellerSuspended, SellerID: "seller-1", Status: sellerstatus.StatusSuspended, At: asOf1},
		{EventID: "evt-1", SellerID: "seller-1", Status: sellerstatus.StatusSuspended, At: asOf1},
		{EventID: "evt-2", EventType: typeSellerSuspended, SellerID: "seller 1", Status: sellerstatus.StatusSuspended, At: asOf1},
		{EventID: "evt-3", EventType: typeSellerSuspended, SellerID: "seller-1", Status: sellerstatus.StatusSuspended},
		{EventID: "evt-4", EventType: typeSellerSuspended, SellerID: "seller-1", Status: "GONE", At: asOf1},
	}
	for _, ev := range cases {
		e := sellerEnv(t)
		before := e.store.Counts()
		wantIs(t, handleSeller(e, ev), shared.ErrValidation)
		e.assertNothingStored(before)
	}
}

func TestSellerKnownViewRejectsAMalformedObservation(t *testing.T) {
	e := sellerEnv(t)
	mustNot(t, handleSeller(e, sellerEvent("evt-1", "seller-1", sellerstatus.StatusSuspended, asOf1)))
	before := e.store.Counts()
	wantIs(t, handleSeller(e, usecases.SellerEvent{EventID: "evt-2", EventType: typeSellerReinstated, SellerID: "seller-1", Status: sellerstatus.StatusActive}),
		shared.ErrValidation)
	e.assertNothingStored(before)
}

func TestSellerFailureRollsEverythingBack(t *testing.T) {
	e := sellerEnv(t)
	e.listings.saveManyErrs = []error{errBoom, errBoom}
	before := e.store.Counts()
	wantIs(t, handleSeller(e, sellerEvent("evt-1", "seller-1", sellerstatus.StatusSuspended, asOf1)), errBoom)
	e.assertNothingStored(before)
	if _, err := e.store.Sellers().Get(context.Background(), "seller-1"); err == nil {
		t.Fatal("the view must be rolled back with the listings")
	}
}
