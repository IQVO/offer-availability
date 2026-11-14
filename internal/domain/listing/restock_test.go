package listing

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// restockedFedListing is a LIVE listing whose availability a feed set to 10
// PHYSICAL as of t1.
func restockedFedListing(t *testing.T) *Listing {
	t.Helper()
	return fedListing(t, 10, BasisPhysical)
}

func TestRestockAddsToTheQuantity(t *testing.T) {
	l := restockedFedListing(t)
	events, err := l.Restock(2, t2, "ret-1", t2)
	if err != nil {
		t.Fatalf("Restock: %v", err)
	}
	want := Availability{Qty: 12, Basis: BasisPhysical, AsOf: t2, FeedID: "ret-1"}
	if l.Availability() != want || l.Version() != 3 || l.State() != StateLive {
		t.Fatalf("after restock: %+v version %d state %s", l.Availability(), l.Version(), l.State())
	}
	ev := onlyEvent[ListingAvailabilityChanged](t, events)
	wantEv := ListingAvailabilityChanged{
		Header: shared.Header{Version: 3, At: t2}, ListingID: "lst-1", CatalogItemID: "item-1", NodeID: "node-1",
		AvailableQty: 12, Basis: BasisPhysical, AsOf: t2, FeedID: "ret-1",
	}
	if ev != wantEv {
		t.Fatalf("event = %+v, want %+v", ev, wantEv)
	}
	assertHeader(t, ev, 3, t2, "lst-1")
}

func TestRestockAListingThatNeverHadAFeedKeepsUnknownBasis(t *testing.T) {
	l := liveListing(t) // availability 0 / UNKNOWN, no asOf, no feed
	events, err := l.Restock(3, t1, "ret-1", t2)
	if err != nil {
		t.Fatalf("Restock: %v", err)
	}
	want := Availability{Qty: 3, Basis: BasisUnknown, AsOf: t1, FeedID: "ret-1"}
	if l.Availability() != want {
		t.Fatalf("after restock: %+v, want %+v", l.Availability(), want)
	}
	if ev := onlyEvent[ListingAvailabilityChanged](t, events); ev.Basis != BasisUnknown || ev.AvailableQty != 3 {
		t.Fatalf("event = %+v", ev)
	}
	// The snapshot invariant holds: the pair is stamped together.
	if _, err := Restore(l.Snapshot()); err != nil {
		t.Fatalf("restore a restocked listing: %v", err)
	}
}

func TestRestockOlderThanTheCurrentFactKeepsTheFeedStamp(t *testing.T) {
	l := restockedFedListing(t) // asOf t1, feed-1
	if _, err := l.Restock(2, t1.Add(-time.Hour), "ret-1", t2); err != nil {
		t.Fatalf("Restock: %v", err)
	}
	want := Availability{Qty: 12, Basis: BasisPhysical, AsOf: t1, FeedID: "feed-1"}
	if l.Availability() != want {
		t.Fatalf("after an older restock: %+v, want %+v", l.Availability(), want)
	}
}

func TestRestockDoesNotTouchTheState(t *testing.T) {
	l := liveListing(t)
	if _, err := l.Suppress(ReasonNodeNotSellable, t1); err != nil {
		t.Fatalf("Suppress: %v", err)
	}
	if _, err := l.Restock(2, t1, "ret-1", t2); err != nil {
		t.Fatalf("Restock: %v", err)
	}
	if l.State() != StateSuppressed || l.Reason() != ReasonNodeNotSellable || l.Availability().Qty != 2 {
		t.Fatalf("a SUPPRESSED listing is restocked without changing state: %s/%s %+v", l.State(), l.Reason(), l.Availability())
	}
}

func TestRestockRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		qty  int
		at   time.Time
		ref  string
	}{
		{"zero qty", 0, t1, "ret-1"},
		{"negative qty", -2, t1, "ret-1"},
		{"no instant", 2, time.Time{}, "ret-1"},
		{"no ref", 2, t1, ""},
		{"malformed ref", 2, t1, "ret 1"},
		{"above the ceiling", MaxQty + 1, t1, "ret-1"},
	}
	for _, c := range cases {
		l := liveListing(t)
		before := l.Snapshot()
		_, err := l.Restock(c.qty, c.at, c.ref, t1)
		if !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", c.name, err)
		}
		assertUntouched(t, l, before)
	}
}

func TestRestockSumsToTheCeiling(t *testing.T) {
	l := liveListing(t)
	if _, err := l.ApplyAvailability(MaxQty-2, BasisPhysical, t1, "feed-1", t1); err != nil {
		t.Fatalf("ApplyAvailability: %v", err)
	}
	if _, err := l.Restock(2, t2, "ret-1", t2); err != nil {
		t.Fatalf("Restock to the ceiling: %v", err)
	}
	if l.Availability().Qty != MaxQty {
		t.Fatalf("qty = %d, want %d", l.Availability().Qty, MaxQty)
	}
}
