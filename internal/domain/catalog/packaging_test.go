package catalog_test

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

var packNow = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

const (
	gEach   = "06141410000120"
	gInner  = "06141410000298"
	gCase   = "06141410000366"
	gPallet = "06141410000434"
	gSpare  = "06141410000502"
)

func gtin(t *testing.T, s string) gs1.GTIN {
	t.Helper()
	g, err := catalog.ParseGTIN(s)
	if err != nil {
		t.Fatalf("ParseGTIN(%s): %v", s, err)
	}
	return g
}

func newPackItem(t *testing.T) *catalog.Item {
	t.Helper()
	it, _, err := catalog.Create("item-1", catalog.Spec{Title: "Jar", LengthMm: 10, WidthMm: 10, HeightMm: 10, WeightG: 10}, packNow)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func spec(t *testing.T, g, child string, units int) catalog.PackSpec {
	t.Helper()
	s := catalog.PackSpec{GTIN: gtin(t, g), UnitsPerPack: units}
	if child != "" {
		s.ChildGTIN = gtin(t, child)
	}
	return s
}

// chain builds EACH(1) -> INNER(6) -> CASE(24) -> PALLET(480).
func chain(t *testing.T) *catalog.Item {
	t.Helper()
	it := newPackItem(t)
	for _, st := range []struct {
		l     catalog.PackLevel
		g, c  string
		units int
	}{
		{catalog.PackLevelEach, gEach, "", 1},
		{catalog.PackLevelInner, gInner, gEach, 6},
		{catalog.PackLevelCase, gCase, gInner, 24},
		{catalog.PackLevelPallet, gPallet, gCase, 480},
	} {
		if _, err := it.DeclarePackLevel(st.l, spec(t, st.g, st.c, st.units), packNow); err != nil {
			t.Fatalf("declare %s: %v", st.l, err)
		}
	}
	return it
}

func TestParseGTIN(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{gEach, true}, {"6141410000120", true}, {"06141410000121", false}, {"123", false}, {"", false},
		{"96141410000115", false}, // indicator digit 9: variable measure
	} {
		_, err := catalog.ParseGTIN(tc.raw)
		if (err == nil) != tc.ok {
			t.Errorf("ParseGTIN(%q) err=%v, want ok=%v", tc.raw, err, tc.ok)
		}
		if err != nil && !errors.Is(err, catalog.ErrInvalidGTIN) {
			t.Errorf("ParseGTIN(%q) err=%v is not ErrInvalidGTIN", tc.raw, err)
		}
	}
}

func TestPackLevelOrder(t *testing.T) {
	ranked := []catalog.PackLevel{catalog.PackLevelEach, catalog.PackLevelInner, catalog.PackLevelCase, catalog.PackLevelPallet}
	for a, la := range ranked {
		for b, lb := range ranked {
			if got := la.Below(lb); got != (a < b) {
				t.Errorf("%s.Below(%s) = %v", la, lb, got)
			}
		}
		if catalog.PackLevel("BOX").Below(la) || la.Below("BOX") {
			t.Errorf("an unknown level is below nothing and above nothing")
		}
	}
	if _, err := catalog.ParsePackLevel("each"); !errors.Is(err, catalog.ErrInvalidPackLevel) {
		t.Errorf("lower case level: %v", err)
	}
}

func TestDeclareChainEmitsEventsAndBumpsVersion(t *testing.T) {
	it := newPackItem(t)
	ev, err := it.DeclarePackLevel(catalog.PackLevelEach, spec(t, gEach, "", 1), packNow)
	if err != nil || len(ev) != 1 || it.Version() != 2 {
		t.Fatalf("each: ev=%v err=%v version=%d", ev, err, it.Version())
	}
	d := ev[0].(catalog.CatalogItemPackagingDeclared)
	if d.UnitsPerPack != 1 || d.Level != catalog.PackLevelEach || d.EventName() != "CatalogItemPackagingDeclared" || d.SubjectID() != "item-1" || d.Entity() != "catalogitem" {
		t.Fatalf("event = %+v", d)
	}
	full := chain(t)
	if got := len(full.Packaging()); got != 4 || full.Version() != 5 {
		t.Fatalf("levels=%d version=%d", got, full.Version())
	}
	order := full.Packaging()
	for i := 1; i < len(order); i++ {
		if !order[i-1].Level.Below(order[i].Level) {
			t.Fatalf("not lowest first: %v", order)
		}
	}
}

func TestDeclareRejections(t *testing.T) {
	cases := []struct {
		name  string
		level catalog.PackLevel
		spec  func(*testing.T) catalog.PackSpec
		want  error
	}{
		{"unknown level", "BOX", func(t *testing.T) catalog.PackSpec { return spec(t, gEach, "", 1) }, catalog.ErrInvalidPackLevel},
		{"no gtin", catalog.PackLevelEach, func(*testing.T) catalog.PackSpec { return catalog.PackSpec{UnitsPerPack: 1} }, catalog.ErrInvalidGTIN},
		{"each with 2 units", catalog.PackLevelEach, func(t *testing.T) catalog.PackSpec { return spec(t, gEach, "", 2) }, catalog.ErrInvalidPackQuantity},
		{"each with a child", catalog.PackLevelEach, func(t *testing.T) catalog.PackSpec { return spec(t, gEach, gSpare, 1) }, catalog.ErrInvalidPackQuantity},
		{"units zero", catalog.PackLevelEach, func(t *testing.T) catalog.PackSpec { return spec(t, gEach, "", 0) }, catalog.ErrInvalidPackQuantity},
		{"units over max", catalog.PackLevelCase, func(t *testing.T) catalog.PackSpec { return spec(t, gCase, gEach, catalog.MaxPackQuantity+1) }, catalog.ErrInvalidPackQuantity},
		{"case with 1 unit", catalog.PackLevelCase, func(t *testing.T) catalog.PackSpec { return spec(t, gCase, gEach, 1) }, catalog.ErrInvalidPackQuantity},
		{"case without child", catalog.PackLevelCase, func(t *testing.T) catalog.PackSpec { return spec(t, gCase, "", 6) }, catalog.ErrPackLevelOrder},
		{"child not declared", catalog.PackLevelCase, func(t *testing.T) catalog.PackSpec { return spec(t, gCase, gSpare, 6) }, catalog.ErrPackLevelOrder},
		{"partial dims", catalog.PackLevelEach, func(t *testing.T) catalog.PackSpec { s := spec(t, gEach, "", 1); s.LengthMm = 5; return s }, catalog.ErrInvalidPackDimensions},
		{"negative weight", catalog.PackLevelEach, func(t *testing.T) catalog.PackSpec { s := spec(t, gEach, "", 1); s.GrossWeightG = -1; return s }, catalog.ErrInvalidPackDimensions},
		{"negative dim", catalog.PackLevelEach, func(t *testing.T) catalog.PackSpec {
			s := spec(t, gEach, "", 1)
			s.LengthMm, s.WidthMm, s.HeightMm = -1, 1, 1
			return s
		}, catalog.ErrInvalidPackDimensions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := newPackItem(t)
			v := it.Version()
			ev, err := it.DeclarePackLevel(tc.level, tc.spec(t), packNow)
			if !errors.Is(err, tc.want) || len(ev) != 0 || it.Version() != v || len(it.Packaging()) != 0 {
				t.Fatalf("err=%v ev=%v version=%d, want %v and no change", err, ev, it.Version(), tc.want)
			}
		})
	}
}

func TestDeclareChainMath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units int
		ok    bool
	}{
		{"exactly twice the child", 12, true},
		{"equal to the child", 6, false},
		{"not a multiple", 13, false},
		{"just below twice", 11, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := newPackItem(t)
			mustDeclare(t, it, catalog.PackLevelEach, spec(t, gEach, "", 1))
			mustDeclare(t, it, catalog.PackLevelInner, spec(t, gInner, gEach, 6))
			_, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gCase, gInner, tc.units), packNow)
			if (err == nil) != tc.ok {
				t.Fatalf("units %d: err=%v want ok=%v", tc.units, err, tc.ok)
			}
			if err != nil && !errors.Is(err, catalog.ErrInvalidPackQuantity) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// the child must be LOWER than the level
	it := newPackItem(t)
	mustDeclare(t, it, catalog.PackLevelEach, spec(t, gEach, "", 1))
	mustDeclare(t, it, catalog.PackLevelCase, spec(t, gCase, gEach, 24))
	if _, err := it.DeclarePackLevel(catalog.PackLevelInner, spec(t, gInner, gCase, 48), packNow); !errors.Is(err, catalog.ErrPackLevelOrder) {
		t.Fatalf("child above level: %v", err)
	}
	if _, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gInner, gInner, 48), packNow); !errors.Is(err, catalog.ErrPackLevelOrder) {
		t.Fatalf("self as child: %v", err)
	}
}

func mustDeclare(t *testing.T, it *catalog.Item, l catalog.PackLevel, s catalog.PackSpec) []shared.Event {
	t.Helper()
	ev, err := it.DeclarePackLevel(l, s, packNow)
	if err != nil {
		t.Fatalf("declare %s: %v", l, err)
	}
	return ev
}

func TestUnitsPerPackIsImmutablePerGTIN(t *testing.T) {
	it := chain(t)
	v := it.Version()
	_, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gCase, gInner, 48), packNow)
	if !errors.Is(err, catalog.ErrPackQuantityImmutable) || it.Version() != v {
		t.Fatalf("changed units: %v", err)
	}
	_, err = it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gCase, gEach, 24), packNow)
	if !errors.Is(err, catalog.ErrPackQuantityImmutable) {
		t.Fatalf("changed child: %v", err)
	}
	// the identical declaration is a no-op that raises nothing
	ev, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gCase, gInner, 24), packNow)
	if err != nil || len(ev) != 0 || it.Version() != v {
		t.Fatalf("identical redeclare: ev=%v err=%v version=%d", ev, err, it.Version())
	}
}

func TestRedeclareUpdatesDimensionsInPlace(t *testing.T) {
	it := chain(t)
	s := spec(t, gCase, gInner, 24)
	s.LengthMm, s.WidthMm, s.HeightMm, s.GrossWeightG = 400, 300, 200, 5000
	v := it.Version()
	ev, err := it.DeclarePackLevel(catalog.PackLevelCase, s, packNow)
	if err != nil || len(ev) != 1 || it.Version() != v+1 {
		t.Fatalf("ev=%v err=%v version=%d", ev, err, it.Version())
	}
	var got catalog.PackLevelDef
	for _, d := range it.Packaging() {
		if d.Level == catalog.PackLevelCase {
			got = d
		}
	}
	if got.LengthMm != 400 || got.GrossWeightG != 5000 || !got.Active {
		t.Fatalf("case = %+v", got)
	}
}

func TestGrossWeightMustCoverContents(t *testing.T) {
	it := newPackItem(t)
	e := spec(t, gEach, "", 1)
	e.GrossWeightG = 100
	mustDeclare(t, it, catalog.PackLevelEach, e)
	c := spec(t, gInner, gEach, 6)
	c.GrossWeightG = 599
	if _, err := it.DeclarePackLevel(catalog.PackLevelInner, c, packNow); !errors.Is(err, catalog.ErrInvalidPackDimensions) {
		t.Fatalf("599 g for 6 x 100 g: %v", err)
	}
	c.GrossWeightG = 600
	mustDeclare(t, it, catalog.PackLevelInner, c)
	// the weight check also runs when the dimensions of a held GTIN change
	c.GrossWeightG = 599
	if _, err := it.DeclarePackLevel(catalog.PackLevelInner, c, packNow); !errors.Is(err, catalog.ErrInvalidPackDimensions) {
		t.Fatalf("redeclare under contents: %v", err)
	}
}

func TestNewGTINSupersedesTheActiveOneInOneVersion(t *testing.T) {
	it := chain(t)
	v := it.Version()
	ev, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gSpare, gInner, 12), packNow)
	if err != nil || len(ev) != 2 || it.Version() != v+1 {
		t.Fatalf("ev=%v err=%v version=%d (was %d)", ev, err, it.Version(), v)
	}
	r, ok := ev[0].(catalog.CatalogItemPackLevelRetired)
	if !ok || r.GTIN != gtin(t, gCase) || r.Level != catalog.PackLevelCase || r.EventName() != "CatalogItemPackLevelRetired" || r.SubjectID() != "item-1" || r.Entity() != "catalogitem" {
		t.Fatalf("first event = %#v", ev[0])
	}
	if d, ok := ev[1].(catalog.CatalogItemPackagingDeclared); !ok || d.GTIN != gtin(t, gSpare) || d.UnitsPerPack != 12 {
		t.Fatalf("second event = %#v", ev[1])
	}
	if ev[0].AggregateVersion() != ev[1].AggregateVersion() {
		t.Fatalf("both events carry one version")
	}
	assertSupersededKept(t, it)
}

func assertSupersededKept(t *testing.T, it *catalog.Item) {
	t.Helper()
	// the old GTIN is kept but retired and can never be reused
	if _, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gCase, gInner, 24), packNow); !errors.Is(err, catalog.ErrGtinAlreadyAssigned) {
		t.Fatalf("reuse of a retired GTIN: %v", err)
	}
	if got := len(it.Packaging()); got != 5 {
		t.Fatalf("history kept: %d levels", got)
	}
}

func TestGTINBelongsToOneLevel(t *testing.T) {
	it := chain(t)
	if _, err := it.DeclarePackLevel(catalog.PackLevelInner, spec(t, gCase, gEach, 6), packNow); !errors.Is(err, catalog.ErrGtinAlreadyAssigned) {
		t.Fatalf("case GTIN at the inner level: %v", err)
	}
}

func TestRetirePackLevel(t *testing.T) {
	it := chain(t)
	v := it.Version()
	ev, err := it.RetirePackLevel(catalog.PackLevelPallet, packNow.Add(time.Hour))
	if err != nil || len(ev) != 1 || it.Version() != v+1 {
		t.Fatalf("ev=%v err=%v", ev, err)
	}
	r := ev[0].(catalog.CatalogItemPackLevelRetired)
	if !r.RetiredAt.Equal(packNow.Add(time.Hour)) || r.GTIN != gtin(t, gPallet) {
		t.Fatalf("event = %+v", r)
	}
	// retiring again is a no-op, a level never declared is not found
	if ev, err := it.RetirePackLevel(catalog.PackLevelPallet, packNow); err != nil || len(ev) != 0 || it.Version() != v+1 {
		t.Fatalf("again: ev=%v err=%v", ev, err)
	}
	empty := newPackItem(t)
	if _, err := empty.RetirePackLevel(catalog.PackLevelCase, packNow); !errors.Is(err, catalog.ErrPackLevelNotFound) {
		t.Fatalf("never declared: %v", err)
	}
	if _, err := empty.RetirePackLevel("BOX", packNow); !errors.Is(err, catalog.ErrInvalidPackLevel) {
		t.Fatalf("bad level: %v", err)
	}
	// a retired child cannot anchor a new level
	if _, err := it.DeclarePackLevel(catalog.PackLevelPallet, spec(t, gSpare, gCase, 480), packNow); err != nil {
		t.Fatalf("case is still active: %v", err)
	}
	_, _ = it.RetirePackLevel(catalog.PackLevelInner, packNow)
	if _, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, "06141410000571", gInner, 12), packNow); !errors.Is(err, catalog.ErrPackLevelOrder) {
		t.Fatalf("retired child: %v", err)
	}
}

func TestDiscontinuedItemTakesNoPackagingChange(t *testing.T) {
	it := chain(t)
	it.Discontinue(packNow)
	if _, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gSpare, gInner, 12), packNow); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("declare: %v", err)
	}
}

func TestPackagingSurvivesSnapshotRestore(t *testing.T) {
	it := chain(t)
	_, _ = it.RetirePackLevel(catalog.PackLevelPallet, packNow)
	back, err := catalog.RestorePackaged(it.Snapshot(), it.PackLevels())
	if err != nil {
		t.Fatal(err)
	}
	if len(back.PackLevels()) != 4 || back.Version() != it.Version() {
		t.Fatalf("restored: %v", back.PackLevels())
	}
	bad := it.PackLevels()
	bad = append(bad, bad[0])
	if _, err := catalog.RestorePackaged(it.Snapshot(), bad); !errors.Is(err, shared.ErrInvalidSnapshot) {
		t.Fatalf("duplicate GTIN: %v", err)
	}
	two := it.PackLevels()
	two[3].Active = true
	two = append(two, catalog.PackLevelDef{Level: catalog.PackLevelPallet, GTIN: gtin(t, gSpare), ChildGTIN: gtin(t, gCase), UnitsPerPack: 480, Active: true})
	if _, err := catalog.RestorePackaged(it.Snapshot(), two); !errors.Is(err, shared.ErrInvalidSnapshot) {
		t.Fatalf("two active pallets: %v", err)
	}
}

func TestItemGTIN(t *testing.T) {
	g := gtin(t, gEach)
	it, ev, err := catalog.Create("item-2", catalog.Spec{Title: "Jar", LengthMm: 1, WidthMm: 1, HeightMm: 1, WeightG: 1, GTIN: g}, packNow)
	if err != nil || it.GTIN() != g || it.Snapshot().GTIN != g {
		t.Fatalf("gtin lost: %v", err)
	}
	if ev[0].(catalog.CatalogItemCreated).GTIN != g {
		t.Fatalf("created event lacks the gtin")
	}
	back, _ := catalog.Restore(it.Snapshot())
	if back.GTIN() != g {
		t.Fatalf("restore lost the gtin")
	}
}

func TestGrossWeightScalesWithTheChildCount(t *testing.T) {
	it := newPackItem(t)
	e := spec(t, gEach, "", 1)
	e.GrossWeightG = 100
	mustDeclare(t, it, catalog.PackLevelEach, e)
	in := spec(t, gInner, gEach, 6)
	in.GrossWeightG = 600
	mustDeclare(t, it, catalog.PackLevelInner, in)
	c := spec(t, gCase, gInner, 24)
	c.GrossWeightG = 2399 // 4 inner packs of 600 g
	if _, err := it.DeclarePackLevel(catalog.PackLevelCase, c, packNow); !errors.Is(err, catalog.ErrInvalidPackDimensions) {
		t.Fatalf("2399 g: %v", err)
	}
	c.GrossWeightG = 2400
	mustDeclare(t, it, catalog.PackLevelCase, c)
	// a level without a weight is not checked against its contents
	p := spec(t, gPallet, gCase, 480)
	mustDeclare(t, it, catalog.PackLevelPallet, p)
}

func TestFirstDeclaredLevelIsFullyHandled(t *testing.T) {
	// EACH is declared first (index 0): redeclare, supersede and retire must find it.
	it := newPackItem(t)
	mustDeclare(t, it, catalog.PackLevelEach, spec(t, gEach, "", 1))
	d := spec(t, gEach, "", 1)
	d.LengthMm, d.WidthMm, d.HeightMm = 5, 5, 5
	if ev := mustDeclare(t, it, catalog.PackLevelEach, d); len(ev) != 1 {
		t.Fatalf("in-place update at index 0 raised %d events", len(ev))
	}
	if got := it.Packaging()[0]; got.LengthMm != 5 {
		t.Fatalf("not updated: %+v", got)
	}
	if ev := mustDeclare(t, it, catalog.PackLevelEach, spec(t, gSpare, "", 1)); len(ev) != 2 {
		t.Fatalf("superseding index 0 raised %d events", len(ev))
	}
	ev, err := it.RetirePackLevel(catalog.PackLevelEach, packNow)
	if err != nil || len(ev) != 1 {
		t.Fatalf("retire the first declared level: %v %v", ev, err)
	}
	// only retired history at the level: no change; a level the item never had: not found
	if ev, err := it.RetirePackLevel(catalog.PackLevelEach, packNow); err != nil || len(ev) != 0 {
		t.Fatalf("retired history: %v %v", ev, err)
	}
	if _, err := it.RetirePackLevel(catalog.PackLevelCase, packNow); !errors.Is(err, catalog.ErrPackLevelNotFound) {
		t.Fatalf("other levels exist but not this one: %v", err)
	}
}

func TestPackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*catalog.PackSpec)
		ok   bool
	}{
		{"max dimension", func(s *catalog.PackSpec) { s.LengthMm, s.WidthMm, s.HeightMm = catalog.MaxDimension, 1, 1 }, true},
		{"over max dimension", func(s *catalog.PackSpec) { s.LengthMm, s.WidthMm, s.HeightMm = catalog.MaxDimension+1, 1, 1 }, false},
		{"width over max", func(s *catalog.PackSpec) { s.LengthMm, s.WidthMm, s.HeightMm = 1, catalog.MaxDimension+1, 1 }, false},
		{"height over max", func(s *catalog.PackSpec) { s.LengthMm, s.WidthMm, s.HeightMm = 1, 1, catalog.MaxDimension+1 }, false},
		{"max weight", func(s *catalog.PackSpec) { s.GrossWeightG = catalog.MaxDimension }, true},
		{"over max weight", func(s *catalog.PackSpec) { s.GrossWeightG = catalog.MaxDimension + 1 }, false},
		{"smallest dims", func(s *catalog.PackSpec) { s.LengthMm, s.WidthMm, s.HeightMm = 1, 1, 1 }, true},
		{"width missing", func(s *catalog.PackSpec) { s.LengthMm, s.HeightMm = 1, 1 }, false},
		{"height missing", func(s *catalog.PackSpec) { s.LengthMm, s.WidthMm = 1, 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := spec(t, gEach, "", 1)
			tc.mod(&s)
			_, err := newPackItem(t).DeclarePackLevel(catalog.PackLevelEach, s, packNow)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	// units: exactly the maximum is fine on a multiple, one more is not
	it := newPackItem(t)
	mustDeclare(t, it, catalog.PackLevelEach, spec(t, gEach, "", 1))
	if _, err := it.DeclarePackLevel(catalog.PackLevelPallet, spec(t, gPallet, gEach, catalog.MaxPackQuantity), packNow); err != nil {
		t.Fatalf("max units: %v", err)
	}
	if _, err := it.DeclarePackLevel(catalog.PackLevelCase, spec(t, gCase, gEach, 2), packNow); err != nil {
		t.Fatalf("two eaches is the smallest case: %v", err)
	}
}
