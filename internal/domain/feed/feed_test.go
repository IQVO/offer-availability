package feed

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var (
	t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
	t2 = t0.Add(2 * time.Hour)
	t3 = t0.Add(3 * time.Hour)
)

// newListing builds a LIVE listing lst-<n> on node, optionally already fed.
func newListing(t *testing.T, id, node string, qty int, basis listing.Basis, asOf time.Time) *listing.Listing {
	t.Helper()
	l, _, err := listing.Create(listing.ID(id), "item-1", listing.NodeID(node), listing.Sellability{NodeActive: true}, t0)
	if err != nil {
		t.Fatalf("listing.Create: %v", err)
	}
	if !asOf.IsZero() {
		if _, err := l.ApplyAvailability(qty, basis, asOf, "feed-0", asOf); err != nil {
			t.Fatalf("seed availability: %v", err)
		}
	}
	return l
}

func index(ls ...*listing.Listing) map[listing.ID]*listing.Listing {
	m := make(map[listing.ID]*listing.Listing, len(ls))
	for _, l := range ls {
		m[l.ID()] = l
	}
	return m
}

func line(id string, qty int, basis listing.Basis) Line {
	return Line{ListingID: listing.ID(id), Qty: qty, Basis: basis}
}

func mustSubmission(t *testing.T, full bool, asOf time.Time, lines ...Line) Submission {
	t.Helper()
	sub, err := NewSubmission("feed-1", "node-1", full, asOf, lines)
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	return sub
}

func receivedFeed(t *testing.T, sub Submission) *InventoryFeed {
	t.Helper()
	return Receive(sub, t0)
}

func TestNewSubmissionAcceptsAWellFormedFeed(t *testing.T) {
	lines := []Line{line("lst-1", 5, listing.BasisPhysical), line("lst-2", 0, listing.BasisUnknown)}
	sub, err := NewSubmission("feed-1", "node-1", false, t1.In(time.FixedZone("X", 3600)), lines)
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if sub.ID != "feed-1" || sub.NodeID != "node-1" || sub.Full || !sub.AsOf.Equal(t1) || sub.AsOf.Location() != time.UTC || len(sub.Lines) != 2 {
		t.Fatalf("submission = %+v", sub)
	}
	lines[0].Qty = 99
	if sub.Lines[0].Qty != 5 {
		t.Fatal("the submission must copy the lines")
	}
}

func TestNewSubmissionRejects(t *testing.T) {
	ok := []Line{line("lst-1", 5, listing.BasisPhysical)}
	manyLines := func(n int) []Line {
		ls := make([]Line, n)
		for i := range ls {
			ls[i] = line("lst-"+strings.Repeat("a", 1)+string(rune('a'+i%26))+strings.Repeat("b", i/26), 1, listing.BasisPhysical)
		}
		return ls
	}
	cases := []struct {
		name  string
		id    ID
		node  listing.NodeID
		full  bool
		asOf  time.Time
		lines []Line
	}{
		{"bad feed id", "", "node-1", false, t1, ok},
		{"bad node id", "feed-1", "", false, t1, ok},
		{"zero asOf", "feed-1", "node-1", false, time.Time{}, ok},
		{"too many lines", "feed-1", "node-1", true, t1, manyLines(MaxLines + 1)},
		{"partial without lines", "feed-1", "node-1", false, t1, nil},
		{"bad listing id", "feed-1", "node-1", false, t1, []Line{line("", 1, listing.BasisPhysical)}},
		{"bad basis", "feed-1", "node-1", false, t1, []Line{line("lst-1", 1, "SOME")}},
		{"quantity above the bound", "feed-1", "node-1", false, t1, []Line{line("lst-1", listing.MaxQty+1, listing.BasisPhysical)}},
		{"duplicate listing", "feed-1", "node-1", false, t1, []Line{line("lst-1", 1, listing.BasisPhysical), line("lst-1", 2, listing.BasisPhysical)}},
	}
	for _, c := range cases {
		if _, err := NewSubmission(c.id, c.node, c.full, c.asOf, c.lines); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", c.name, err)
		}
	}
}

func TestNewSubmissionBoundaries(t *testing.T) {
	if _, err := NewSubmission("feed-1", "node-1", true, t1, nil); err != nil {
		t.Errorf("a full feed may have no line: %v", err)
	}
	if _, err := NewSubmission("feed-1", "node-1", false, t1, []Line{line("lst-1", -7, listing.BasisPhysical)}); err != nil {
		t.Errorf("a negative quantity is not a submission error, it fails the feed: %v", err)
	}
	if _, err := NewSubmission("feed-1", "node-1", false, t1, []Line{line("lst-1", listing.MaxQty, listing.BasisPhysical)}); err != nil {
		t.Errorf("the maximum quantity is valid: %v", err)
	}
	lines := make([]Line, MaxLines)
	for i := range lines {
		lines[i] = Line{ListingID: listing.ID("l" + itoa(i)), Qty: 1, Basis: listing.BasisPhysical}
	}
	if _, err := NewSubmission("feed-1", "node-1", false, t1, lines); err != nil {
		t.Errorf("exactly %d lines are valid: %v", MaxLines, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestSuperseded(t *testing.T) {
	cases := []struct {
		name        string
		asOf, last  time.Time
		wantIgnored bool
	}{
		{"no feed applied yet", t1, time.Time{}, false},
		{"older than the last applied", t1, t2, true},
		{"one microsecond older", t2.Add(-time.Microsecond), t2, true},
		{"equal to the last applied", t2, t2, false},
		{"newer", t3, t2, false},
	}
	for _, c := range cases {
		if got := Superseded(c.asOf, c.last); got != c.wantIgnored {
			t.Errorf("%s: Superseded = %v, want %v", c.name, got, c.wantIgnored)
		}
	}
}

func TestStateValidity(t *testing.T) {
	for _, s := range []State{StateReceived, StateApplied, StateFailed, StateSuperseded} {
		if !s.Valid() {
			t.Errorf("%s must be valid", s)
		}
	}
	if State("").Valid() || State("DONE").Valid() || !ID("a").Valid() || ID("").Valid() {
		t.Fatal("validity wrong")
	}
}

func TestReceive(t *testing.T) {
	sub := mustSubmission(t, true, t1, line("lst-1", 1, listing.BasisPhysical))
	f := Receive(sub, t0.In(time.FixedZone("X", 3600)))
	snap := f.Snapshot()
	want := Snapshot{
		ID: "feed-1", NodeID: "node-1", FullUpdate: true, AsOf: t1, LineCount: 1, State: StateReceived,
		ReceivedAt: t0, Version: 1,
	}
	if snap != want {
		t.Fatalf("snapshot = %+v, want %+v", snap, want)
	}
	if f.ID() != "feed-1" || f.NodeID() != "node-1" || !f.Full() || !f.AsOf().Equal(t1) || f.LineCount() != 1 ||
		f.State() != StateReceived || f.FailureCode() != "" || f.Version() != 1 {
		t.Fatalf("accessors wrong: %+v", snap)
	}
}
