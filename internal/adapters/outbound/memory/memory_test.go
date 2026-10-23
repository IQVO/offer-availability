package memory_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
)

// TestRepositoryContract runs the repository contract against the doubles.
func TestRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Fixture {
		s := memory.NewStore()
		return repocontract.Fixture{
			Catalog: s.Catalog(), Listings: s.Listings(), Feeds: s.Feeds(), Marks: s.Marks(), Nodes: s.Nodes(),
			Inbox: s.Inbox(), Outbox: s.Outbox(), UoW: s.UnitOfWork(),
			OutboxCount: func(context.Context) (int, error) { return len(s.Outbox().Events()), nil },
		}
	})
}

func TestClockAndSequentialIDs(t *testing.T) {
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	c := memory.NewClock(start)
	c.Advance(time.Hour)
	if !c.Now().Equal(start.Add(time.Hour)) {
		t.Fatalf("now = %v", c.Now())
	}
	c.Set(start)
	if !c.Now().Equal(start) {
		t.Fatalf("now = %v", c.Now())
	}
	ids := &memory.SequentialIDs{}
	got := []string{string(ids.NewCatalogItemID()), string(ids.NewCatalogItemID()), string(ids.NewListingID()), string(ids.NewListingID())}
	want := []string{"item-1", "item-2", "lst-1", "lst-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
}

func TestCountsFollowTheStore(t *testing.T) {
	s := memory.NewStore()
	if c := s.Counts(); c != (memory.Counts{}) {
		t.Fatalf("empty store counts = %+v", c)
	}
	ctx := context.Background()
	if err := s.Catalog().Save(ctx, repocontract.NewItem(t, "item-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Listings().Save(ctx, repocontract.NewListing(t, "lst-1", "item-1", "node-1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Feeds().Save(ctx, repocontract.NewFeed(t, "feed-1", "node-1", repocontract.Now)); err != nil {
		t.Fatal(err)
	}
	if err := s.Marks().Advance(ctx, "node-1", repocontract.Now); err != nil {
		t.Fatal(err)
	}
	if err := s.Nodes().Save(ctx, repocontract.NewNodeView(t, "node-1", "ACTIVE")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inbox().MarkProcessed(ctx, "evt-1", "t", repocontract.Now); err != nil {
		t.Fatal(err)
	}
	if err := s.Outbox().Append(ctx, repocontract.SomeEvents(t)...); err != nil {
		t.Fatal(err)
	}
	want := memory.Counts{Items: 1, Listings: 1, Feeds: 1, Marks: 1, Nodes: 1, Processed: 1, OutboxEvents: 1, VersionSum: 4}
	if c := s.Counts(); c != want {
		t.Fatalf("counts = %+v, want %+v", c, want)
	}
}

func TestMarkAdvanceIsMonotonic(t *testing.T) {
	s := memory.NewStore()
	ctx := context.Background()
	later := repocontract.Now.Add(time.Hour)
	for _, at := range []time.Time{later, repocontract.Now} {
		if err := s.Marks().Advance(ctx, "node-1", at); err != nil {
			t.Fatal(err)
		}
	}
	mark, err := s.Marks().Lock(ctx, "node-1")
	if err != nil || !mark.Equal(later) {
		t.Fatalf("mark = %v %v, want %v", mark, err, later)
	}
}
