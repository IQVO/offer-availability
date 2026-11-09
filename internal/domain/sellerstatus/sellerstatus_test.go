package sellerstatus

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var (
	t0 = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)
	t1 = t0.Add(time.Hour)
)

func viewWith(t *testing.T, status Status, at time.Time) *View {
	t.Helper()
	v, err := New("seller-1", status, at)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

func TestNew(t *testing.T) {
	v, err := New("seller-1", StatusSuspended, t0.In(time.FixedZone("X", 3600)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v.ID() != "seller-1" || v.Status() != StatusSuspended || !v.AsOf().Equal(t0) || v.AsOf().Location() != time.UTC ||
		v.Version() != 1 || !v.Suspended() {
		t.Fatalf("view = %+v", v.Snapshot())
	}
	if viewWith(t, StatusActive, t0).Suspended() {
		t.Fatal("an ACTIVE seller is not suspended")
	}
	bad := []struct {
		id     ID
		status Status
		at     time.Time
	}{{"", StatusActive, t0}, {"seller 1", StatusActive, t0}, {"seller-1", "GONE", t0}, {"seller-1", "", t0}, {"seller-1", StatusActive, time.Time{}}}
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
		wantAt     time.Time
		wantVer    int64
	}{
		{"active to suspended", StatusActive, t0, StatusSuspended, t1, Changed, StatusSuspended, t1, 2},
		{"suspended to active", StatusSuspended, t0, StatusActive, t1, Changed, StatusActive, t1, 2},
		{"same instant is in order", StatusActive, t1, StatusSuspended, t1, Changed, StatusSuspended, t1, 2},
		{"older than asOf is ignored", StatusSuspended, t1, StatusActive, t0, Ignored, StatusSuspended, t1, 1},
		{"older by a microsecond is ignored", StatusActive, t1, StatusSuspended, t1.Add(-time.Microsecond), Ignored, StatusActive, t1, 1},
		{"repeat of suspended", StatusSuspended, t0, StatusSuspended, t1, Repeated, StatusSuspended, t0, 1},
		{"repeat of active", StatusActive, t0, StatusActive, t1, Repeated, StatusActive, t0, 1},
		{"older repeat is ignored", StatusActive, t1, StatusActive, t0, Ignored, StatusActive, t1, 1},
	}
	for _, c := range cases {
		v := viewWith(t, c.from, c.fromAt)
		got, err := v.Observe(c.status, c.at)
		if err != nil || got != c.want {
			t.Errorf("%s: outcome %v err %v, want %v", c.name, got, err, c.want)
		}
		if v.Status() != c.wantStatus || !v.AsOf().Equal(c.wantAt) || v.Version() != c.wantVer {
			t.Errorf("%s: view %+v", c.name, v.Snapshot())
		}
	}
}

func TestObserveRejectsMalformedObservations(t *testing.T) {
	for _, c := range []struct {
		status Status
		at     time.Time
	}{{"", t1}, {"GONE", t1}, {StatusActive, time.Time{}}} {
		v := viewWith(t, StatusActive, t0)
		before := v.Snapshot()
		if out, err := v.Observe(c.status, c.at); !errors.Is(err, shared.ErrValidation) || out != Ignored {
			t.Errorf("%+v: outcome %v err %v", c, out, err)
		}
		if v.Snapshot() != before {
			t.Errorf("%+v: view changed to %+v", c, v.Snapshot())
		}
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	v := viewWith(t, StatusActive, t0)
	if _, err := v.Observe(StatusSuspended, t1); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(v.Snapshot())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if r.Snapshot() != v.Snapshot() || r.Snapshot() != (Snapshot{ID: "seller-1", Status: StatusSuspended, AsOf: t1, Version: 2}) {
		t.Fatalf("round trip %+v vs %+v", r.Snapshot(), v.Snapshot())
	}
	local, err := Restore(Snapshot{ID: "seller-1", Status: StatusActive, AsOf: t0.In(time.FixedZone("X", 7200)), Version: 1})
	if err != nil || local.AsOf().Location() != time.UTC {
		t.Fatalf("Restore must normalise to UTC: %v %v", err, local)
	}
}

func TestRestoreRejectsCorruptRows(t *testing.T) {
	good := Snapshot{ID: "seller-1", Status: StatusActive, AsOf: t0, Version: 1}
	bad := []Snapshot{
		{ID: "", Status: good.Status, AsOf: good.AsOf, Version: 1},
		{ID: good.ID, Status: "GONE", AsOf: good.AsOf, Version: 1},
		{ID: good.ID, Status: good.Status, Version: 1},
		{ID: good.ID, Status: good.Status, AsOf: good.AsOf, Version: 0},
	}
	for _, s := range bad {
		if _, err := Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%+v: want ErrInvalidSnapshot, got %v", s, err)
		}
	}
	if _, err := Restore(good); err != nil {
		t.Fatalf("good snapshot: %v", err)
	}
}
