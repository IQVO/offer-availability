package listing

import (
	"errors"
	"testing"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func TestSuppressFromLive(t *testing.T) {
	for _, reason := range []Reason{ReasonNodeNotSellable, ReasonDiscontinued} {
		l := liveListing(t)
		events, err := l.Suppress(reason, t1)
		if err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
		if l.State() != StateSuppressed || l.Reason() != reason || l.Version() != 2 {
			t.Fatalf("%s: state %s reason %s version %d", reason, l.State(), l.Reason(), l.Version())
		}
		ev := onlyEvent[ListingSuppressed](t, events)
		if ev.Reason != reason || ev.NodeID != "node-1" || ev.ListingID != "lst-1" {
			t.Fatalf("event = %+v", ev)
		}
		assertHeader(t, ev, 2, t1, "lst-1")
	}
}

func TestSuppressRejectsAnUnknownReason(t *testing.T) {
	for _, reason := range []Reason{"", "BORED"} {
		l := liveListing(t)
		before := l.Snapshot()
		if _, err := l.Suppress(reason, t1); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("reason %q: want ErrValidation, got %v", reason, err)
		}
		assertUntouched(t, l, before)
	}
}

func TestSuppressTransitionTable(t *testing.T) {
	cases := []struct {
		name    string
		current Reason // "" = LIVE
		reason  Reason
		legal   bool
	}{
		{"live to node", "", ReasonNodeNotSellable, true},
		{"live to discontinued", "", ReasonDiscontinued, true},
		{"node to discontinued upgrades the reason", ReasonNodeNotSellable, ReasonDiscontinued, true},
		{"node to node is a repeat", ReasonNodeNotSellable, ReasonNodeNotSellable, false},
		{"discontinued to discontinued is a repeat", ReasonDiscontinued, ReasonDiscontinued, false},
		{"discontinued never downgrades", ReasonDiscontinued, ReasonNodeNotSellable, false},
	}
	for _, c := range cases {
		l := liveListing(t)
		if c.current != "" {
			suppressed := suppressedListing(t, c.current)
			l = suppressed
		}
		before := l.Snapshot()
		events, err := l.Suppress(c.reason, t2)
		if c.legal {
			if err != nil || len(events) != 1 || l.Reason() != c.reason || l.Version() != before.Version+1 {
				t.Errorf("%s: err %v events %v reason %s", c.name, err, events, l.Reason())
			}
			continue
		}
		if !errors.Is(err, shared.ErrIllegalTransition) || events != nil {
			t.Errorf("%s: want ErrIllegalTransition, got %v %v", c.name, events, err)
		}
		assertUntouched(t, l, before)
	}
}

func TestRestoreTransitionTable(t *testing.T) {
	cases := []struct {
		name     string
		build    func(*testing.T) *Listing
		sellable bool
		want     error
	}{
		{"suppressed for the node and the node is sellable", func(t *testing.T) *Listing { return suppressedListing(t, ReasonNodeNotSellable) }, true, nil},
		{"suppressed for the node but the node is not sellable", func(t *testing.T) *Listing { return suppressedListing(t, ReasonNodeNotSellable) }, false, ErrNodeNotSellable},
		{"live listing", liveListing, true, shared.ErrIllegalTransition},
		{"discontinued is final", func(t *testing.T) *Listing { return suppressedListing(t, ReasonDiscontinued) }, true, shared.ErrIllegalTransition},
		{"discontinued is final on an unsellable node too", func(t *testing.T) *Listing { return suppressedListing(t, ReasonDiscontinued) }, false, shared.ErrIllegalTransition},
		{"live listing on an unsellable node", liveListing, false, shared.ErrIllegalTransition},
	}
	for _, c := range cases {
		l := c.build(t)
		before := l.Snapshot()
		events, err := l.Restore(c.sellable, t2)
		if c.want != nil {
			if !errors.Is(err, c.want) || events != nil {
				t.Errorf("%s: want %v, got %v %v", c.name, c.want, events, err)
			}
			assertUntouched(t, l, before)
			continue
		}
		if err != nil || l.State() != StateLive || l.Reason() != "" || l.Version() != before.Version+1 {
			t.Fatalf("%s: err %v state %s reason %q", c.name, err, l.State(), l.Reason())
		}
		ev := onlyEvent[ListingRestored](t, events)
		if ev.ListingID != "lst-1" || ev.NodeID != "node-1" || ev.EventName() != "ListingRestored" {
			t.Fatalf("event = %+v", ev)
		}
		assertHeader(t, ev, before.Version+1, t2, "lst-1")
	}
}

func TestRestoreKeepsTheAvailability(t *testing.T) {
	l := fedListing(t, 33, BasisPhysical)
	if _, err := l.Suppress(ReasonNodeNotSellable, t1); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Restore(true, t2); err != nil {
		t.Fatal(err)
	}
	if l.Availability().Qty != 33 || l.Availability().Basis != BasisPhysical {
		t.Fatalf("availability changed: %+v", l.Availability())
	}
}
