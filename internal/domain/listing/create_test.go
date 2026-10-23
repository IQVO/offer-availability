package listing

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var (
	t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
)

func newListing(t *testing.T, s Sellability) *Listing {
	t.Helper()
	l, _, err := Create("lst-1", "item-1", "node-1", s, t0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return l
}

func liveListing(t *testing.T) *Listing {
	t.Helper()
	return newListing(t, Sellability{NodeActive: true})
}

func suppressedListing(t *testing.T, reason Reason) *Listing {
	t.Helper()
	l := liveListing(t)
	if _, err := l.Suppress(reason, t0); err != nil {
		t.Fatalf("Suppress: %v", err)
	}
	return l
}

func fedListing(t *testing.T, qty int, basis Basis) *Listing {
	t.Helper()
	l := liveListing(t)
	if _, err := l.ApplyAvailability(qty, basis, t1, "feed-1", t1); err != nil {
		t.Fatalf("ApplyAvailability: %v", err)
	}
	return l
}

// onlyEvent returns the single event of events, failing the test otherwise.
func onlyEvent[T shared.Event](t *testing.T, events []shared.Event) T {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("want exactly 1 event, got %d: %v", len(events), events)
	}
	ev, ok := events[0].(T)
	if !ok {
		t.Fatalf("event is %T", events[0])
	}
	return ev
}

func assertHeader(t *testing.T, ev shared.Event, version int64, at time.Time, subject string) {
	t.Helper()
	if ev.AggregateVersion() != version || !ev.OccurredAt().Equal(at) || ev.SubjectID() != subject || ev.Entity() != "listing" {
		t.Fatalf("header of %s: version %d at %v subject %q entity %q", ev.EventName(),
			ev.AggregateVersion(), ev.OccurredAt(), ev.SubjectID(), ev.Entity())
	}
}

func assertUntouched(t *testing.T, l *Listing, before Snapshot) {
	t.Helper()
	if l.Snapshot() != before {
		t.Fatalf("a failed command changed the listing: %+v != %+v", l.Snapshot(), before)
	}
}

func TestCreateLiveOnActiveNode(t *testing.T) {
	l, events, err := Create("lst-1", "item-1", "node-1", Sellability{NodeActive: true}, t0.In(time.FixedZone("X", -7200)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if l.ID() != "lst-1" || l.ItemID() != "item-1" || l.NodeID() != "node-1" || l.State() != StateLive || l.Reason() != "" || l.Version() != 1 {
		t.Fatalf("unexpected listing %+v", l.Snapshot())
	}
	if l.Availability() != (Availability{Qty: 0, Basis: BasisUnknown}) {
		t.Fatalf("initial availability = %+v", l.Availability())
	}
	ev := onlyEvent[ListingCreated](t, events)
	if ev.ListingID != "lst-1" || ev.CatalogItemID != "item-1" || ev.NodeID != "node-1" || ev.EventName() != "ListingCreated" {
		t.Fatalf("event = %+v", ev)
	}
	assertHeader(t, ev, 1, t0, "lst-1")
}

func TestCreateStartsSuppressedWhenNotSellable(t *testing.T) {
	cases := []struct {
		name string
		s    Sellability
		want Reason
	}{
		{"node not active", Sellability{}, ReasonNodeNotSellable},
		{"item discontinued", Sellability{NodeActive: true, ItemDiscontinued: true}, ReasonDiscontinued},
		{"both: discontinued wins", Sellability{ItemDiscontinued: true}, ReasonDiscontinued},
	}
	for _, c := range cases {
		l, events, err := Create("lst-1", "item-1", "node-1", c.s, t0)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if l.State() != StateSuppressed || l.Reason() != c.want || l.Version() != 1 {
			t.Errorf("%s: state %s reason %s version %d", c.name, l.State(), l.Reason(), l.Version())
		}
		if len(events) != 2 {
			t.Fatalf("%s: want ListingCreated + ListingSuppressed, got %v", c.name, events)
		}
		created, ok1 := events[0].(ListingCreated)
		sup, ok2 := events[1].(ListingSuppressed)
		if !ok1 || !ok2 {
			t.Fatalf("%s: events are %T, %T", c.name, events[0], events[1])
		}
		if sup.Reason != c.want || sup.NodeID != "node-1" || sup.ListingID != "lst-1" || sup.EventName() != "ListingSuppressed" {
			t.Errorf("%s: suppressed event %+v", c.name, sup)
		}
		assertHeader(t, created, 1, t0, "lst-1")
		assertHeader(t, sup, 1, t0, "lst-1")
	}
}

func TestCreateRejectsMalformedIDs(t *testing.T) {
	cases := []struct{ id, item, node string }{
		{"", "item-1", "node-1"},
		{"lst 1", "item-1", "node-1"},
		{"lst-1", "", "node-1"},
		{"lst-1", "item-1", ""},
		{"lst-1", "item-1", "-node"},
	}
	for _, c := range cases {
		if _, _, err := Create(ID(c.id), ItemID(c.item), NodeID(c.node), Sellability{NodeActive: true}, t0); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%+v: want ErrValidation, got %v", c, err)
		}
	}
}

func TestEnumValidity(t *testing.T) {
	assertAllValid(t, "state", StateLive, StateSuppressed)
	assertAllValid(t, "reason", ReasonNodeNotSellable, ReasonDiscontinued)
	assertAllValid(t, "basis", BasisPhysical, BasisThroughputConstrained, BasisUnknown)
	assertNoneValid(t, "state", State(""), State("X"))
	assertNoneValid(t, "reason", Reason(""), Reason("X"))
	assertNoneValid(t, "basis", Basis(""), Basis("X"))
}

func TestIDValidity(t *testing.T) {
	assertAllValid(t, "id", ID("a"))
	assertAllValid(t, "item id", ItemID("a"))
	assertAllValid(t, "node id", NodeID("a"))
	assertNoneValid(t, "id", ID(""))
	assertNoneValid(t, "item id", ItemID(""))
	assertNoneValid(t, "node id", NodeID(""))
}

type validator interface{ Valid() bool }

func assertAllValid[T validator](t *testing.T, what string, values ...T) {
	t.Helper()
	for _, v := range values {
		if !v.Valid() {
			t.Errorf("%s %v must be valid", what, v)
		}
	}
}

func assertNoneValid[T validator](t *testing.T, what string, values ...T) {
	t.Helper()
	for _, v := range values {
		if v.Valid() {
			t.Errorf("%s %v must be invalid", what, v)
		}
	}
}
