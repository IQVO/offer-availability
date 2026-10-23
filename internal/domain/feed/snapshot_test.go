package feed

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func TestRestoreRejectsCorruptRows(t *testing.T) {
	applied := stateApplied(t).Snapshot()
	failed := stateFailed(t).Snapshot()
	received := stateReceived(t).Snapshot()
	mutations := map[string]func() Snapshot{
		"bad feed id":             func() Snapshot { s := applied; s.ID = ""; return s },
		"bad node id":             func() Snapshot { s := applied; s.NodeID = ""; return s },
		"unknown state":           func() Snapshot { s := applied; s.State = "DONE"; return s },
		"zero asOf":               func() Snapshot { s := applied; s.AsOf = time.Time{}; return s },
		"zero receivedAt":         func() Snapshot { s := applied; s.ReceivedAt = time.Time{}; return s },
		"negative line count":     func() Snapshot { s := applied; s.LineCount = -1; return s },
		"too many lines":          func() Snapshot { s := applied; s.LineCount = MaxLines + 1; return s },
		"partial feed, no lines":  func() Snapshot { s := applied; s.LineCount, s.AppliedCount = 0, 0; return s },
		"version zero":            func() Snapshot { s := applied; s.Version = 0; return s },
		"negative applied":        func() Snapshot { s := applied; s.AppliedCount = -1; return s },
		"negative stale":          func() Snapshot { s := applied; s.StaleCount = -1; return s },
		"negative zeroed":         func() Snapshot { s := applied; s.FullUpdate, s.ZeroedCount = true, -1; return s },
		"counters above lines":    func() Snapshot { s := applied; s.AppliedCount, s.StaleCount = 1, 1; return s },
		"received with counters":  func() Snapshot { s := received; s.AppliedCount = 1; return s },
		"failed with stale":       func() Snapshot { s := failed; s.StaleCount = 1; return s },
		"failed with zeroed":      func() Snapshot { s := failed; s.FullUpdate, s.ZeroedCount = true, 1; return s },
		"partial feed zeroed":     func() Snapshot { s := applied; s.ZeroedCount = 1; return s },
		"applied without time":    func() Snapshot { s := applied; s.AppliedAt = time.Time{}; return s },
		"received with appliedAt": func() Snapshot { s := received; s.AppliedAt = t2; return s },
		"failed without a code":   func() Snapshot { s := failed; s.FailureCode = ""; return s },
		"failed with a bad code":  func() Snapshot { s := failed; s.FailureCode = "ErrBoom"; return s },
		"applied with a code":     func() Snapshot { s := applied; s.FailureCode = CodeListingNotOnNode; return s },
	}
	for name, build := range mutations {
		if _, err := Restore(build()); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: want ErrInvalidSnapshot, got %v", name, err)
		}
	}
}

func TestRestoreAcceptsTheBoundaries(t *testing.T) {
	applied := stateApplied(t).Snapshot()
	good := map[string]func() Snapshot{
		"all lines applied":  func() Snapshot { s := applied; s.LineCount, s.AppliedCount = 3, 3; return s },
		"applied plus stale": func() Snapshot { s := applied; s.LineCount, s.AppliedCount, s.StaleCount = 3, 2, 1; return s },
		"all lines stale":    func() Snapshot { s := applied; s.AppliedCount, s.StaleCount = 0, 1; return s },
		"maximum lines":      func() Snapshot { s := applied; s.LineCount = MaxLines; return s },
		"full feed without line": func() Snapshot {
			s := applied
			s.FullUpdate, s.LineCount, s.AppliedCount, s.ZeroedCount = true, 0, 0, 5
			return s
		},
		"version one":          func() Snapshot { s := stateReceived(t).Snapshot(); s.Version = 1; return s },
		"negative-qty failure": func() Snapshot { s := stateFailed(t).Snapshot(); s.FailureCode = CodeNegativeAvailability; return s },
	}
	for name, build := range good {
		if _, err := Restore(build()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
