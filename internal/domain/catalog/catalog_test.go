package catalog

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

func goodSpec() Spec {
	return Spec{Title: "Water bottle 750 ml", LengthMm: 270, WidthMm: 75, HeightMm: 75, WeightG: 340, Hazmat: true, Fragile: true}
}

func mustCreate(t *testing.T) *Item {
	t.Helper()
	it, _, err := Create("item-1", goodSpec(), t0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return it
}

func TestCreateRaisesCatalogItemCreated(t *testing.T) {
	it, events, err := Create("item-1", goodSpec(), t0.In(time.FixedZone("X", 3600)))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if it.ID() != "item-1" || it.State() != StateActive || it.Discontinued() || it.Version() != 1 || it.Spec() != goodSpec() {
		t.Fatalf("unexpected item %+v", it.Snapshot())
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev, ok := events[0].(CatalogItemCreated)
	if !ok {
		t.Fatalf("event is %T", events[0])
	}
	want := CatalogItemCreated{
		Header:        shared.Header{Version: 1, At: t0},
		CatalogItemID: "item-1", Title: "Water bottle 750 ml",
		LengthMm: 270, WidthMm: 75, HeightMm: 75, WeightG: 340, Hazmat: true, Fragile: true,
	}
	if ev != want {
		t.Fatalf("event = %+v, want %+v", ev, want)
	}
	if ev.EventName() != "CatalogItemCreated" || ev.Entity() != "catalogitem" || ev.SubjectID() != "item-1" ||
		ev.AggregateVersion() != 1 || !ev.OccurredAt().Equal(t0) {
		t.Fatalf("event accessors wrong: %+v", ev)
	}
}

func TestCreateDimensionBounds(t *testing.T) {
	fields := map[string]func(*Spec, int){
		"lengthMm": func(s *Spec, v int) { s.LengthMm = v },
		"widthMm":  func(s *Spec, v int) { s.WidthMm = v },
		"heightMm": func(s *Spec, v int) { s.HeightMm = v },
		"weightG":  func(s *Spec, v int) { s.WeightG = v },
	}
	values := []struct {
		v  int
		ok bool
	}{{-1, false}, {0, false}, {1, true}, {MaxDimension, true}, {MaxDimension + 1, false}}
	for name, set := range fields {
		for _, c := range values {
			spec := goodSpec()
			set(&spec, c.v)
			_, _, err := Create("item-1", spec, t0)
			if c.ok && err != nil {
				t.Errorf("%s=%d: unexpected error %v", name, c.v, err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidItemDimensions) {
				t.Errorf("%s=%d: want ErrInvalidItemDimensions, got %v", name, c.v, err)
			}
			if !c.ok && err != nil && !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%d: error %q does not name the field", name, c.v, err)
			}
		}
	}
}

func TestCreateTextAndIDValidation(t *testing.T) {
	cases := []struct {
		name  string
		id    ID
		title string
	}{
		{"empty id", "", "x"},
		{"id with space", "item 1", "x"},
		{"empty title", "item-1", ""},
		{"blank title", "item-1", "   "},
		{"untrimmed title", "item-1", " x"},
		{"control character", "item-1", "a\tb"},
		{"title too long", "item-1", strings.Repeat("x", MaxTitle+1)},
	}
	for _, c := range cases {
		spec := goodSpec()
		spec.Title = c.title
		if _, _, err := Create(c.id, spec, t0); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", c.name, err)
		}
	}
	spec := goodSpec()
	spec.Title = strings.Repeat("é", MaxTitle)
	if _, _, err := Create("item-1", spec, t0); err != nil {
		t.Fatalf("a title of exactly %d characters is valid: %v", MaxTitle, err)
	}
}

func TestDiscontinueTransition(t *testing.T) {
	it := mustCreate(t)
	events := it.Discontinue(t0.Add(time.Hour))
	if !it.Discontinued() || it.State() != StateDiscontinued || it.Version() != 2 {
		t.Fatalf("after discontinue: %+v", it.Snapshot())
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev, ok := events[0].(CatalogItemDiscontinued)
	if !ok || ev.CatalogItemID != "item-1" || ev.AggregateVersion() != 2 || !ev.OccurredAt().Equal(t0.Add(time.Hour)) {
		t.Fatalf("event = %#v", events[0])
	}
	if ev.EventName() != "CatalogItemDiscontinued" || ev.Entity() != "catalogitem" || ev.SubjectID() != "item-1" {
		t.Fatalf("event accessors wrong: %+v", ev)
	}
}

func TestDiscontinueTwiceIsANoOp(t *testing.T) {
	it := mustCreate(t)
	it.Discontinue(t0)
	if events := it.Discontinue(t0.Add(time.Hour)); events != nil {
		t.Fatalf("second discontinue raised %v", events)
	}
	if it.Version() != 2 || !it.Discontinued() {
		t.Fatalf("second discontinue changed the item: %+v", it.Snapshot())
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	it := mustCreate(t)
	it.Discontinue(t0)
	snap := it.Snapshot()
	back, err := Restore(snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if back.Snapshot() != snap {
		t.Fatalf("round trip: %+v != %+v", back.Snapshot(), snap)
	}
}

func TestRestoreRejectsCorruptRows(t *testing.T) {
	base := mustCreate(t).Snapshot()
	mutations := map[string]func(*Snapshot){
		"bad id":           func(s *Snapshot) { s.ID = "" },
		"unknown state":    func(s *Snapshot) { s.State = "GONE" },
		"empty state":      func(s *Snapshot) { s.State = "" },
		"blank title":      func(s *Snapshot) { s.Title = "" },
		"zero length":      func(s *Snapshot) { s.LengthMm = 0 },
		"width too large":  func(s *Snapshot) { s.WidthMm = MaxDimension + 1 },
		"zero height":      func(s *Snapshot) { s.HeightMm = 0 },
		"zero weight":      func(s *Snapshot) { s.WeightG = 0 },
		"version zero":     func(s *Snapshot) { s.Version = 0 },
		"version negative": func(s *Snapshot) { s.Version = -1 },
	}
	for name, mutate := range mutations {
		s := base
		mutate(&s)
		if _, err := Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: want ErrInvalidSnapshot, got %v", name, err)
		}
	}
	for _, ok := range []State{StateActive, StateDiscontinued} {
		s := base
		s.State = ok
		s.Version = 1
		if _, err := Restore(s); err != nil {
			t.Errorf("state %s version 1 must restore: %v", ok, err)
		}
	}
}

func TestIDAndStateValidity(t *testing.T) {
	if !ID("item-1").Valid() || ID("").Valid() {
		t.Fatal("ID.Valid wrong")
	}
	if !StateActive.Valid() || !StateDiscontinued.Valid() || State("X").Valid() {
		t.Fatal("State.Valid wrong")
	}
}
