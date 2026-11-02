package feed

import (
	"errors"
	"fmt"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// Restore rebuilds a persisted feed, validating every invariant.
func Restore(s Snapshot) (*InventoryFeed, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("feed %q: %v", s.ID, err)
	}
	return &InventoryFeed{
		id: s.ID, node: s.NodeID, full: s.FullUpdate, asOf: shared.UTC(s.AsOf), lineCount: s.LineCount,
		state: s.State, applied: s.AppliedCount, stale: s.StaleCount, zeroed: s.ZeroedCount, failure: s.FailureCode,
		receivedAt: shared.UTC(s.ReceivedAt), appliedAt: shared.UTC(s.AppliedAt), version: s.Version,
	}, nil
}

func checkSnapshot(s Snapshot) error {
	if !s.ID.Valid() || !s.NodeID.Valid() {
		return errors.New("feedId and nodeId must be valid ids")
	}
	if !s.State.Valid() {
		return fmt.Errorf("state %q is unknown", s.State)
	}
	if s.AsOf.IsZero() || s.ReceivedAt.IsZero() {
		return errors.New("asOf and receivedAt are required")
	}
	if s.LineCount < 0 || s.LineCount > MaxLines || (s.LineCount == 0 && !s.FullUpdate) {
		return errors.New("lineCount is out of range")
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	return checkOutcome(s)
}

// checkOutcome checks the counters, failure code and appliedAt against the state.
func checkOutcome(s Snapshot) error {
	counters := s.AppliedCount + s.StaleCount
	if s.AppliedCount < 0 || s.StaleCount < 0 || s.ZeroedCount < 0 || counters > s.LineCount {
		return errors.New("counters are out of range")
	}
	if s.State != StateApplied && (counters != 0 || s.ZeroedCount != 0) {
		return errors.New("only an APPLIED feed has counters")
	}
	if s.ZeroedCount > 0 && !s.FullUpdate {
		return errors.New("only a full feed zeroes listings")
	}
	if (s.State == StateApplied) == s.AppliedAt.IsZero() {
		return errors.New("appliedAt is set exactly when the feed is APPLIED")
	}
	validCode := s.FailureCode == CodeListingNotOnNode || s.FailureCode == CodeNegativeAvailability
	if (s.State == StateFailed) != (s.FailureCode != "") || (s.State == StateFailed && !validCode) {
		return errors.New("a failure code is set exactly when the feed is FAILED")
	}
	return nil
}
