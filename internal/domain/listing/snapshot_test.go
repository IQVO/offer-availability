package listing

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func TestSnapshotRoundTrip(t *testing.T) {
	builds := map[string]func(*testing.T) *Listing{
		"never fed":           liveListing,
		"fed":                 func(t *testing.T) *Listing { return fedListing(t, 12, BasisThroughputConstrained) },
		"suppressed for node": func(t *testing.T) *Listing { return suppressedListing(t, ReasonNodeNotSellable) },
		"suppressed discont.": func(t *testing.T) *Listing { return suppressedListing(t, ReasonDiscontinued) },
		"born suppressed":     func(t *testing.T) *Listing { return newListing(t, Sellability{}) },
	}
	for name, build := range builds {
		snap := build(t).Snapshot()
		back, err := Restore(snap)
		if err != nil {
			t.Fatalf("%s: Restore: %v", name, err)
		}
		if back.Snapshot() != snap {
			t.Errorf("%s: round trip %+v != %+v", name, back.Snapshot(), snap)
		}
	}
}

func TestRestoreNormalisesToUTC(t *testing.T) {
	snap := fedListing(t, 1, BasisPhysical).Snapshot()
	snap.AsOf = snap.AsOf.In(time.FixedZone("X", 7200))
	back, err := Restore(snap)
	if err != nil {
		t.Fatal(err)
	}
	if back.Availability().AsOf.Location() != time.UTC {
		t.Fatalf("asOf not in UTC: %v", back.Availability().AsOf.Location())
	}
}

func TestRestoreRejectsCorruptRows(t *testing.T) {
	fed := fedListing(t, 5, BasisPhysical).Snapshot()
	sup := suppressedListing(t, ReasonNodeNotSellable).Snapshot()
	mutations := map[string]func() Snapshot{
		"bad listing id":                 func() Snapshot { s := fed; s.ID = ""; return s },
		"bad item id":                    func() Snapshot { s := fed; s.ItemID = ""; return s },
		"bad node id":                    func() Snapshot { s := fed; s.NodeID = "x y"; return s },
		"unknown state":                  func() Snapshot { s := fed; s.State = "GONE"; return s },
		"live with a reason":             func() Snapshot { s := fed; s.Reason = ReasonDiscontinued; return s },
		"suppressed without a reason":    func() Snapshot { s := sup; s.Reason = ""; return s },
		"suppressed with unknown reason": func() Snapshot { s := sup; s.Reason = "X"; return s },
		"negative quantity":              func() Snapshot { s := fed; s.Qty = -1; return s },
		"quantity above the bound":       func() Snapshot { s := fed; s.Qty = MaxQty + 1; return s },
		"unknown basis":                  func() Snapshot { s := fed; s.Basis = "X"; return s },
		"asOf without a feed":            func() Snapshot { s := fed; s.FeedID = ""; return s },
		"feed without an asOf":           func() Snapshot { s := fed; s.AsOf = time.Time{}; return s },
		"malformed feed id":              func() Snapshot { s := fed; s.FeedID = "feed 1"; return s },
		"never fed with a quantity":      func() Snapshot { s := fed; s.AsOf, s.FeedID, s.Basis = time.Time{}, "", BasisUnknown; return s },
		"never fed with a basis":         func() Snapshot { s := fed; s.AsOf, s.FeedID, s.Qty = time.Time{}, "", 0; return s },
		"version zero":                   func() Snapshot { s := fed; s.Version = 0; return s },
	}
	for name, build := range mutations {
		if _, err := Restore(build()); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: want ErrInvalidSnapshot, got %v", name, err)
		}
	}
}

func TestRestoreAcceptsTheBoundaries(t *testing.T) {
	fed := fedListing(t, 5, BasisPhysical).Snapshot()
	good := map[string]Snapshot{}
	s := fed
	s.Qty = 0
	good["zero quantity"] = s
	s = fed
	s.Qty = MaxQty
	good["maximum quantity"] = s
	s = fed
	s.Version = 1
	good["version one"] = s
	for name, snap := range good {
		if _, err := Restore(snap); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
