package listing

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func TestApplyAvailabilityReplacesTheValueObject(t *testing.T) {
	l := liveListing(t)
	asOf := t1.In(time.FixedZone("X", 3600))
	events, err := l.ApplyAvailability(120, BasisThroughputConstrained, asOf, "feed-7", t2.In(time.FixedZone("X", 3600)))
	if err != nil {
		t.Fatalf("ApplyAvailability: %v", err)
	}
	want := Availability{Qty: 120, Basis: BasisThroughputConstrained, AsOf: t1, FeedID: "feed-7"}
	if l.Availability() != want || l.Version() != 2 || l.State() != StateLive {
		t.Fatalf("after apply: %+v version %d state %s", l.Availability(), l.Version(), l.State())
	}
	ev := onlyEvent[ListingAvailabilityChanged](t, events)
	wantEv := ListingAvailabilityChanged{
		Header:    shared.Header{Version: 2, At: t2},
		ListingID: "lst-1", CatalogItemID: "item-1", NodeID: "node-1",
		AvailableQty: 120, Basis: BasisThroughputConstrained, AsOf: t1, FeedID: "feed-7",
	}
	if ev != wantEv || ev.EventName() != "ListingAvailabilityChanged" {
		t.Fatalf("event = %+v, want %+v", ev, wantEv)
	}
	assertHeader(t, ev, 2, t2, "lst-1")
}

func TestApplyAvailabilityQuantityBounds(t *testing.T) {
	cases := []struct {
		qty  int
		want error
	}{
		{-1, ErrNegativeAvailability},
		{0, nil},
		{1, nil},
		{MaxQty, nil},
		{MaxQty + 1, shared.ErrValidation},
	}
	for _, c := range cases {
		l := liveListing(t)
		before := l.Snapshot()
		_, err := l.ApplyAvailability(c.qty, BasisPhysical, t1, "feed-1", t1)
		if c.want == nil {
			if err != nil {
				t.Errorf("qty %d: unexpected %v", c.qty, err)
			}
			continue
		}
		if !errors.Is(err, c.want) {
			t.Errorf("qty %d: want %v, got %v", c.qty, c.want, err)
		}
		assertUntouched(t, l, before)
	}
}

func TestApplyAvailabilityRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name   string
		basis  Basis
		asOf   time.Time
		feedID string
	}{
		{"unknown basis", "SOME", t1, "feed-1"},
		{"empty basis", "", t1, "feed-1"},
		{"zero asOf", BasisPhysical, time.Time{}, "feed-1"},
		{"empty feed id", BasisPhysical, t1, ""},
		{"malformed feed id", BasisPhysical, t1, "feed 1"},
	}
	for _, c := range cases {
		l := liveListing(t)
		before := l.Snapshot()
		if _, err := l.ApplyAvailability(5, c.basis, c.asOf, c.feedID, t1); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", c.name, err)
		}
		assertUntouched(t, l, before)
	}
}

func TestApplyAvailabilityIsMonotonicPerListing(t *testing.T) {
	cases := []struct {
		name  string
		asOf  time.Time
		stale bool
	}{
		{"older", t1.Add(-time.Second), true},
		{"equal", t1, true},
		{"one nanosecond later", t1.Add(time.Nanosecond), false},
		{"later", t2, false},
	}
	for _, c := range cases {
		l := fedListing(t, 10, BasisPhysical)
		before := l.Snapshot()
		events, err := l.ApplyAvailability(99, BasisUnknown, c.asOf, "feed-2", t2)
		if c.stale {
			if !errors.Is(err, ErrStaleAvailability) || events != nil {
				t.Errorf("%s: want ErrStaleAvailability, got %v %v", c.name, events, err)
			}
			assertUntouched(t, l, before)
			continue
		}
		if err != nil || len(events) != 1 || l.Availability().Qty != 99 || l.Availability().FeedID != "feed-2" {
			t.Errorf("%s: err %v events %v availability %+v", c.name, err, events, l.Availability())
		}
	}
}

func TestNegativeQuantityWinsOverStaleness(t *testing.T) {
	l := fedListing(t, 10, BasisPhysical)
	before := l.Snapshot()
	if _, err := l.ApplyAvailability(-5, BasisPhysical, t0, "feed-2", t2); !errors.Is(err, ErrNegativeAvailability) {
		t.Fatalf("a negative stale line must fail with ErrNegativeAvailability, got %v", err)
	}
	assertUntouched(t, l, before)
}

func TestApplyAvailabilityOnSuppressedListingKeepsTheState(t *testing.T) {
	l := suppressedListing(t, ReasonNodeNotSellable)
	if _, err := l.ApplyAvailability(7, BasisPhysical, t1, "feed-1", t1); err != nil {
		t.Fatalf("a suppressed listing takes availability: %v", err)
	}
	if l.State() != StateSuppressed || l.Reason() != ReasonNodeNotSellable || l.Availability().Qty != 7 {
		t.Fatalf("state or availability wrong: %+v", l.Snapshot())
	}
}

func TestApplyAvailabilityWithUnchangedNumbersStillRaisesTheEvent(t *testing.T) {
	l := fedListing(t, 10, BasisPhysical)
	events, err := l.ApplyAvailability(10, BasisPhysical, t2, "feed-2", t2)
	if err != nil || len(events) != 1 || l.Availability().AsOf != t2 {
		t.Fatalf("err %v events %v availability %+v", err, events, l.Availability())
	}
}

func TestZeroOut(t *testing.T) {
	t.Run("keeps the basis and stamps the feed", func(t *testing.T) {
		l := fedListing(t, 40, BasisThroughputConstrained)
		events, err := l.ZeroOut(t2, "feed-9", t2)
		if err != nil {
			t.Fatalf("ZeroOut: %v", err)
		}
		want := Availability{Qty: 0, Basis: BasisThroughputConstrained, AsOf: t2, FeedID: "feed-9"}
		if l.Availability() != want || l.Version() != 3 {
			t.Fatalf("after zero-out: %+v version %d", l.Availability(), l.Version())
		}
		ev := onlyEvent[ListingAvailabilityChanged](t, events)
		if ev.AvailableQty != 0 || ev.Basis != BasisThroughputConstrained || ev.FeedID != "feed-9" || !ev.AsOf.Equal(t2) {
			t.Fatalf("event = %+v", ev)
		}
	})
	t.Run("a listing already at zero is untouched", func(t *testing.T) {
		l := fedListing(t, 0, BasisPhysical)
		before := l.Snapshot()
		events, err := l.ZeroOut(t2, "feed-9", t2)
		if err != nil || events != nil {
			t.Fatalf("want a silent no-op, got %v %v", events, err)
		}
		assertUntouched(t, l, before)
	})
	t.Run("a never-fed listing is untouched", func(t *testing.T) {
		l := liveListing(t)
		before := l.Snapshot()
		if events, err := l.ZeroOut(t2, "feed-9", t2); err != nil || events != nil {
			t.Fatalf("want a silent no-op, got %v %v", events, err)
		}
		assertUntouched(t, l, before)
	})
	t.Run("a listing not older than the feed is stale", func(t *testing.T) {
		l := fedListing(t, 40, BasisPhysical)
		before := l.Snapshot()
		if _, err := l.ZeroOut(t1, "feed-9", t2); !errors.Is(err, ErrStaleAvailability) {
			t.Fatalf("want ErrStaleAvailability, got %v", err)
		}
		assertUntouched(t, l, before)
	})
}
