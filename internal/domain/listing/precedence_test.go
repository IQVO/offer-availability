package listing

import (
	"errors"
	"testing"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0013: OWNER_SUSPENDED and the precedence DISCONTINUED > NODE_NOT_SELLABLE
// > OWNER_SUSPENDED.

func TestOwnerSuspendedIsAReason(t *testing.T) {
	assertAllValid(t, "reason", ReasonOwnerSuspended)
	if ReasonOwnerSuspended != "OWNER_SUSPENDED" {
		t.Fatalf("wire value = %q", ReasonOwnerSuspended)
	}
}

func TestReasonPrecedence(t *testing.T) {
	order := []Reason{"", ReasonOwnerSuspended, ReasonNodeNotSellable, ReasonDiscontinued}
	for i, hi := range order {
		for j, lo := range order {
			if got, want := hi.Outranks(lo), i > j; got != want {
				t.Errorf("%q outranks %q = %v, want %v", hi, lo, got, want)
			}
		}
	}
	if Reason("X").Outranks("") {
		t.Error("an unknown reason must not outrank anything")
	}
}

func TestSellabilityCause(t *testing.T) {
	cases := []struct {
		s    Sellability
		want Reason
	}{
		{Sellability{NodeActive: true}, ""},
		{Sellability{NodeActive: true, OwnerSuspended: true}, ReasonOwnerSuspended},
		{Sellability{OwnerSuspended: true}, ReasonNodeNotSellable},
		{Sellability{}, ReasonNodeNotSellable},
		{Sellability{NodeActive: true, ItemDiscontinued: true}, ReasonDiscontinued},
		{Sellability{ItemDiscontinued: true, OwnerSuspended: true}, ReasonDiscontinued},
		{Sellability{NodeActive: true, ItemDiscontinued: true, OwnerSuspended: true}, ReasonDiscontinued},
	}
	for _, c := range cases {
		if got := c.s.Cause(); got != c.want {
			t.Errorf("%+v: cause %q, want %q", c.s, got, c.want)
		}
	}
}

func TestCreateForASuspendedOwner(t *testing.T) {
	cases := []struct {
		s    Sellability
		want Reason
	}{
		{Sellability{NodeActive: true, OwnerSuspended: true}, ReasonOwnerSuspended},
		{Sellability{OwnerSuspended: true}, ReasonNodeNotSellable},
		{Sellability{NodeActive: true, ItemDiscontinued: true, OwnerSuspended: true}, ReasonDiscontinued},
	}
	for _, c := range cases {
		l, events, err := CreateOwned("lst-1", "item-1", "node-1", "seller-1", c.s, t0)
		if err != nil {
			t.Fatalf("%+v: %v", c.s, err)
		}
		if l.State() != StateSuppressed || l.Reason() != c.want || l.Version() != 1 || len(events) != 2 {
			t.Fatalf("%+v: state %s reason %s version %d events %v", c.s, l.State(), l.Reason(), l.Version(), events)
		}
		sup, ok := events[1].(ListingSuppressed)
		if !ok || sup.Reason != c.want {
			t.Fatalf("%+v: second event %+v", c.s, events[1])
		}
		assertHeader(t, sup, 1, t0, "lst-1")
	}
}

func TestSuppressFollowsThePrecedence(t *testing.T) {
	cases := []struct {
		name    string
		current Reason
		reason  Reason
		legal   bool
	}{
		{"live to owner", "", ReasonOwnerSuspended, true},
		{"owner to node upgrades the reason", ReasonOwnerSuspended, ReasonNodeNotSellable, true},
		{"owner to discontinued upgrades the reason", ReasonOwnerSuspended, ReasonDiscontinued, true},
		{"owner to owner is a repeat", ReasonOwnerSuspended, ReasonOwnerSuspended, false},
		{"node keeps its reason against owner", ReasonNodeNotSellable, ReasonOwnerSuspended, false},
		{"discontinued keeps its reason against owner", ReasonDiscontinued, ReasonOwnerSuspended, false},
	}
	for _, c := range cases {
		l := liveListing(t)
		if c.current != "" {
			l = suppressedListing(t, c.current)
		}
		before := l.Snapshot()
		events, err := l.Suppress(c.reason, t2)
		if !c.legal {
			if !errors.Is(err, shared.ErrIllegalTransition) || events != nil {
				t.Errorf("%s: want ErrIllegalTransition, got %v %v", c.name, events, err)
			}
			assertUntouched(t, l, before)
			continue
		}
		if err != nil || l.State() != StateSuppressed || l.Reason() != c.reason || l.Version() != before.Version+1 {
			t.Fatalf("%s: err %v state %s reason %s", c.name, err, l.State(), l.Reason())
		}
		ev := onlyEvent[ListingSuppressed](t, events)
		if ev.Reason != c.reason {
			t.Fatalf("%s: event %+v", c.name, ev)
		}
		assertHeader(t, ev, before.Version+1, t2, "lst-1")
	}
}

// reconcileCase starts from a listing in state/reason (reason "" = LIVE) and
// reconciles it against s.
type reconcileCase struct {
	name      string
	current   Reason
	s         Sellability
	wantState State
	want      Reason
	wantEvent string // "" = no event
}

var reconcileCases = []reconcileCase{
	{"live and nothing holds", "", Sellability{NodeActive: true}, StateLive, "", ""},
	{"live and the owner is suspended", "", Sellability{NodeActive: true, OwnerSuspended: true}, StateSuppressed, ReasonOwnerSuspended, "ListingSuppressed"},
	{"live and the node is not sellable", "", Sellability{}, StateSuppressed, ReasonNodeNotSellable, "ListingSuppressed"},
	{"owner cleared, nothing else holds", ReasonOwnerSuspended, Sellability{NodeActive: true}, StateLive, "", "ListingRestored"},
	{"owner cleared, the node still holds", ReasonOwnerSuspended, Sellability{}, StateSuppressed, ReasonNodeNotSellable, "ListingSuppressed"},
	{"owner cleared, the item is discontinued", ReasonOwnerSuspended, Sellability{NodeActive: true, ItemDiscontinued: true}, StateSuppressed, ReasonDiscontinued, "ListingSuppressed"},
	{"owner still suspended", ReasonOwnerSuspended, Sellability{NodeActive: true, OwnerSuspended: true}, StateSuppressed, ReasonOwnerSuspended, ""},
	{"node cleared, nothing else holds", ReasonNodeNotSellable, Sellability{NodeActive: true}, StateLive, "", "ListingRestored"},
	{"node cleared, the owner still holds", ReasonNodeNotSellable, Sellability{NodeActive: true, OwnerSuspended: true}, StateSuppressed, ReasonOwnerSuspended, "ListingSuppressed"},
	{"node still not sellable", ReasonNodeNotSellable, Sellability{OwnerSuspended: true}, StateSuppressed, ReasonNodeNotSellable, ""},
	{"discontinued is final", ReasonDiscontinued, Sellability{NodeActive: true}, StateSuppressed, ReasonDiscontinued, ""},
	{"discontinued is final against the owner", ReasonDiscontinued, Sellability{NodeActive: true, OwnerSuspended: true}, StateSuppressed, ReasonDiscontinued, ""},
}

func TestReconcileMovesToTheHighestActiveCause(t *testing.T) {
	for _, c := range reconcileCases {
		l := liveListing(t)
		if c.current != "" {
			l = suppressedListing(t, c.current)
		}
		before := l.Snapshot()
		events := l.Reconcile(c.s, t2)
		if l.State() != c.wantState || l.Reason() != c.want {
			t.Errorf("%s: state %s reason %q, want %s %q", c.name, l.State(), l.Reason(), c.wantState, c.want)
		}
		assertReconcileEvent(t, c, l, before, events)
	}
}

func assertReconcileEvent(t *testing.T, c reconcileCase, l *Listing, before Snapshot, events []shared.Event) {
	t.Helper()
	if c.wantEvent == "" {
		if events != nil {
			t.Errorf("%s: want no event, got %v", c.name, events)
		}
		assertUntouched(t, l, before)
		return
	}
	if len(events) != 1 || events[0].EventName() != c.wantEvent || l.Version() != before.Version+1 {
		t.Fatalf("%s: events %v version %d", c.name, events, l.Version())
	}
	assertHeader(t, events[0], before.Version+1, t2, "lst-1")
	if sup, ok := events[0].(ListingSuppressed); ok && (sup.Reason != c.want || sup.NodeID != "node-1") {
		t.Errorf("%s: suppressed event %+v", c.name, sup)
	}
	if res, ok := events[0].(ListingRestored); ok && res.NodeID != "node-1" {
		t.Errorf("%s: restored event %+v", c.name, res)
	}
}

func TestReconcileKeepsTheAvailability(t *testing.T) {
	l := fedListing(t, 21, BasisThroughputConstrained)
	l.Reconcile(Sellability{NodeActive: true, OwnerSuspended: true}, t2)
	l.Reconcile(Sellability{NodeActive: true}, t2)
	if l.State() != StateLive || l.Availability().Qty != 21 || l.Availability().Basis != BasisThroughputConstrained {
		t.Fatalf("state %s availability %+v", l.State(), l.Availability())
	}
}

func TestRestoreAcceptsAnOwnerSuspendedSnapshot(t *testing.T) {
	l, err := Restore(Snapshot{ID: "lst-1", ItemID: "item-1", NodeID: "node-1", OwnerRef: "seller-1",
		State: StateSuppressed, Reason: ReasonOwnerSuspended, Basis: BasisUnknown, Version: 3})
	if err != nil || l.Reason() != ReasonOwnerSuspended || l.OwnerRef() != "seller-1" {
		t.Fatalf("Restore: %v %+v", err, l)
	}
}
