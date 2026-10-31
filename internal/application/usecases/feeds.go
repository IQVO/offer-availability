package usecases

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ApplyInventoryFeedCommand is the input of ApplyInventoryFeed.
type ApplyInventoryFeedCommand struct {
	FeedID     feed.ID
	NodeID     listing.NodeID
	FullUpdate bool
	AsOf       time.Time
	Lines      []feed.Line
}

// ApplyInventoryFeed applies one vendor inventory feed in ONE unit of work.
type ApplyInventoryFeed struct{ writer }

// NewApplyInventoryFeed builds the use case.
func NewApplyInventoryFeed(d Deps) *ApplyInventoryFeed { return &ApplyInventoryFeed{writer{d}} }

// outcome is what one attempt hands back: the feed as stored and, for a FAILED
// feed, the invariant that broke. A FAILED feed is committed (the row stays,
// nothing else changes), so the attempt returns nil to the unit of work and the
// failure is reported after the commit.
type outcome struct {
	view    views.InventoryFeed
	failure error
}

// Execute validates the request (a malformed request is shared.ErrValidation
// and records nothing), then, under the node's mark lock, records the feed
// RECEIVED and settles it (ADR 0002, A11):
//
//   - an APPLIED or SUPERSEDED feedId is feed.ErrFeedAlreadyApplied; a FAILED one
//     is evaluated again;
//   - a feed older than the last APPLIED feed of the node is SUPERSEDED;
//   - a line that names no listing of the node, or a negative quantity, makes
//     the feed FAILED: the returned view is the stored feed AND the error
//     (feed.ErrListingNotOnNode or listing.ErrNegativeAvailability) is returned
//     with it;
//   - otherwise the lines are applied (stale ones skipped and counted), a full
//     feed zeroes the omitted listings, every changed listing is saved in bulk,
//     the node's mark advances and the feed is APPLIED.
//
// The events leave in the order: one ListingAvailabilityChanged per changed
// listing, then InventoryFeedApplied.
func (uc *ApplyInventoryFeed) Execute(ctx context.Context, cmd ApplyInventoryFeedCommand) (views.InventoryFeed, error) {
	v, err := uc.execute(ctx, cmd)
	uc.recordOutcome(ctx, v, err)
	return v, err
}

func (uc *ApplyInventoryFeed) execute(ctx context.Context, cmd ApplyInventoryFeedCommand) (views.InventoryFeed, error) {
	sub, err := feed.NewSubmission(cmd.FeedID, cmd.NodeID, cmd.FullUpdate, cmd.AsOf, cmd.Lines)
	if err != nil {
		return views.InventoryFeed{}, err
	}
	out, err := transact(ctx, uc.UoW, func(ctx context.Context) (outcome, error) { return uc.attempt(ctx, sub) })
	if err != nil {
		return views.InventoryFeed{}, err
	}
	return out.view, out.failure
}

func (uc *ApplyInventoryFeed) attempt(ctx context.Context, sub feed.Submission) (outcome, error) {
	lastApplied, err := uc.Marks.Lock(ctx, sub.NodeID)
	if err != nil {
		return outcome{}, err
	}
	f, err := uc.receive(ctx, sub)
	if err != nil {
		return outcome{}, err
	}
	if feed.Superseded(sub.AsOf, lastApplied) {
		return uc.settle(ctx, f, f.MarkSuperseded())
	}
	listings, err := uc.loadListings(ctx, sub)
	if err != nil {
		return outcome{}, err
	}
	res, err := f.Apply(sub, listings, uc.now())
	if errors.Is(err, feed.ErrListingNotOnNode) || errors.Is(err, listing.ErrNegativeAvailability) {
		out, serr := uc.settle(ctx, f, f.MarkFailed(err))
		out.failure = err
		return out, serr
	}
	if err != nil {
		return outcome{}, err
	}
	return uc.commit(ctx, sub, f, res)
}

// receive records the submission as a RECEIVED feed: a new one, or a FAILED one
// sent again.
func (uc *ApplyInventoryFeed) receive(ctx context.Context, sub feed.Submission) (*feed.InventoryFeed, error) {
	f, err := uc.Feeds.Get(ctx, sub.ID)
	exists, err := found(err)
	if err != nil {
		return nil, err
	}
	if exists {
		err = f.Resubmit(sub, uc.now())
	} else {
		f = feed.Receive(sub, uc.now())
	}
	if err != nil {
		return nil, err
	}
	if err := uc.Feeds.Save(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// settle saves the feed after a transition (err is the transition's result) and
// returns its view.
func (uc *ApplyInventoryFeed) settle(ctx context.Context, f *feed.InventoryFeed, err error) (outcome, error) {
	if err != nil {
		return outcome{}, err
	}
	if err := uc.Feeds.Save(ctx, f); err != nil {
		return outcome{}, err
	}
	return outcome{view: views.InventoryFeedFrom(f)}, nil
}

// loadListings loads every listing the lines name and, for a full feed, every
// listing of the node.
func (uc *ApplyInventoryFeed) loadListings(ctx context.Context, sub feed.Submission) (map[listing.ID]*listing.Listing, error) {
	ids := make([]listing.ID, len(sub.Lines))
	for i, l := range sub.Lines {
		ids[i] = l.ListingID
	}
	loaded, err := uc.Listings.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	if sub.Full {
		more, err := uc.Listings.ListByNode(ctx, sub.NodeID)
		if err != nil {
			return nil, err
		}
		loaded = append(loaded, more...)
	}
	byID := make(map[listing.ID]*listing.Listing, len(loaded))
	for _, l := range loaded {
		byID[l.ID()] = l
	}
	return byID, nil
}

// commit settles an evaluated feed as APPLIED: listings, mark, feed and events.
func (uc *ApplyInventoryFeed) commit(ctx context.Context, sub feed.Submission, f *feed.InventoryFeed, res feed.Result) (outcome, error) {
	feedEvents, err := f.MarkApplied(res, uc.now())
	if err != nil {
		return outcome{}, err
	}
	if err := uc.Listings.SaveMany(ctx, res.Changed); err != nil {
		return outcome{}, err
	}
	if err := uc.Marks.Advance(ctx, sub.NodeID, sub.AsOf); err != nil {
		return outcome{}, err
	}
	if err := uc.Feeds.Save(ctx, f); err != nil {
		return outcome{}, err
	}
	events := append(append([]shared.Event(nil), res.Events...), feedEvents...)
	if err := uc.publish(ctx, events); err != nil {
		return outcome{}, err
	}
	return outcome{view: views.InventoryFeedFrom(f)}, nil
}
