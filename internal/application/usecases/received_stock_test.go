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
