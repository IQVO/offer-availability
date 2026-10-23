package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var (
	asOf1 = repoNow().Add(time.Hour)
	asOf2 = repoNow().Add(2 * time.Hour)
	asOf3 = repoNow().Add(3 * time.Hour)
)

func feedCmd(id string, full bool, asOf time.Time, lines ...feed.Line) usecases.ApplyInventoryFeedCommand {
	return usecases.ApplyInventoryFeedCommand{FeedID: feed.ID(id), NodeID: "node-1", FullUpdate: full, AsOf: asOf, Lines: lines}
}

func ln(id string, qty int) feed.Line {
	return feed.Line{ListingID: listing.ID(id), Qty: qty, Basis: listing.BasisPhysical}
}

// feedEnv has two listings on node-1 (lst-a fed 1 at repoNow, lst-b never fed) and one on node-2.
func feedEnv(t *testing.T) *env {
	e := newEnv(t)
	e.seedListing("lst-a", "item-1", "node-1", 1, repoNow())
	e.seedListing("lst-b", "item-2", "node-1", -1, repoNow())
	e.seedListing("lst-x", "item-1", "node-2", 4, repoNow())
	return e
}

func apply(e *env, cmd usecases.ApplyInventoryFeedCommand) (feedResult, error) {
	v, err := usecases.NewApplyInventoryFeed(e.deps).Execute(context.Background(), cmd)
	return feedResult{state: v.State, applied: v.AppliedCount, stale: v.StaleCount, zeroed: v.ZeroedCount, code: v.FailureCode, lines: v.LineCount}, err
}

type feedResult struct {
	state                  string
	applied, stale, zeroed int
	lines                  int
	code                   string
}

func TestApplyPartialFeed(t *testing.T) {
	e := feedEnv(t)
	got, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10), ln("lst-b", 20)))
	mustNot(t, err)
	if got != (feedResult{state: "APPLIED", applied: 2, lines: 2}) {
		t.Fatalf("result = %+v", got)
	}
	if e.listing("lst-a").Availability().Qty != 10 || e.listing("lst-b").Availability().Qty != 20 || e.listing("lst-x").Availability().Qty != 4 {
		t.Fatal("quantities not applied as expected")
	}
	e.assertEvents("ListingAvailabilityChanged", "ListingAvailabilityChanged", "InventoryFeedApplied")
	mark, err := e.store.Marks().Lock(context.Background(), "node-1")
	mustNot(t, err)
	if !mark.Equal(asOf1) {
		t.Fatalf("mark = %v, want %v", mark, asOf1)
	}
	stored, err := e.store.Feeds().Get(context.Background(), "feed-1")
	mustNot(t, err)
	if stored.State() != feed.StateApplied || stored.Version() != 2 {
		t.Fatalf("stored feed = %s v%d", stored.State(), stored.Version())
	}
	last, ok := e.events()[2].(feed.InventoryFeedApplied)
	if !ok || last.LineCount != 2 || last.FullUpdate || last.NodeID != "node-1" || !last.AppliedAt.Equal(repoNow()) {
		t.Fatalf("last event = %#v", e.events()[2])
	}
}

func TestApplyFeedSkipsAndCountsStaleLines(t *testing.T) {
	e := feedEnv(t)
	got, err := apply(e, feedCmd("feed-1", false, repoNow(), ln("lst-a", 99), ln("lst-b", 5)))
	mustNot(t, err)
	if got != (feedResult{state: "APPLIED", applied: 1, stale: 1, lines: 2}) {
		t.Fatalf("result = %+v", got)
	}
	if e.listing("lst-a").Availability().Qty != 1 {
		t.Fatal("a stale line must not change the listing")
	}
	e.assertEvents("ListingAvailabilityChanged", "InventoryFeedApplied")
}

func TestApplyFullFeedZeroesOmittedListings(t *testing.T) {
	e := feedEnv(t)
	got, err := apply(e, feedCmd("feed-1", true, asOf1, ln("lst-b", 7)))
	mustNot(t, err)
	if got != (feedResult{state: "APPLIED", applied: 1, zeroed: 1, lines: 1}) {
		t.Fatalf("result = %+v", got)
	}
	a := e.listing("lst-a").Availability()
	if a.Qty != 0 || !a.AsOf.Equal(asOf1) || a.FeedID != "feed-1" || a.Basis != listing.BasisPhysical {
		t.Fatalf("zeroed listing = %+v", a)
	}
	if e.listing("lst-x").Availability().Qty != 4 {
		t.Fatal("a listing of another node must not be zeroed")
	}
	e.assertEvents("ListingAvailabilityChanged", "ListingAvailabilityChanged", "InventoryFeedApplied")
}

func TestApplyEmptyFullFeedZeroesTheNode(t *testing.T) {
	e := feedEnv(t)
	got, err := apply(e, feedCmd("feed-1", true, asOf1))
	mustNot(t, err)
	if got.state != "APPLIED" || got.zeroed != 1 || got.lines != 0 {
		t.Fatalf("result = %+v", got)
	}
}

func TestApplyOlderFeedIsSuperseded(t *testing.T) {
	e := feedEnv(t)
	_, err := apply(e, feedCmd("feed-2", false, asOf2, ln("lst-a", 20)))
	mustNot(t, err)
	before := e.store.Counts()
	got, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	mustNot(t, err)
	if got.state != "SUPERSEDED" || got.applied != 0 {
		t.Fatalf("result = %+v", got)
	}
	if e.listing("lst-a").Availability().Qty != 20 {
		t.Fatal("a superseded feed must not change a listing")
	}
	after := e.store.Counts()
	if after.Feeds != before.Feeds+1 || after.OutboxEvents != before.OutboxEvents || after.Listings != before.Listings {
		t.Fatalf("counts before %+v after %+v", before, after)
	}
	again, err := apply(e, feedCmd("feed-1", false, asOf3, ln("lst-a", 10)))
	wantIs(t, err, feed.ErrFeedAlreadyApplied)
	if again.state != "" {
		t.Fatalf("an error returns no view, got %+v", again)
	}
}

func TestApplyFeedOfTheSameInstantIsNotSuperseded(t *testing.T) {
	e := feedEnv(t)
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	mustNot(t, err)
	got, err := apply(e, feedCmd("feed-2", false, asOf1, ln("lst-b", 3)))
	mustNot(t, err)
	if got.state != "APPLIED" || got.applied != 1 {
		t.Fatalf("result = %+v", got)
	}
}

func TestFailedFeedIsRecordedAndChangesNothingElse(t *testing.T) {
	cases := []struct {
		name  string
		lines []feed.Line
		want  error
		code  string
	}{
		{"listing of another node", []feed.Line{ln("lst-a", 5), ln("lst-x", 5)}, feed.ErrListingNotOnNode, "ErrListingNotOnNode"},
		{"unknown listing", []feed.Line{ln("lst-zzz", 5)}, feed.ErrListingNotOnNode, "ErrListingNotOnNode"},
		{"negative quantity", []feed.Line{ln("lst-a", 5), ln("lst-b", -1)}, listing.ErrNegativeAvailability, "ErrNegativeAvailability"},
	}
	for _, c := range cases {
		e := feedEnv(t)
		before := e.store.Counts()
		got, err := apply(e, feedCmd("feed-1", true, asOf1, c.lines...))
		wantIs(t, err, c.want)
		if got.state != "FAILED" || got.code != c.code || got.applied != 0 {
			t.Errorf("%s: result = %+v", c.name, got)
		}
		after := e.store.Counts()
		if after.Feeds != before.Feeds+1 || after.OutboxEvents != 0 || after.Listings != before.Listings || after.Marks != before.Marks {
			t.Errorf("%s: counts before %+v after %+v", c.name, before, after)
		}
		if e.listing("lst-a").Availability().Qty != 1 || e.listing("lst-a").Version() != 2 {
			t.Errorf("%s: a FAILED feed changed a listing", c.name)
		}
		stored, gerr := e.store.Feeds().Get(context.Background(), "feed-1")
		mustNot(t, gerr)
		if stored.State() != feed.StateFailed || stored.FailureCode() != c.code {
			t.Errorf("%s: stored feed = %s %s", c.name, stored.State(), stored.FailureCode())
		}
	}
}

func TestFailedFeedMayBeSentAgain(t *testing.T) {
	e := feedEnv(t)
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-zzz", 5)))
	wantIs(t, err, feed.ErrListingNotOnNode)
	got, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 5)))
	mustNot(t, err)
	if got.state != "APPLIED" || got.applied != 1 {
		t.Fatalf("result = %+v", got)
	}
	stored, err := e.store.Feeds().Get(context.Background(), "feed-1")
	mustNot(t, err)
	if stored.Version() != 4 {
		t.Fatalf("version = %d, want 4 (received, failed, received again, applied)", stored.Version())
	}
	_, err = apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 6)))
	wantIs(t, err, feed.ErrFeedAlreadyApplied)
	e.assertEvents("ListingAvailabilityChanged", "InventoryFeedApplied")
}

func TestResentFailedFeedCannotChangeNode(t *testing.T) {
	e := feedEnv(t)
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-zzz", 5)))
	wantIs(t, err, feed.ErrListingNotOnNode)
	cmd := feedCmd("feed-1", false, asOf1, ln("lst-x", 5))
	cmd.NodeID = "node-2"
	_, err = apply(e, cmd)
	wantIs(t, err, shared.ErrValidation)
}

func TestMalformedFeedRecordsNothing(t *testing.T) {
	cases := map[string]usecases.ApplyInventoryFeedCommand{
		"no lines on a partial feed": feedCmd("feed-1", false, asOf1),
		"duplicate listing":          feedCmd("feed-1", false, asOf1, ln("lst-a", 1), ln("lst-a", 2)),
		"bad feed id":                feedCmd("", false, asOf1, ln("lst-a", 1)),
		"no asOf":                    feedCmd("feed-1", false, time.Time{}, ln("lst-a", 1)),
	}
	for name, cmd := range cases {
		e := feedEnv(t)
		before := e.store.Counts()
		_, err := apply(e, cmd)
		wantIs(t, err, shared.ErrValidation)
		e.assertNothingStored(before)
		_ = name
	}
}

func TestApplyFeedInfrastructureFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*env)
	}{
		{"lock fails", func(e *env) { e.marks.lockErr = errBoom }},
		{"feed read fails", func(e *env) { e.feeds.getErr = errBoom }},
		{"first save fails", func(e *env) { e.feeds.saveErrs = []error{errBoom} }},
		{"final save fails", func(e *env) { e.feeds.saveErrs = []error{nil, errBoom} }},
		{"listing read fails", func(e *env) { e.listings.getManyErr = errBoom }},
		{"node listing read fails", func(e *env) { e.listings.byNodeErr = errBoom }},
		{"bulk save fails", func(e *env) { e.listings.saveManyErrs = []error{errBoom} }},
		{"mark advance fails", func(e *env) { e.marks.advanceErr = errBoom }},
		{"outbox fails", func(e *env) { e.outbox.err = errBoom }},
	}
	for _, c := range cases {
		e := feedEnv(t)
		c.setup(e)
		before := e.store.Counts()
		_, err := apply(e, feedCmd("feed-1", true, asOf1, ln("lst-a", 5)))
		wantIs(t, err, errBoom)
		e.assertNothingStored(before)
	}
}

func TestApplyFeedFailedAndSupersededSaveFailures(t *testing.T) {
	e := feedEnv(t)
	e.feeds.saveErrs = []error{nil, errBoom}
	before := e.store.Counts()
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-zzz", 5)))
	wantIs(t, err, errBoom)
	e.assertNothingStored(before)

	e = feedEnv(t)
	_, err = apply(e, feedCmd("feed-2", false, asOf2, ln("lst-a", 20)))
	mustNot(t, err)
	e.feeds.saves = 0
	e.feeds.saveErrs = []error{nil, errBoom}
	before = e.store.Counts()
	_, err = apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	wantIs(t, err, errBoom)
	e.assertNothingStored(before)
}

func TestApplyFeedRetriesOnceOnConcurrentUpdate(t *testing.T) {
	e := feedEnv(t)
	e.listings.saveManyErrs = []error{ports.ErrConcurrentUpdate}
	got, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	mustNot(t, err)
	if got.state != "APPLIED" {
		t.Fatalf("result = %+v", got)
	}
	e.assertEvents("ListingAvailabilityChanged", "InventoryFeedApplied")

	e = feedEnv(t)
	e.listings.saveManyErrs = []error{ports.ErrConcurrentUpdate, ports.ErrConcurrentUpdate}
	before := e.store.Counts()
	_, err = apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	wantIs(t, err, ports.ErrConcurrentUpdate)
	e.assertNothingStored(before)
}

func TestApplyFeedRefusesALargeFeedBeforeTouchingAnything(t *testing.T) {
	e := feedEnv(t)
	lines := make([]feed.Line, feed.MaxLines+1)
	for i := range lines {
		lines[i] = ln("lst-a", 1)
	}
	before := e.store.Counts()
	_, err := apply(e, feedCmd("feed-1", true, asOf1, lines...))
	wantIs(t, err, shared.ErrValidation)
	e.assertNothingStored(before)
}
