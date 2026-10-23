package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func nodeEvent(id string, status nodestatus.Status, at time.Time) usecases.NodeEvent {
	return usecases.NodeEvent{EventID: id, EventType: "com.retailnetwork.logistics.node-topology.node." + string(status), NodeID: "node-1", Status: status, At: at}
}

func handle(e *env, ev usecases.NodeEvent) error {
	return usecases.NewApplyNodeEvent(e.deps).Execute(context.Background(), ev)
}

func nodeView(t *testing.T, e *env) *nodestatus.View {
	t.Helper()
	v, err := e.store.Nodes().Get(context.Background(), "node-1")
	mustNot(t, err)
	return v
}

// nodeEnv: node-1 is ACTIVE with two LIVE listings and one listing already suppressed for DISCONTINUED.
func nodeEnv(t *testing.T) *env {
	e := newEnv(t)
	e.seedNode("node-1", nodestatus.StatusActive)
	e.seedListing("lst-a", "item-1", "node-1", 3, repoNow())
	e.seedListing("lst-b", "item-2", "node-1", -1, repoNow())
	e.seedListing("lst-d", "item-3", "node-1", -1, repoNow())
	d := e.listing("lst-d")
	_, err := d.Suppress(listing.ReasonDiscontinued, repoNow())
	mustNot(t, err)
	mustNot(t, e.store.Listings().Save(context.Background(), d))
	e.seedListing("lst-x", "item-1", "node-2", -1, repoNow())
	return e
}

func TestNodeSuspendedSuppressesLiveListings(t *testing.T) {
	e := nodeEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1)))
	if nodeView(t, e).Status() != nodestatus.StatusSuspended {
		t.Fatal("the view must be SUSPENDED")
	}
	e.assertEvents("ListingSuppressed", "ListingSuppressed")
	for _, id := range []listing.ID{"lst-a", "lst-b"} {
		if l := e.listing(id); l.State() != listing.StateSuppressed || l.Reason() != listing.ReasonNodeNotSellable {
			t.Errorf("%s: %s %s", id, l.State(), l.Reason())
		}
	}
	if e.listing("lst-d").Reason() != listing.ReasonDiscontinued || e.listing("lst-d").Version() != 2 {
		t.Fatal("a listing suppressed for DISCONTINUED must be left alone")
	}
	if e.listing("lst-x").State() != listing.StateLive {
		t.Fatal("a listing of another node must be left alone")
	}
	if e.listing("lst-a").Availability().Qty != 3 {
		t.Fatal("suppressing keeps the availability")
	}
}

func TestNodeRetiredSuppressesAndIsTerminal(t *testing.T) {
	e := nodeEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusRetired, asOf1)))
	e.assertEvents("ListingSuppressed", "ListingSuppressed")
	mustNot(t, handle(e, nodeEvent("evt-2", nodestatus.StatusActive, asOf2)))
	if nodeView(t, e).Status() != nodestatus.StatusRetired || e.listing("lst-a").State() != listing.StateSuppressed {
		t.Fatal("a retired node must stay retired and its listings suppressed")
	}
	e.assertEvents("ListingSuppressed", "ListingSuppressed")
}

func TestNodeActivatedRestoresOnlyNodeSuppressedListings(t *testing.T) {
	e := nodeEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1)))
	mustNot(t, handle(e, nodeEvent("evt-2", nodestatus.StatusActive, asOf2)))
	e.assertEvents("ListingSuppressed", "ListingSuppressed", "ListingRestored", "ListingRestored")
	for _, id := range []listing.ID{"lst-a", "lst-b"} {
		if e.listing(id).State() != listing.StateLive {
			t.Errorf("%s must be LIVE again", id)
		}
	}
	if d := e.listing("lst-d"); d.State() != listing.StateSuppressed || d.Reason() != listing.ReasonDiscontinued {
		t.Fatal("DISCONTINUED is final")
	}
}

func TestDuplicateEventIdIsSkipped(t *testing.T) {
	e := nodeEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1)))
	before := e.store.Counts()
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusActive, asOf2)))
	e.assertNothingStored(before)
}

func TestOutOfOrderEventIsIgnored(t *testing.T) {
	e := nodeEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf2)))
	before := e.store.Counts()
	mustNot(t, handle(e, nodeEvent("evt-2", nodestatus.StatusActive, asOf1)))
	after := e.store.Counts()
	if after.Processed != before.Processed+1 || after.OutboxEvents != before.OutboxEvents || after.VersionSum != before.VersionSum {
		t.Fatalf("an old event only lands in the inbox: before %+v after %+v", before, after)
	}
	if nodeView(t, e).Status() != nodestatus.StatusSuspended {
		t.Fatal("the view must keep the newer status")
	}
}

func TestRepeatedStatusRunsThePolicyAgain(t *testing.T) {
	e := nodeEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1)))
	// A listing created on the suspended node afterwards is already suppressed; a LIVE one
	// that slipped in is healed by the repeated event.
	e.seedListing("lst-late", "item-9", "node-1", -1, repoNow())
	mustNot(t, handle(e, nodeEvent("evt-2", nodestatus.StatusSuspended, asOf2)))
	if e.listing("lst-late").State() != listing.StateSuppressed {
		t.Fatal("the repeated event must suppress the LIVE listing")
	}
	if nodeView(t, e).Version() != 2 {
		t.Fatal("a repeated status does not save the view again")
	}
}

func TestNodeRegisteredCreatesProposedViewWithoutPolicy(t *testing.T) {
	e := newEnv(t)
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusProposed, asOf1)))
	if v := nodeView(t, e); v.Status() != nodestatus.StatusProposed || v.Version() != 1 {
		t.Fatalf("view = %+v", v.Snapshot())
	}
	e.assertEvents()
}

func TestNodeRegisteredForAKnownNodeChangesNothing(t *testing.T) {
	e := nodeEnv(t)
	before := e.store.Counts()
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusProposed, asOf1)))
	after := e.store.Counts()
	if after.Processed != before.Processed+1 || after.VersionSum != before.VersionSum || after.OutboxEvents != 0 {
		t.Fatalf("before %+v after %+v", before, after)
	}
	if nodeView(t, e).Status() != nodestatus.StatusActive {
		t.Fatal("registering a known node must not demote it")
	}
}

func TestEventForAnUnknownNodeCreatesTheViewAndRunsThePolicy(t *testing.T) {
	e := newEnv(t)
	e.seedListing("lst-a", "item-1", "node-1", -1, repoNow())
	l := e.listing("lst-a")
	_, err := l.Suppress(listing.ReasonNodeNotSellable, repoNow())
	mustNot(t, err)
	mustNot(t, e.store.Listings().Save(context.Background(), l))
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusActive, asOf1)))
	if nodeView(t, e).Status() != nodestatus.StatusActive || e.listing("lst-a").State() != listing.StateLive {
		t.Fatal("NodeActivated for an unknown node must create the view and restore the listing")
	}
	e.assertEvents("ListingRestored")
}

func TestNodeEventFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*env)
		ev    usecases.NodeEvent
		want  error
	}{
		{"no event id", func(*env) {}, usecases.NodeEvent{EventType: "t", NodeID: "node-1", Status: nodestatus.StatusActive, At: asOf1}, shared.ErrValidation},
		{"no event type", func(*env) {}, usecases.NodeEvent{EventID: "evt-1", NodeID: "node-1", Status: nodestatus.StatusActive, At: asOf1}, shared.ErrValidation},
		{"malformed node id", func(*env) {}, usecases.NodeEvent{EventID: "evt-1", EventType: "t", NodeID: "a b", Status: nodestatus.StatusActive, At: asOf1}, shared.ErrValidation},
		{"no instant", func(*env) {}, usecases.NodeEvent{EventID: "evt-1", EventType: "t", NodeID: "node-1", Status: nodestatus.StatusActive}, shared.ErrValidation},
		{"inbox fails", func(e *env) { e.inbox.err = errBoom }, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1), errBoom},
		{"view read fails", func(e *env) { e.nodes.getErr = errBoom }, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1), errBoom},
		{"view save fails", func(e *env) { e.nodes.saveErr = errBoom }, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1), errBoom},
		{"listing read fails", func(e *env) { e.listings.byNodeErr = errBoom }, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1), errBoom},
		{"bulk save fails", func(e *env) { e.listings.saveManyErrs = []error{errBoom} }, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1), errBoom},
		{"outbox fails", func(e *env) { e.outbox.err = errBoom }, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1), errBoom},
	}
	for _, c := range cases {
		e := nodeEnv(t)
		c.setup(e)
		before := e.store.Counts()
		wantIs(t, handle(e, c.ev), c.want)
		e.assertNothingStored(before)
	}
}

func TestNodeEventForAnUnknownNodeWithAFailingSave(t *testing.T) {
	e := newEnv(t)
	e.nodes.saveErr = errBoom
	before := e.store.Counts()
	wantIs(t, handle(e, nodeEvent("evt-1", nodestatus.StatusActive, asOf1)), errBoom)
	e.assertNothingStored(before)
}

func TestNodeEventRetriesOnceOnConcurrentUpdate(t *testing.T) {
	e := nodeEnv(t)
	e.listings.saveManyErrs = []error{ports.ErrConcurrentUpdate}
	mustNot(t, handle(e, nodeEvent("evt-1", nodestatus.StatusSuspended, asOf1)))
	e.assertEvents("ListingSuppressed", "ListingSuppressed")
}
