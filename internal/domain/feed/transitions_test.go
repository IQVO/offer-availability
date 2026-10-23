package feed

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func stateReceived(t *testing.T) *InventoryFeed {
	t.Helper()
	return receivedFeed(t, mustSubmission(t, false, t1, line("lst-a", 1, listing.BasisPhysical)))
}

func stateApplied(t *testing.T) *InventoryFeed {
	t.Helper()
	f := stateReceived(t)
	if _, err := f.MarkApplied(Result{Applied: 1}, t2); err != nil {
		t.Fatal(err)
	}
	return f
}

func stateFailed(t *testing.T) *InventoryFeed {
	t.Helper()
	f := stateReceived(t)
	if err := f.MarkFailed(ErrListingNotOnNode); err != nil {
		t.Fatal(err)
	}
	return f
}

func stateSuperseded(t *testing.T) *InventoryFeed {
	t.Helper()
	f := stateReceived(t)
	if err := f.MarkSuperseded(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMarkApplied(t *testing.T) {
	f := stateReceived(t)
	events, err := f.MarkApplied(Result{Applied: 1, Stale: 0, Zeroed: 3}, t2.In(time.FixedZone("X", 3600)))
	if err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	assertAppliedSnapshot(t, f.Snapshot())
	assertFeedAppliedEvent(t, events)
}

func assertAppliedSnapshot(t *testing.T, snap Snapshot) {
	t.Helper()
	if snap.State != StateApplied || snap.AppliedCount != 1 || snap.StaleCount != 0 || snap.ZeroedCount != 3 || !snap.AppliedAt.Equal(t2) || snap.Version != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func assertFeedAppliedEvent(t *testing.T, events []shared.Event) {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev, ok := events[0].(InventoryFeedApplied)
	want := InventoryFeedApplied{Header: shared.Header{Version: 2, At: t2}, FeedID: "feed-1", NodeID: "node-1", FullUpdate: false, LineCount: 1, AppliedAt: t2}
	if !ok || ev != want {
		t.Fatalf("event = %#v, want %#v", events[0], want)
	}
	if ev.EventName() != "InventoryFeedApplied" || ev.Entity() != "inventoryfeed" || ev.SubjectID() != "feed-1" ||
		ev.AggregateVersion() != 2 || !ev.OccurredAt().Equal(t2) {
		t.Fatalf("event accessors wrong: %+v", ev)
	}
}

func TestMarkFailedRecordsTheBrokenInvariant(t *testing.T) {
	cases := []struct {
		cause error
		code  string
	}{
		{fmt.Errorf("line 3: %w", ErrListingNotOnNode), "ErrListingNotOnNode"},
		{fmt.Errorf("line 3: %w", listing.ErrNegativeAvailability), "ErrNegativeAvailability"},
	}
	for _, c := range cases {
		f := stateReceived(t)
		if err := f.MarkFailed(c.cause); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
		if f.State() != StateFailed || f.FailureCode() != c.code || f.Version() != 2 {
			t.Errorf("state %s code %q version %d", f.State(), f.FailureCode(), f.Version())
		}
	}
	f := stateReceived(t)
	before := f.Snapshot()
	for _, cause := range []error{nil, errors.New("boom"), listing.ErrStaleAvailability} {
		if err := f.MarkFailed(cause); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("cause %v: want ErrValidation, got %v", cause, err)
		}
	}
	if f.Snapshot() != before {
		t.Fatal("a refused MarkFailed changed the feed")
	}
}

func TestMarkSuperseded(t *testing.T) {
	f := stateSuperseded(t)
	if f.State() != StateSuperseded || f.Version() != 2 {
		t.Fatalf("state %s version %d", f.State(), f.Version())
	}
}

func TestTerminalAndFailedFeedsRefuseTheRemainingTransitions(t *testing.T) {
	builds := map[string]func(*testing.T) *InventoryFeed{"applied": stateApplied, "failed": stateFailed, "superseded": stateSuperseded}
	for name, build := range builds {
		f := build(t)
		before := f.Snapshot()
		if _, err := f.MarkApplied(Result{}, t3); !errors.Is(err, shared.ErrIllegalTransition) {
			t.Errorf("%s: MarkApplied want ErrIllegalTransition, got %v", name, err)
		}
		if err := f.MarkFailed(ErrListingNotOnNode); !errors.Is(err, shared.ErrIllegalTransition) {
			t.Errorf("%s: MarkFailed want ErrIllegalTransition, got %v", name, err)
		}
		if err := f.MarkSuperseded(); !errors.Is(err, shared.ErrIllegalTransition) {
			t.Errorf("%s: MarkSuperseded want ErrIllegalTransition, got %v", name, err)
		}
		if f.Snapshot() != before {
			t.Errorf("%s: a refused transition changed the feed", name)
		}
	}
}

func TestResubmit(t *testing.T) {
	corrected := mustSubmission(t, true, t3, line("lst-a", 4, listing.BasisPhysical), line("lst-b", 5, listing.BasisPhysical))
	f := stateFailed(t)
	if err := f.Resubmit(corrected, t3.In(time.FixedZone("X", 3600))); err != nil {
		t.Fatalf("Resubmit: %v", err)
	}
	want := Snapshot{
		ID: "feed-1", NodeID: "node-1", FullUpdate: true, AsOf: t3, LineCount: 2, State: StateReceived,
		ReceivedAt: t3, Version: 3,
	}
	if f.Snapshot() != want {
		t.Fatalf("snapshot = %+v, want %+v", f.Snapshot(), want)
	}
}

func TestResubmitRefusals(t *testing.T) {
	sub := mustSubmission(t, false, t3, line("lst-a", 4, listing.BasisPhysical))
	otherNode, err := NewSubmission("feed-1", "node-2", false, t3, sub.Lines)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := NewSubmission("feed-2", "node-1", false, t3, sub.Lines)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		build func(*testing.T) *InventoryFeed
		sub   Submission
		want  error
	}{
		{"applied", stateApplied, sub, ErrFeedAlreadyApplied},
		{"superseded", stateSuperseded, sub, ErrFeedAlreadyApplied},
		{"received", stateReceived, sub, shared.ErrIllegalTransition},
		{"failed for another node", stateFailed, otherNode, shared.ErrValidation},
		{"failed with another id", stateFailed, otherID, shared.ErrValidation},
	}
	for _, c := range cases {
		f := c.build(t)
		before := f.Snapshot()
		if err := f.Resubmit(c.sub, t3); !errors.Is(err, c.want) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, err)
		}
		if f.Snapshot() != before {
			t.Errorf("%s: a refused Resubmit changed the feed", c.name)
		}
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	failed := stateFailed(t)
	applied := stateApplied(t)
	full := receivedFeed(t, mustSubmission(t, true, t1))
	if _, err := full.MarkApplied(Result{Zeroed: 4}, t2); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]*InventoryFeed{"received": stateReceived(t), "applied": applied, "failed": failed, "superseded": stateSuperseded(t), "full zeroed": full} {
		snap := f.Snapshot()
		back, err := Restore(snap)
		if err != nil {
			t.Fatalf("%s: Restore: %v", name, err)
		}
		if back.Snapshot() != snap {
			t.Errorf("%s: round trip %+v != %+v", name, back.Snapshot(), snap)
		}
	}
}
