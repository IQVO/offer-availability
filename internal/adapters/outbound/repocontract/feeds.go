package repocontract

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

var feedCases = []named{
	{"save then get round-trips every state", feedRoundTrip},
	{"get of an unknown id is ErrNotFound", feedGetMiss},
	{"creating an id that exists is ErrConcurrentUpdate", feedDuplicateID},
	{"a feed moves RECEIVED, FAILED, RECEIVED, APPLIED one version per save", feedLifecycle},
	{"a stale update is ErrConcurrentUpdate", feedStaleUpdate},
}

func feedRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	asOf := Now.Add(time.Hour)
	received := NewFeed(t, "feed-received", "node-1", asOf)
	applied := NewFeed(t, "feed-applied", "node-1", asOf)
	failed := NewFeed(t, "feed-failed", "node-1", asOf)
	superseded := NewFeed(t, "feed-superseded", "node-1", asOf)
	for _, x := range []*feed.InventoryFeed{received, applied, failed, superseded} {
		noErr(t, f.Feeds.Save(ctx, x))
	}
	_, err := applied.MarkApplied(feed.Result{Applied: 1}, Now)
	noErr(t, err)
	noErr(t, failed.MarkFailed(feed.ErrListingNotOnNode))
	noErr(t, superseded.MarkSuperseded())
	for _, x := range []*feed.InventoryFeed{applied, failed, superseded} {
		noErr(t, f.Feeds.Save(ctx, x))
	}
	for _, x := range []*feed.InventoryFeed{received, applied, failed, superseded} {
		got, err := f.Feeds.Get(ctx, x.ID())
		noErr(t, err)
		if !reflect.DeepEqual(got.Snapshot(), x.Snapshot()) {
			t.Fatalf("%s: snapshot = %+v, want %+v", x.ID(), got.Snapshot(), x.Snapshot())
		}
	}
}

func feedGetMiss(t *testing.T, f Fixture) {
	_, err := f.Feeds.Get(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
}

func feedDuplicateID(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Feeds.Save(ctx, NewFeed(t, "feed-1", "node-1", Now)))
	wantErr(t, f.Feeds.Save(ctx, NewFeed(t, "feed-1", "node-2", Now)), ports.ErrConcurrentUpdate)
}

func feedLifecycle(t *testing.T, f Fixture) {
	ctx := context.Background()
	x := NewFeed(t, "feed-1", "node-1", Now.Add(time.Hour))
	noErr(t, f.Feeds.Save(ctx, x))
	noErr(t, x.MarkFailed(listing.ErrNegativeAvailability))
	noErr(t, f.Feeds.Save(ctx, x))
	sub, err := feed.NewSubmission("feed-1", "node-1", true, Now.Add(2*time.Hour), nil)
	noErr(t, err)
	noErr(t, x.Resubmit(sub, Now))
	noErr(t, f.Feeds.Save(ctx, x))
	_, err = x.MarkApplied(feed.Result{Zeroed: 2}, Now)
	noErr(t, err)
	noErr(t, f.Feeds.Save(ctx, x))
	got, err := f.Feeds.Get(ctx, "feed-1")
	noErr(t, err)
	if got.State() != feed.StateApplied || got.Version() != 4 {
		t.Fatalf("stored feed = %s v%d, want APPLIED v4", got.State(), got.Version())
	}
}

func feedStaleUpdate(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Feeds.Save(ctx, NewFeed(t, "feed-1", "node-1", Now)))
	a, err := f.Feeds.Get(ctx, "feed-1")
	noErr(t, err)
	b, err := f.Feeds.Get(ctx, "feed-1")
	noErr(t, err)
	noErr(t, a.MarkSuperseded())
	noErr(t, f.Feeds.Save(ctx, a))
	noErr(t, b.MarkFailed(feed.ErrListingNotOnNode))
	wantErr(t, f.Feeds.Save(ctx, b), ports.ErrConcurrentUpdate)
}
