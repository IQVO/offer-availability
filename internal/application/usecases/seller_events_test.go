package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0013: SellerSuspended / SellerReinstated and the precedence
// DISCONTINUED > NODE_NOT_SELLABLE > OWNER_SUSPENDED.

const (
	typeSellerSuspended  = "com.retailnetwork.partner.seller-inbound.seller.SellerSuspended"
	typeSellerReinstated = "com.retailnetwork.partner.seller-inbound.seller.SellerReinstated"
)

func sellerEvent(id string, seller sellerstatus.ID, status sellerstatus.Status, at time.Time) usecases.SellerEvent {
	typ := typeSellerReinstated
	if status == sellerstatus.StatusSuspended {
		typ = typeSellerSuspended
	}
	return usecases.SellerEvent{EventID: id, EventType: typ, SellerID: seller, Status: status, At: at}
}

func handleSeller(e *env, ev usecases.SellerEvent) error {
	return usecases.NewApplySellerEvent(e.deps).Execute(context.Background(), ev)
}

// seedOwned stores a listing of owner built from s (LIVE when s holds no cause).
func (e *env) seedOwned(id listing.ID, item listing.ItemID, node listing.NodeID, owner listing.OwnerRef, s listing.Sellability) {
	e.t.Helper()
	mustNot(e.t, e.store.Listings().Save(context.Background(), repocontract.NewOwnedListing(e.t, id, item, node, owner, s)))
}

func sellerView(t *testing.T, e *env, id sellerstatus.ID) *sellerstatus.View {
	t.Helper()
	v, err := e.store.Sellers().Get(context.Background(), id)
	mustNot(t, err)
	return v
}

// sellerEnv: node-1 ACTIVE; LIVE listings lst-o (operator), lst-s (seller-1),
// lst-t (seller-2), all on node-1; lst-z (seller-1) on node-2, which is ACTIVE.
func sellerEnv(t *testing.T) *env {
	e := newEnv(t)
	e.seedNode("node-1", nodestatus.StatusActive)
	e.seedNode("node-2", nodestatus.StatusActive)
	e.seedItem("item-1")
	e.seedItem("item-2")
	live := listing.Sellability{NodeActive: true}
	e.seedOwned("lst-o", "item-2", "node-1", "", live)
	e.seedOwned("lst-s", "item-1", "node-1", "seller-1", live)
	e.seedOwned("lst-t", "item-1", "node-1", "seller-2", live)
	e.seedOwned("lst-z", "item-2", "node-2", "seller-1", live)
	return e
}

// expect is the state and reason one listing must show after a step.
type expect struct {
	id     listing.ID
	state  listing.State
	reason listing.Reason
}

// step is one ordered action of a precedence scenario: the events it must
// raise (in order) and the listings it must leave behind.
type step struct {
	name   string
	do     func(e *env) error
	events []string
	want   []expect
}

func live(id listing.ID) expect { return expect{id, listing.StateLive, ""} }

func held(id listing.ID, r listing.Reason) expect { return expect{id, listing.StateSuppressed, r} }

func suspendOwner(id string, seller sellerstatus.ID, at time.Time) func(*env) error {
	return func(e *env) error { return handleSeller(e, sellerEvent(id, seller, sellerstatus.StatusSuspended, at)) }
}

func reinstateOwner(id string, seller sellerstatus.ID, at time.Time) func(*env) error {
	return func(e *env) error { return handleSeller(e, sellerEvent(id, seller, sellerstatus.StatusActive, at)) }
}

func nodeTo(id string, node nodestatus.ID, status nodestatus.Status, at time.Time) func(*env) error {
	return func(e *env) error {
		ev := nodeEvent(id, status, at)
		ev.NodeID = node
		return handle(e, ev)
	}
}

// runSteps applies the steps IN ORDER (a slice, never a map: a terminal step
// applied first would make a later one legitimately refused).
func runSteps(t *testing.T, e *env, steps []step) {
	t.Helper()
	for _, s := range steps {
		before := len(e.events())
		if err := s.do(e); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		var got []string
		for _, ev := range e.events()[before:] {
			got = append(got, ev.EventName())
		}
		if !equalNames(got, s.events) {
			t.Fatalf("%s: events %v, want %v", s.name, got, s.events)
		}
		for _, w := range s.want {
			if l := e.listing(w.id); l.State() != w.state || l.Reason() != w.reason {
				t.Fatalf("%s: %s is %s %q, want %s %q", s.name, w.id, l.State(), l.Reason(), w.state, w.reason)
			}
		}
	}
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	evSuppressed = "ListingSuppressed"
	evRestored   = "ListingRestored"
)

func TestPrecedenceOwnerAndNode(t *testing.T) {
	e := sellerEnv(t)
	t1, t2, t3, t4 := asOf1, asOf2, asOf3, asOf3.Add(time.Hour)
	runSteps(t, e, []step{
		{"suspend owner", suspendOwner("e1", "seller-1", t1), []string{evSuppressed, evSuppressed},
			[]expect{live("lst-o"), held("lst-s", listing.ReasonOwnerSuspended), live("lst-t"), held("lst-z", listing.ReasonOwnerSuspended)}},
		{"suspend node: the higher cause takes over", nodeTo("e2", "node-1", nodestatus.StatusSuspended, t1), []string{evSuppressed, evSuppressed, evSuppressed},
			[]expect{held("lst-o", listing.ReasonNodeNotSellable), held("lst-s", listing.ReasonNodeNotSellable), held("lst-t", listing.ReasonNodeNotSellable)}},
		{"reinstate owner: the node still holds", reinstateOwner("e3", "seller-1", t2), []string{evRestored},
			[]expect{held("lst-s", listing.ReasonNodeNotSellable), live("lst-z")}},
		{"suspend owner again: node keeps its reason", suspendOwner("e4", "seller-1", t3), []string{evSuppressed},
			[]expect{held("lst-s", listing.ReasonNodeNotSellable), held("lst-z", listing.ReasonOwnerSuspended)}},
		{"activate node: the owner still holds", nodeTo("e5", "node-1", nodestatus.StatusActive, t2), []string{evRestored, evSuppressed, evRestored},
			[]expect{live("lst-o"), held("lst-s", listing.ReasonOwnerSuspended), live("lst-t")}},
		{"reinstate owner: nothing else holds", reinstateOwner("e6", "seller-1", t4), []string{evRestored, evRestored},
			[]expect{live("lst-o"), live("lst-s"), live("lst-t"), live("lst-z")}},
	})
}

func TestPrecedenceDiscontinuedIsFinal(t *testing.T) {
	e := sellerEnv(t)
	discontinue := func(e *env) error {
		_, err := usecases.NewDiscontinueItem(e.deps).Execute(context.Background(), "item-1")
		return err
	}
	runSteps(t, e, []step{
		{"suspend owner", suspendOwner("e1", "seller-1", asOf1), []string{evSuppressed, evSuppressed},
			[]expect{held("lst-s", listing.ReasonOwnerSuspended)}},
		{"discontinue the item: the highest cause", discontinue, []string{"CatalogItemDiscontinued", evSuppressed, evSuppressed},
			[]expect{held("lst-s", listing.ReasonDiscontinued), held("lst-t", listing.ReasonDiscontinued)}},
		{"reinstate owner: discontinued stays", reinstateOwner("e2", "seller-1", asOf2), []string{evRestored},
			[]expect{held("lst-s", listing.ReasonDiscontinued), live("lst-z")}},
		{"activate the node again: discontinued stays", nodeTo("e3", "node-1", nodestatus.StatusActive, asOf2), nil,
			[]expect{held("lst-s", listing.ReasonDiscontinued), held("lst-t", listing.ReasonDiscontinued), live("lst-o")}},
	})
}

func TestReinstateHandsOverToTheRemainingCause(t *testing.T) {
	// A listing left at OWNER_SUSPENDED on a node that is not sellable (a state
	// the policies never produce, stored directly) reconciles to NODE_NOT_SELLABLE.
	e := newEnv(t)
	e.seedNode("node-1", nodestatus.StatusSuspended)
	e.seedItem("item-1")
	e.seedOwned("lst-s", "item-1", "node-1", "seller-1", listing.Sellability{NodeActive: true, OwnerSuspended: true})
	mustNot(t, e.store.Sellers().Save(context.Background(), repocontract.NewSellerView(t, "seller-1", sellerstatus.StatusSuspended)))
	runSteps(t, e, []step{
		{"reinstate owner", reinstateOwner("e1", "seller-1", asOf1), []string{evSuppressed},
			[]expect{held("lst-s", listing.ReasonNodeNotSellable)}},
		{"activate node", nodeTo("e2", "node-1", nodestatus.StatusActive, asOf1), []string{evRestored},
			[]expect{live("lst-s")}},
	})
	if ev, ok := e.events()[0].(listing.ListingSuppressed); !ok || ev.Reason != listing.ReasonNodeNotSellable || ev.AggregateVersion() != 2 {
		t.Fatalf("hand-over event = %+v", e.events()[0])
	}
}

func TestCreateListingForASuspendedOwner(t *testing.T) {
	e := sellerEnv(t)
	e.seedItem("item-3")
	create := func(node listing.NodeID) func(*env) error {
		return func(e *env) error {
			_, err := usecases.NewCreateListing(e.deps).Execute(context.Background(),
				usecases.CreateListingCommand{CatalogItemID: "item-3", NodeID: node, OwnerRef: "seller-1"})
			return err
		}
	}
	runSteps(t, e, []step{
		{"suspend owner", suspendOwner("e1", "seller-1", asOf1), []string{evSuppressed, evSuppressed}, nil},
		{"create on an active node", create("node-1"), []string{"ListingCreated", evSuppressed}, nil},
		{"create on an unknown node: the node wins", create("node-9"), []string{"ListingCreated", evSuppressed}, nil},
		{"activate the unknown node: the owner still holds", nodeTo("e2", "node-9", nodestatus.StatusActive, asOf1), []string{evSuppressed}, nil},
		{"reinstate owner", reinstateOwner("e3", "seller-1", asOf2), []string{evRestored, evRestored, evRestored, evRestored}, nil},
	})
	reasons := []listing.Reason{listing.ReasonOwnerSuspended, listing.ReasonNodeNotSellable, listing.ReasonOwnerSuspended}
	got := suppressedOf(e.events()[2:])
	if len(got) != len(reasons) {
		t.Fatalf("suppressions after the owner's = %+v", got)
	}
	for i, ev := range got {
		if ev.Reason != reasons[i] {
			t.Errorf("suppression %d: reason %s, want %s", i, ev.Reason, reasons[i])
		}
	}
}

func suppressedOf(events []shared.Event) []listing.ListingSuppressed {
	var out []listing.ListingSuppressed
	for _, ev := range events {
		if s, ok := ev.(listing.ListingSuppressed); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestCreateListingForAnActiveOrUnknownOwnerIsLive(t *testing.T) {
	e := sellerEnv(t)
	e.seedItem("item-3")
	mustNot(t, e.store.Sellers().Save(context.Background(), repocontract.NewSellerView(t, "seller-3", sellerstatus.StatusActive)))
	for _, owner := range []listing.OwnerRef{"seller-3", "seller-unknown", ""} {
		got, err := usecases.NewCreateListing(e.deps).Execute(context.Background(),
			usecases.CreateListingCommand{CatalogItemID: "item-3", NodeID: "node-1", OwnerRef: owner})
		mustNot(t, err)
		if got.State != string(listing.StateLive) {
			t.Errorf("owner %q: listing %+v", owner, got)
		}
	}
}
