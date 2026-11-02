package usecases

import (
	"context"
	"errors"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// feedReasons maps a feed failure or refusal to the problem slug the REST
// adapter answers with (same vocabulary as apis/openapi.yaml), in match order.
var feedReasons = []struct {
	err    error
	reason string
}{
	{feed.ErrListingNotOnNode, "listing-not-on-node"},
	{listing.ErrNegativeAvailability, "negative-availability"},
	{feed.ErrFeedAlreadyApplied, "feed-already-applied"},
	{ports.ErrConcurrentUpdate, "concurrent-update"},
	{shared.ErrValidation, "validation-failed"},
}

// internalErrorReason is the reason of every failure that is not a refusal the
// caller can act on (a database error, a bug).
const internalErrorReason = "internal-error"

// recordOutcome counts one inventory feed attempt. A nil error is a settled
// feed: FAILED when the result carries the broken invariant (it is committed,
// ADR 0005), APPLIED or SUPERSEDED otherwise; a non-nil error is a refusal
// that recorded nothing.
func (uc *ApplyInventoryFeed) recordOutcome(ctx context.Context, res FeedResult, err error) {
	m := uc.metrics()
	switch {
	case err != nil:
		m.FeedRejected(ctx, feedReason(err))
	case res.Failed():
		m.FeedFailed(ctx, feedReason(res.Failure))
	case res.Feed.State == string(feed.StateSuperseded):
		m.FeedSuperseded(ctx)
	default:
		m.FeedApplied(ctx)
	}
}

func feedReason(err error) string {
	for _, r := range feedReasons {
		if errors.Is(err, r.err) {
			return r.reason
		}
	}
	return internalErrorReason
}
