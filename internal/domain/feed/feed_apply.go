package feed

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// Result is what applying a feed to its listings produced.
type Result struct {
	// Events are the ListingAvailabilityChanged events, in processing order.
	Events []shared.Event
	// Changed are the listings that must be saved, in processing order.
	Changed []*listing.Listing
	// Applied counts lines that replaced a listing's availability.
	Applied int
	// Stale counts lines skipped because their asOf was not after the listing's.
	Stale int
	// Zeroed counts listings set to 0 by FullFeedZeroing.
	Zeroed int
}

// Apply evaluates the submission against the listings the use case loaded
// (every listing the lines name and, for a full feed, every listing of the
// node). It validates every line first and returns the first offending line:
// ErrListingNotOnNode, or listing.ErrNegativeAvailability. Nothing is applied
// then. Otherwise each line is applied (a stale line is skipped and counted) and,
// for a full feed, the omitted listings of the node are zeroed (A10). The feed
// itself is not changed: call MarkApplied or MarkFailed. After an error the
// loaded listings must be discarded, never saved.
func (f *InventoryFeed) Apply(sub Submission, listings map[listing.ID]*listing.Listing, now time.Time) (Result, error) {
	if err := f.requireReceived(); err != nil {
		return Result{}, err
	}
	if !f.matches(sub) {
		return Result{}, shared.Invalid("submission does not match feed %s", f.id)
	}
	if err := f.validateLines(sub, listings); err != nil {
		return Result{}, err
	}
	var res Result
	covered := make(map[listing.ID]struct{}, len(sub.Lines))
	for _, line := range sub.Lines {
		covered[line.ListingID] = struct{}{}
		l := listings[line.ListingID]
		events, err := l.ApplyAvailability(line.Qty, line.Basis, sub.AsOf, string(f.id), now)
		if errors.Is(err, listing.ErrStaleAvailability) {
			res.Stale++
			continue
		}
		if err != nil {
			return Result{}, err
		}
		res.Applied++
		res.Events = append(res.Events, events...)
		res.Changed = append(res.Changed, l)
	}
	if f.full {
		if err := f.zeroOmitted(sub, listings, covered, now, &res); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// matches reports whether sub is the submission this feed recorded.
func (f *InventoryFeed) matches(sub Submission) bool {
	return sub.ID == f.id && sub.NodeID == f.node && sub.Full == f.full &&
		len(sub.Lines) == f.lineCount && sub.AsOf.Equal(f.asOf)
}

// validateLines checks every line before anything is applied: it names a
// listing of the node (ErrListingNotOnNode) and its quantity is not negative.
func (f *InventoryFeed) validateLines(sub Submission, listings map[listing.ID]*listing.Listing) error {
	for _, line := range sub.Lines {
		l, ok := listings[line.ListingID]
		if !ok || l.NodeID() != f.node {
			return fmt.Errorf("%w: listing %s is not a listing of node %s", ErrListingNotOnNode, line.ListingID, f.node)
		}
		if line.Qty < 0 {
			return fmt.Errorf("%w: listing %s, availableQty %d", listing.ErrNegativeAvailability, line.ListingID, line.Qty)
		}
	}
	return nil
}

// zeroOmitted is FullFeedZeroing: it zeroes, in listing id order, every listing
// of the node that has no line.
func (f *InventoryFeed) zeroOmitted(sub Submission, listings map[listing.ID]*listing.Listing, covered map[listing.ID]struct{}, now time.Time, res *Result) error {
	omitted := make([]*listing.Listing, 0, len(listings))
	for id, l := range listings {
		if _, ok := covered[id]; !ok && l.NodeID() == f.node {
			omitted = append(omitted, l)
		}
	}
	slices.SortFunc(omitted, func(a, b *listing.Listing) int { return strings.Compare(string(a.ID()), string(b.ID())) })
	for _, l := range omitted {
		events, err := l.ZeroOut(sub.AsOf, string(f.id), now)
		if errors.Is(err, listing.ErrStaleAvailability) {
			continue
		}
		if err != nil {
			return err
		}
		if len(events) == 0 {
			continue
		}
		res.Zeroed++
		res.Events = append(res.Events, events...)
		res.Changed = append(res.Changed, l)
	}
	return nil
}
