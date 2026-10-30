package usecases

import (
	"context"
	"errors"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/views"
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

// recordOutcome counts one inventory feed attempt. A nil error is an applied or
// superseded feed (the view says which); an error that comes with a stored view
// is a FAILED feed (it is committed, ADR 0005); any other error is a refusal
// that recorded nothing.
func (uc *ApplyInventoryFeed) recordOutcome(ctx context.Context, v views.InventoryFeed, err error) {
	m := uc.metrics()
	switch {
	case err == nil && v.State == string(feed.StateSuperseded):
		m.FeedSuperseded(ctx)
	case err == nil:
		m.FeedApplied(ctx)
	case v.State == string(feed.StateFailed):
		m.FeedFailed(ctx, feedReason(err))
	default:
		m.FeedRejected(ctx, feedReason(err))
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
