package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0014 (ADR 0013 rule 8): InboundShipmentReceived creates owner listings.

const typeShipmentReceived = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived"

// recordedSkips is a ports.ReceivedStockMetrics that keeps the reasons.
type recordedSkips struct {
	types   []string
	reasons []string
}

func (r *recordedSkips) ReceivedSkipped(_ context.Context, eventType, reason string) {
	r.types = append(r.types, eventType)
	r.reasons = append(r.reasons, reason)
}

func received(id string, seller listing.OwnerRef, node listing.NodeID, lines ...usecases.ReceivedLine) usecases.ReceivedStockEvent {
	return usecases.ReceivedStockEvent{
		EventID: id, EventType: typeShipmentReceived, ShipmentID: "shp-1", SellerID: seller, NodeID: node,
		ReceivedPresent: true, Received: lines,
	}
}

func byItem(item catalog.ID, qty int) usecases.ReceivedLine {
	return usecases.ReceivedLine{CatalogItemID: item, GTIN: uEach, QtyEaches: qty}
}

func byGTIN(gtin string, qty int) usecases.ReceivedLine {
	return usecases.ReceivedLine{GTIN: gtin, QtyEaches: qty}
}

func handleReceived(e *env, ev usecases.ReceivedStockEvent) (usecases.ReceivedStockResult, error) {
	return usecases.NewApplyReceivedStock(e.deps).Execute(context.Background(), ev)
}

// receivedEnv: node-1 ACTIVE, node-2 SUSPENDED; item-1 with the EACH < INNER <
// CASE chain, item-2 plain, item-3 discontinued; seller-2 SUSPENDED (every
// other seller is unknown, hence ACTIVE); lst-old is seller-1's listing of
// item-2 on node-1, fed with 5.
func receivedEnv(t *testing.T) (*env, *recordedSkips) {
	t.Helper()
	e := newEnv(t)
	e.seedNode("node-1", nodestatus.StatusActive)
	e.seedNode("node-2", nodestatus.StatusSuspended)
	for _, id := range []catalog.ID{"item-1", "item-2", "item-3"} {
		e.seedItem(id)
	}
	declareChain(t, e, "item-1")
	_, err := usecases.NewDiscontinueItem(e.deps).Execute(context.Background(), "item-3")
	mustNot(t, err)
	mustNot(t, e.store.Sellers().Save(context.Background(), repocontract.NewSellerView(t, "seller-2", sellerstatus.StatusSuspended)))
	l := repocontract.NewOwnedListing(t, "lst-old", "item-2", "node-1", "seller-1", listing.Sellability{NodeActive: true})
	mustNot(t, e.store.Listings().Save(context.Background(), l))
	mustNot(t, e.store.Listings().Save(context.Background(), repocontract.Fed(t, l, 5, listing.BasisPhysical, asOf1)))
	rec := &recordedSkips{}
	e.deps.ReceivedMetrics = rec
	return e, rec
}

// owned is the state one (item, node, owner) listing must show after a step.
type owned struct {
	item   listing.ItemID
	node   listing.NodeID
	owner  listing.OwnerRef
	state  listing.State
	reason listing.Reason
}

// receivedStep is one ordered event of the scenario: the events it must raise,
// the listings it must leave and the skip reasons it must count.
type receivedStep struct {
	name   string
	ev     usecases.ReceivedStockEvent
	events []string
	want   []owned
	skips  []string
}

func (e *env) ownedListing(item listing.ItemID, node listing.NodeID, owner listing.OwnerRef) *listing.Listing {
	e.t.Helper()
	l, err := e.store.Listings().FindByItemNodeOwner(context.Background(), item, node, owner)
	mustNot(e.t, err)
	return l
}

func runReceivedSteps(t *testing.T, e *env, rec *recordedSkips, steps []receivedStep) {
	t.Helper()
	for _, s := range steps {
		before, counted := len(e.events()), len(rec.reasons)
		if _, err := handleReceived(e, s.ev); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		var got []string
		for _, ev := range e.events()[before:] {
			got = append(got, ev.EventName())
		}
		if !equalNames(got, s.events) {
			t.Fatalf("%s: events %v, want %v", s.name, got, s.events)
		}
		if !equalNames(rec.reasons[counted:], s.skips) {
			t.Fatalf("%s: counted %v, want %v", s.name, rec.reasons[counted:], s.skips)
		}
		for _, w := range s.want {
			l := e.ownedListing(w.item, w.node, w.owner)
			if l.State() != w.state || l.Reason() != w.reason {
				t.Fatalf("%s: %s/%s/%s is %s %q, want %s %q", s.name, w.item, w.node, w.owner, l.State(), l.Reason(), w.state, w.reason)
			}
			if av := l.Availability(); av.Qty != 0 || av.Basis != listing.BasisUnknown || !av.AsOf.IsZero() {
				t.Fatalf("%s: a received listing starts at 0 UNKNOWN, got %+v", s.name, av)
			}
		}
	}
}

func TestReceivedStockScenario(t *testing.T) {
	e, rec := receivedEnv(t)
	created := []string{"ListingCreated"}
	createdHeld := []string{"ListingCreated", "ListingSuppressed"}
	unknown := received("evt-8", "seller-1", "node-1")
	unknown.ReceivedPresent = false
	// A slice, applied in order: the duplicate must come after its original.
	runReceivedSteps(t, e, rec, []receivedStep{
		{"catalogItemId present", received("evt-1", "seller-1", "node-1", byItem("item-1", 24)), created,
			[]owned{{"item-1", "node-1", "seller-1", listing.StateLive, ""}}, nil},
		{"GTIN-only case GTIN resolved", received("evt-2", "seller-3", "node-1", byGTIN(uCase, 48)), created,
			[]owned{{"item-1", "node-1", "seller-3", listing.StateLive, ""}}, nil},
		{"unresolvable GTIN skipped and counted", received("evt-3", "seller-4", "node-1", byGTIN(uSpare, 6)), nil,
			nil, []string{usecases.SkipGTINUnresolved}},
		{"existing listing no-op", received("evt-4", "seller-1", "node-1", byItem("item-2", 10)), nil, nil, nil},
		{"suspended owner", received("evt-5", "seller-2", "node-1", byItem("item-2", 3)), createdHeld,
			[]owned{{"item-2", "node-1", "seller-2", listing.StateSuppressed, listing.ReasonOwnerSuspended}}, nil},
		{"non-sellable node", received("evt-6", "seller-1", "node-2", byItem("item-1", 2)), createdHeld,
			[]owned{{"item-1", "node-2", "seller-1", listing.StateSuppressed, listing.ReasonNodeNotSellable}}, nil},
		{"discontinued item", received("evt-7", "seller-1", "node-1", byItem("item-3", 1)), createdHeld,
			[]owned{{"item-3", "node-1", "seller-1", listing.StateSuppressed, listing.ReasonDiscontinued}}, nil},
		{"received absent counted as unknown", unknown, nil, nil, []string{usecases.SkipReceivedUnknown}},
		{"duplicate event", received("evt-1", "seller-9", "node-1", byItem("item-2", 1), byGTIN(uSpare, 1)), nil, nil, nil},
	})
	if old := e.listing("lst-old"); old.Version() != 2 || old.Availability().Qty != 5 {
		t.Fatalf("the existing listing was touched: v%d %+v", old.Version(), old.Availability())
	}
	if _, err := e.store.Listings().FindByItemNodeOwner(context.Background(), "item-2", "node-1", "seller-9"); err == nil {
		t.Fatal("a redelivered id created a listing")
	}
	for _, typ := range rec.types {
		if typ != typeShipmentReceived {
			t.Fatalf("counted under type %q", typ)
		}
	}
	if got := e.store.Counts().Processed; got != 8 {
		t.Fatalf("inbox rows = %d, want 8 (every distinct id, the unknown one included)", got)
	}
}

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
