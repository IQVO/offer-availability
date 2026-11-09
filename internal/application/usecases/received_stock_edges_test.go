package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ApplyReceivedStock: result, resolution, skips, refusals and failures (ADR 0014).

func TestReceivedEventDataCarriesTheOwner(t *testing.T) {
	e, _ := receivedEnv(t)
	before := len(e.events())
	res, err := handleReceived(e, received("evt-1", "seller-1", "node-1", byItem("item-1", 24), byItem("item-2", 1), byGTIN(uSpare, 6)))
	mustNot(t, err)
	if len(res.Created) != 1 || res.Existing != 1 || len(res.Skipped) != 1 || res.Duplicate || res.Unknown {
		t.Fatalf("result = %+v", res)
	}
	if s := res.Skipped[0]; s.GTIN != uSpare || s.Reason != usecases.SkipGTINUnresolved || s.CatalogItemID != "" {
		t.Fatalf("skipped = %+v", s)
	}
	ev, ok := e.events()[before].(listing.ListingCreated)
	if !ok || ev.ListingID != res.Created[0] || ev.CatalogItemID != "item-1" || ev.NodeID != "node-1" || ev.OwnerRef != "seller-1" || ev.Version != 1 {
		t.Fatalf("ListingCreated = %+v", e.events()[before])
	}
}

func TestReceivedInnerAndRetiredGTINsResolve(t *testing.T) {
	e, rec := receivedEnv(t)
	_, err := usecases.NewRetirePackLevel(e.deps).Execute(context.Background(), "item-1", "CASE")
	mustNot(t, err)
	_, err = handleReceived(e, received("evt-1", "seller-a", "node-1", byGTIN(uInner, 6)))
	mustNot(t, err)
	_, err = handleReceived(e, received("evt-2", "seller-b", "node-1", byGTIN(uCase, 24)))
	mustNot(t, err)
	e.ownedListing("item-1", "node-1", "seller-a")
	e.ownedListing("item-1", "node-1", "seller-b")
	if len(rec.reasons) != 0 {
		t.Fatalf("an inner and a retired case GTIN must resolve, counted %v", rec.reasons)
	}
}

func TestReceivedLinesOfOneItemMakeOneListing(t *testing.T) {
	e, _ := receivedEnv(t)
	before := len(e.events())
	res, err := handleReceived(e, received("evt-1", "seller-1", "node-1", byGTIN(uEach, 2), byGTIN(uCase, 24), byItem("item-1", 1)))
	mustNot(t, err)
	if len(res.Created) != 1 || res.Existing != 0 || len(e.events()) != before+1 {
		t.Fatalf("one item, one listing: %+v, %d event(s)", res, len(e.events())-before)
	}
}

func TestReceivedSkipsWithoutError(t *testing.T) {
	e, rec := receivedEnv(t)
	before := len(e.events())
	res, err := handleReceived(e, received("evt-1", "seller-1", "node-1",
		byItem("item-1", 0),         // zero eaches: silently skipped, not counted
		byItem("item-2", -3),        // negative: same
		byGTIN("abc", 1),            // not a GTIN
		byGTIN("06141410000121", 1), // bad check digit
		byItem("item-9", 4),         // an id the catalogue does not hold
		byItem("not an id!", 4),     // not even an id
	))
	mustNot(t, err)
	want := []string{usecases.SkipGTINUnresolved, usecases.SkipGTINUnresolved, usecases.SkipItemUnknown, usecases.SkipItemUnknown}
	if !equalNames(rec.reasons, want) || len(res.Skipped) != 4 || len(res.Created) != 0 || len(e.events()) != before {
		t.Fatalf("counted %v, result %+v", rec.reasons, res)
	}
	if res.Skipped[2].CatalogItemID != "item-9" {
		t.Fatalf("the skipped line keeps its id: %+v", res.Skipped[2])
	}
}

func TestReceivedWithoutMetricsCountsNothing(t *testing.T) {
	e, _ := receivedEnv(t)
	e.deps.ReceivedMetrics = nil
	ev := received("evt-1", "seller-1", "node-1", byGTIN(uSpare, 1))
	ev.ReceivedPresent = false
	res, err := handleReceived(e, ev)
	mustNot(t, err)
	if !res.Unknown {
		t.Fatalf("result = %+v", res)
	}
	res, err = handleReceived(e, ev)
	mustNot(t, err)
	if !res.Duplicate || res.Unknown {
		t.Fatalf("redelivered = %+v", res)
	}
}

func TestReceivedRefusesAMalformedEvent(t *testing.T) {
	e, rec := receivedEnv(t)
	good := received("evt-1", "seller-1", "node-1", byItem("item-1", 1))
	bad := []func(*usecases.ReceivedStockEvent){
		func(ev *usecases.ReceivedStockEvent) { ev.EventID = "" },
		func(ev *usecases.ReceivedStockEvent) { ev.EventType = "" },
		func(ev *usecases.ReceivedStockEvent) { ev.SellerID = "" },
		func(ev *usecases.ReceivedStockEvent) { ev.SellerID = "not an id!" },
		func(ev *usecases.ReceivedStockEvent) { ev.NodeID = "" },
	}
	before := e.store.Counts()
	for i, mutate := range bad {
		ev := good
		mutate(&ev)
		_, err := handleReceived(e, ev)
		if err == nil {
			t.Fatalf("case %d: accepted %+v", i, ev)
		}
		wantIs(t, err, shared.ErrValidation)
	}
	e.assertNothingStored(before)
	if len(rec.reasons) != 0 {
		t.Fatalf("a refused event counted %v", rec.reasons)
	}
}

func TestReceivedFailureStoresAndCountsNothing(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*env)
	}{
		{"outbox", func(e *env) { e.outbox.err = errBoom }},
		{"inbox", func(e *env) { e.inbox.err = errBoom }},
		{"node view", func(e *env) { e.nodes.getErr = errBoom }},
		{"listing lookup", func(e *env) { e.listings.findErr = errBoom }},
		{"listing save", func(e *env) { e.listings.saveErrs = []error{errBoom} }},
		{"gtin index", func(e *env) { e.catalog.findErr = errBoom }},
		{"catalogue", func(e *env) { e.catalog.getErr = errBoom }},
	}
	for _, c := range cases {
		e, rec := receivedEnv(t)
		c.inject(e)
		before := e.store.Counts()
		_, err := handleReceived(e, received("evt-1", "seller-1", "node-1", byGTIN(uSpare, 1), byGTIN(uCase, 2), byItem("item-2", 1), byItem("item-1", 1)))
		wantIs(t, err, errBoom)
		e.assertNothingStored(before)
		if len(rec.reasons) != 0 {
			t.Fatalf("%s: a rolled-back event counted %v", c.name, rec.reasons)
		}
	}
}

func TestReceivedRetriesAListingCreatedMeanwhile(t *testing.T) {
	e, _ := receivedEnv(t)
	// The first save trips the unique (item, node, owner) index, as when POST
	// /listings created the triple between the lookup and the save.
	e.listings.saveErrs = []error{listing.ErrDuplicateListing}
	res, err := handleReceived(e, received("evt-1", "seller-1", "node-1", byItem("item-1", 1)))
	mustNot(t, err)
	if len(res.Created) != 1 || e.listings.saves != 2 || e.store.Counts().Processed != 1 {
		t.Fatalf("result %+v after %d save(s)", res, e.listings.saves)
	}
}
