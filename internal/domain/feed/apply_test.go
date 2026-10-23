package feed

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func changedIDs(res Result) []listing.ID {
	ids := make([]listing.ID, len(res.Changed))
	for i, l := range res.Changed {
		ids[i] = l.ID()
	}
	return ids
}

func assertCounters(t *testing.T, res Result, applied, stale, zeroed int) {
	t.Helper()
	if res.Applied != applied || res.Stale != stale || res.Zeroed != zeroed {
		t.Fatalf("counters applied/stale/zeroed = %d/%d/%d, want %d/%d/%d", res.Applied, res.Stale, res.Zeroed, applied, stale, zeroed)
	}
	if len(res.Events) != len(res.Changed) || len(res.Changed) != applied+zeroed {
		t.Fatalf("events %d changed %d, want %d each", len(res.Events), len(res.Changed), applied+zeroed)
	}
}

func assertAvailability(t *testing.T, l *listing.Listing, qty int, basis listing.Basis, asOf time.Time, feedID string) {
	t.Helper()
	want := listing.Availability{Qty: qty, Basis: basis, AsOf: asOf, FeedID: feedID}
	if l.Availability() != want {
		t.Fatalf("listing %s availability = %+v, want %+v", l.ID(), l.Availability(), want)
	}
}

func TestApplyPartialFeed(t *testing.T) {
	a := newListing(t, "lst-a", "node-1", 1, listing.BasisPhysical, t0)
	b := newListing(t, "lst-b", "node-1", 2, listing.BasisPhysical, t0)
	other := newListing(t, "lst-c", "node-1", 3, listing.BasisPhysical, t0)
	sub := mustSubmission(t, false, t2, line("lst-b", 20, listing.BasisThroughputConstrained), line("lst-a", 0, listing.BasisPhysical))
	f := receivedFeed(t, sub)
	res, err := f.Apply(sub, index(a, b, other), t3)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCounters(t, res, 2, 0, 0)
	if ids := changedIDs(res); ids[0] != "lst-b" || ids[1] != "lst-a" {
		t.Fatalf("changed in line order, got %v", ids)
	}
	assertAvailability(t, a, 0, listing.BasisPhysical, t2, "feed-1")
	assertAvailability(t, b, 20, listing.BasisThroughputConstrained, t2, "feed-1")
	assertAvailability(t, other, 3, listing.BasisPhysical, t0, "feed-0")
	ev, ok := res.Events[0].(listing.ListingAvailabilityChanged)
	if !ok || ev.ListingID != "lst-b" || !ev.OccurredAt().Equal(t3) {
		t.Fatalf("first event = %#v", res.Events[0])
	}
	if f.State() != StateReceived {
		t.Fatal("Apply must not change the feed itself")
	}
}

func TestApplySkipsAndCountsStaleLines(t *testing.T) {
	fresh := newListing(t, "lst-a", "node-1", 1, listing.BasisPhysical, t0)
	newer := newListing(t, "lst-b", "node-1", 2, listing.BasisPhysical, t2)
	same := newListing(t, "lst-c", "node-1", 3, listing.BasisPhysical, t2)
	sub := mustSubmission(t, false, t2, line("lst-a", 10, listing.BasisPhysical), line("lst-b", 20, listing.BasisPhysical), line("lst-c", 30, listing.BasisPhysical))
	res, err := receivedFeed(t, sub).Apply(sub, index(fresh, newer, same), t3)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCounters(t, res, 1, 2, 0)
	assertAvailability(t, newer, 2, listing.BasisPhysical, t2, "feed-0")
	assertAvailability(t, same, 3, listing.BasisPhysical, t2, "feed-0")
}

func TestApplyOfAnAllStaleFeedAppliesNothing(t *testing.T) {
	a := newListing(t, "lst-a", "node-1", 1, listing.BasisPhysical, t2)
	sub := mustSubmission(t, false, t1, line("lst-a", 10, listing.BasisPhysical))
	res, err := receivedFeed(t, sub).Apply(sub, index(a), t3)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCounters(t, res, 0, 1, 0)
	if res.Events != nil || res.Changed != nil {
		t.Fatalf("nothing changed, got %v %v", res.Events, res.Changed)
	}
}

func TestApplyFailsOnTheFirstOffendingLine(t *testing.T) {
	a := newListing(t, "lst-a", "node-1", 1, listing.BasisPhysical, t0)
	b := newListing(t, "lst-b", "node-1", 2, listing.BasisPhysical, t0)
	foreign := newListing(t, "lst-x", "node-9", 2, listing.BasisPhysical, t0)
	cases := []struct {
		name  string
		lines []Line
		want  error
	}{
		{"unknown listing", []Line{line("lst-a", 5, listing.BasisPhysical), line("lst-zzz", 5, listing.BasisPhysical)}, ErrListingNotOnNode},
		{"listing of another node", []Line{line("lst-x", 5, listing.BasisPhysical)}, ErrListingNotOnNode},
		{"negative quantity", []Line{line("lst-a", 5, listing.BasisPhysical), line("lst-b", -1, listing.BasisPhysical)}, listing.ErrNegativeAvailability},
		{"not on node first", []Line{line("lst-zzz", -1, listing.BasisPhysical)}, ErrListingNotOnNode},
		{"negative first then unknown", []Line{line("lst-a", -1, listing.BasisPhysical), line("lst-zzz", 1, listing.BasisPhysical)}, listing.ErrNegativeAvailability},
		{"negative stale line still fails", []Line{line("lst-a", -1, listing.BasisPhysical)}, listing.ErrNegativeAvailability},
	}
	for _, c := range cases {
		sub := mustSubmission(t, false, t1, c.lines...)
		before := []listing.Snapshot{a.Snapshot(), b.Snapshot()}
		res, err := receivedFeed(t, sub).Apply(sub, index(a, b, foreign), t3)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
		if res.Events != nil || res.Changed != nil {
			t.Errorf("%s: a failed feed returns no result", c.name)
		}
		if a.Snapshot() != before[0] || b.Snapshot() != before[1] {
			t.Errorf("%s: a failed feed must not touch any listing", c.name)
		}
	}
}

func TestFullFeedZeroesOmittedListings(t *testing.T) {
	covered := newListing(t, "lst-b", "node-1", 5, listing.BasisPhysical, t0)
	omitted2 := newListing(t, "lst-d", "node-1", 7, listing.BasisThroughputConstrained, t0)
	omitted1 := newListing(t, "lst-c", "node-1", 9, listing.BasisPhysical, t0)
	zero := newListing(t, "lst-e", "node-1", 0, listing.BasisPhysical, t0)
	neverFed := newListing(t, "lst-f", "node-1", 0, listing.BasisUnknown, time.Time{})
	newer := newListing(t, "lst-g", "node-1", 4, listing.BasisPhysical, t2)
	foreign := newListing(t, "lst-x", "node-9", 4, listing.BasisPhysical, t0)
	sub := mustSubmission(t, true, t2, line("lst-b", 50, listing.BasisPhysical))
	res, err := receivedFeed(t, sub).Apply(sub, index(covered, omitted2, omitted1, zero, neverFed, newer, foreign), t3)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertCounters(t, res, 1, 0, 2)
	if ids := changedIDs(res); ids[0] != "lst-b" || ids[1] != "lst-c" || ids[2] != "lst-d" {
		t.Fatalf("changed order: lines first, then zeroed by id; got %v", ids)
	}
	assertAvailability(t, covered, 50, listing.BasisPhysical, t2, "feed-1")
	assertAvailability(t, omitted1, 0, listing.BasisPhysical, t2, "feed-1")
	assertAvailability(t, omitted2, 0, listing.BasisThroughputConstrained, t2, "feed-1")
	assertAvailability(t, zero, 0, listing.BasisPhysical, t0, "feed-0")
	assertAvailability(t, newer, 4, listing.BasisPhysical, t2, "feed-0")
	assertAvailability(t, foreign, 4, listing.BasisPhysical, t0, "feed-0")
	if neverFed.Version() != 1 {
		t.Fatal("a never-fed listing must stay untouched")
	}
}

func TestPartialFeedNeverZeroes(t *testing.T) {
	covered := newListing(t, "lst-a", "node-1", 5, listing.BasisPhysical, t0)
	omitted := newListing(t, "lst-b", "node-1", 9, listing.BasisPhysical, t0)
	sub := mustSubmission(t, false, t2, line("lst-a", 6, listing.BasisPhysical))
	res, err := receivedFeed(t, sub).Apply(sub, index(covered, omitted), t3)
	if err != nil {
		t.Fatal(err)
	}
	assertCounters(t, res, 1, 0, 0)
	assertAvailability(t, omitted, 9, listing.BasisPhysical, t0, "feed-0")
}

func TestEmptyFullFeedZeroesTheWholeNode(t *testing.T) {
	a := newListing(t, "lst-a", "node-1", 5, listing.BasisPhysical, t0)
	sub := mustSubmission(t, true, t2)
	res, err := receivedFeed(t, sub).Apply(sub, index(a), t3)
	if err != nil {
		t.Fatal(err)
	}
	assertCounters(t, res, 0, 0, 1)
}

func TestApplyGuards(t *testing.T) {
	a := newListing(t, "lst-a", "node-1", 1, listing.BasisPhysical, t0)
	sub := mustSubmission(t, false, t2, line("lst-a", 10, listing.BasisPhysical))
	otherAsOf := mustSubmission(t, false, t3, line("lst-a", 10, listing.BasisPhysical))
	otherLines := mustSubmission(t, false, t2, line("lst-a", 10, listing.BasisPhysical), line("lst-b", 1, listing.BasisPhysical))
	fullVsPartial := mustSubmission(t, true, t2, line("lst-a", 10, listing.BasisPhysical))
	otherNode, err := NewSubmission("feed-1", "node-2", false, t2, sub.Lines)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := NewSubmission("feed-2", "node-1", false, t2, sub.Lines)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Submission{"asOf": otherAsOf, "line count": otherLines, "full flag": fullVsPartial, "node": otherNode, "id": otherID} {
		if _, err := receivedFeed(t, sub).Apply(s, index(a), t3); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("a submission with another %s: want ErrValidation, got %v", name, err)
		}
	}
	done := receivedFeed(t, sub)
	if err := done.MarkSuperseded(); err != nil {
		t.Fatal(err)
	}
	if _, err := done.Apply(sub, index(a), t3); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("Apply on a SUPERSEDED feed: want ErrIllegalTransition, got %v", err)
	}
}

// A Submission built by hand, bypassing NewSubmission, must still be refused by
// Apply instead of corrupting a listing.
func TestApplyRefusesAHandBuiltSubmission(t *testing.T) {
	a := newListing(t, "lst-a", "node-1", 5, listing.BasisPhysical, t0)
	badBasis := Submission{ID: "feed-1", NodeID: "node-1", AsOf: t2, Lines: []Line{{ListingID: "lst-a", Qty: 1, Basis: "SOME"}}}
	if _, err := Receive(badBasis, t0).Apply(badBasis, index(a), t3); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("a line with an unknown basis: want ErrValidation, got %v", err)
	}
	noAsOf := Submission{ID: "feed-1", NodeID: "node-1", Full: true}
	if _, err := Receive(noAsOf, t0).Apply(noAsOf, index(a), t3); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("a full feed without asOf: want ErrValidation, got %v", err)
	}
}
