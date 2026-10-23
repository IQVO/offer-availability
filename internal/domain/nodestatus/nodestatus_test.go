package nodestatus

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

func viewWith(t *testing.T, status Status, at time.Time) *View {
	t.Helper()
	v, err := New("node-1", status, at)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func TestNew(t *testing.T) {
	v, err := New("node-1", StatusProposed, t0.In(time.FixedZone("X", 3600)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v.ID() != "node-1" || v.Status() != StatusProposed || !v.StatusAt().Equal(t0) || v.StatusAt().Location() != time.UTC || v.Version() != 1 || v.Sellable() {
		t.Fatalf("view = %+v", v.Snapshot())
	}
	bad := []struct {
		id     ID
		status Status
		at     time.Time
	}{{"", StatusActive, t0}, {"node-1", "GONE", t0}, {"node-1", "", t0}, {"node-1", StatusActive, time.Time{}}}
	for _, c := range bad {
		if _, err := New(c.id, c.status, c.at); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%+v: want ErrValidation, got %v", c, err)
		}
	}
}

func TestObserveTable(t *testing.T) {
	cases := []struct {
		name       string
		from       Status
		fromAt     time.Time
		status     Status
		at         time.Time
		want       Outcome
		wantStatus Status
		wantSell   bool
		wantAt     time.Time
	}{
		{"proposed to active", StatusProposed, t0, StatusActive, t1, Changed, StatusActive, true, t1},
		{"active to suspended", StatusActive, t0, StatusSuspended, t1, Changed, StatusSuspended, false, t1},
		{"suspended to active", StatusSuspended, t0, StatusActive, t1, Changed, StatusActive, true, t1},
		{"active to retired", StatusActive, t0, StatusRetired, t1, Changed, StatusRetired, false, t1},
		{"suspended to retired", StatusSuspended, t0, StatusRetired, t1, Changed, StatusRetired, false, t1},
		{"same instant is in order", StatusActive, t1, StatusSuspended, t1, Changed, StatusSuspended, false, t1},
		{"older than the status is ignored", StatusSuspended, t1, StatusActive, t0, Ignored, StatusSuspended, false, t1},
		{"older by a microsecond is ignored", StatusActive, t1, StatusSuspended, t1.Add(-time.Microsecond), Ignored, StatusActive, true, t1},
		{"retired is terminal for activate", StatusRetired, t0, StatusActive, t2, Ignored, StatusRetired, false, t0},
		{"retired is terminal for suspend", StatusRetired, t0, StatusSuspended, t2, Ignored, StatusRetired, false, t0},
		{"repeat of active", StatusActive, t0, StatusActive, t1, Repeated, StatusActive, true, t0},
		{"repeat of suspended", StatusSuspended, t0, StatusSuspended, t1, Repeated, StatusSuspended, false, t0},
		{"repeat of retired", StatusRetired, t0, StatusRetired, t1, Repeated, StatusRetired, false, t0},
		{"older repeat is ignored", StatusActive, t1, StatusActive, t0, Ignored, StatusActive, true, t1},
	}
	for _, c := range cases {
		v := viewWith(t, c.from, c.fromAt)
		version := v.Version()
		got, err := v.Observe(c.status, c.at)
		if err != nil || got != c.want {
			t.Errorf("%s: outcome %v err %v, want %v", c.name, got, err, c.want)
		}
		if v.Status() != c.wantStatus || v.Sellable() != c.wantSell || !v.StatusAt().Equal(c.wantAt) {
			t.Errorf("%s: status %s sellable %v at %v", c.name, v.Status(), v.Sellable(), v.StatusAt())
		}
		wantVersion := version
		if c.want == Changed {
			wantVersion++
		}
		if v.Version() != wantVersion {
			t.Errorf("%s: version %d, want %d", c.name, v.Version(), wantVersion)
		}
	}
}

func TestObserveRejectsMalformedObservations(t *testing.T) {
	cases := []struct {
		status Status
		at     time.Time
	}{{StatusProposed, t1}, {"", t1}, {"GONE", t1}, {StatusActive, time.Time{}}}
	for _, c := range cases {
		v := viewWith(t, StatusSuspended, t0)
		before := v.Snapshot()
		if _, err := v.Observe(c.status, c.at); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%+v: want ErrValidation, got %v", c, err)
		}
		if v.Snapshot() != before {
			t.Errorf("%+v: a refused observation changed the view", c)
		}
	}
}

func TestStatusValidityAndSellable(t *testing.T) {
	for _, s := range []Status{StatusProposed, StatusActive, StatusSuspended, StatusRetired} {
		if !s.Valid() {
			t.Errorf("%s must be valid", s)
		}
		if got := viewWith(t, s, t0).Sellable(); got != (s == StatusActive) {
			t.Errorf("%s: Sellable = %v", s, got)
		}
	}
	if Status("").Valid() || Status("X").Valid() || !ID("a").Valid() || ID("").Valid() {
		t.Fatal("validity wrong")
	}
}

func TestSnapshotRoundTripAndRestore(t *testing.T) {
	snap := viewWith(t, StatusSuspended, t1).Snapshot()
	back, err := Restore(snap)
	if err != nil || back.Snapshot() != snap {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	snap.StatusAt = snap.StatusAt.In(time.FixedZone("X", 3600))
	if back, err = Restore(snap); err != nil || back.StatusAt().Location() != time.UTC {
		t.Fatalf("Restore must normalise to UTC: %v %v", back, err)
	}
	good := viewWith(t, StatusActive, t0).Snapshot()
	mutations := map[string]func(*Snapshot){
		"bad id":         func(s *Snapshot) { s.ID = "" },
		"unknown status": func(s *Snapshot) { s.Status = "GONE" },
		"no instant":     func(s *Snapshot) { s.StatusAt = time.Time{} },
		"version zero":   func(s *Snapshot) { s.Version = 0 },
	}
	for name, mutate := range mutations {
		s := good
		mutate(&s)
		if _, err := Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: want ErrInvalidSnapshot, got %v", name, err)
		}
	}
}
