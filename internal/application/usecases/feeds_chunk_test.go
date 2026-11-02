package usecases_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// nodeWithListings seeds n extra LIVE listings on node-1, so the node counts
// n+2 listings (feedEnv's lst-a and lst-b included).
func nodeWithListings(t *testing.T, e *env, n int) {
	t.Helper()
	for i := range n {
		id := listing.ID(fmt.Sprintf("lst-x%02d", i))
		e.seedListing(id, listing.ItemID(fmt.Sprintf("item-x%02d", i)), "node-1", 3, repoNow())
	}
}

func TestApplyFeedWritesInBatchesOfTheConfiguredChunkSize(t *testing.T) {
	e := feedEnv(t)
	nodeWithListings(t, e, 6) // 8 listings on node-1; the full feed below covers lst-b
	e.deps.FeedChunkSize = 4
	got, err := apply(e, feedCmd("feed-1", true, asOf1, ln("lst-b", 7)))
	mustNot(t, err)
	if got.state != "APPLIED" || got.applied != 1 || got.zeroed != 7 {
		t.Fatalf("result = %+v", got)
	}
	if sizes := e.listings.saveManySizes; !reflect.DeepEqual(sizes, []int{4, 4}) {
		t.Fatalf("SaveMany batch sizes = %v, want [4 4]", sizes)
	}
	if sizes := e.outbox.appendSizes; !reflect.DeepEqual(sizes, []int{4, 4, 1}) {
		t.Fatalf("outbox batch sizes = %v, want [4 4 1] (8 changes + the feed event)", sizes)
	}
	for _, id := range []listing.ID{"lst-a", "lst-x00", "lst-x05"} {
		if e.listing(id).Availability().Qty != 0 {
			t.Fatalf("listing %s was not zeroed", id)
		}
	}
	if e.listing("lst-b").Availability().Qty != 7 {
		t.Fatal("the fed line was not applied")
	}
	if n := len(e.events()); n != 9 {
		t.Fatalf("stored events = %d, want 9", n)
	}
}

func TestApplyFeedWithoutAChunkSizeWritesOneBatch(t *testing.T) {
	e := feedEnv(t)
	nodeWithListings(t, e, 6)
	got, err := apply(e, feedCmd("feed-1", true, asOf1, ln("lst-b", 7)))
	mustNot(t, err)
	if got.zeroed != 7 {
		t.Fatalf("result = %+v", got)
	}
	if sizes := e.listings.saveManySizes; !reflect.DeepEqual(sizes, []int{8}) {
		t.Fatalf("SaveMany batch sizes = %v, want [8]", sizes)
	}
	if sizes := e.outbox.appendSizes; !reflect.DeepEqual(sizes, []int{9}) {
		t.Fatalf("outbox batch sizes = %v, want [9]", sizes)
	}
}
