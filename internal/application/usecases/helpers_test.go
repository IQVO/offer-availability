package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var errBoom = errors.New("boom")

// env is one use-case test bench: the memory doubles behind fault-injecting
// wrappers, a fixed clock and sequential ids.
type env struct {
	t     *testing.T
	store *memory.Store
	clock *memory.Clock

	catalog  *flakyCatalog
	listings *flakyListings
	feeds    *flakyFeeds
	marks    *flakyMarks
	nodes    *flakyNodes
	inbox    *flakyInbox
	outbox   *flakyOutbox
	deps     usecases.Deps
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s := memory.NewStore()
	e := &env{
		t: t, store: s, clock: memory.NewClock(repocontract.Now),
		catalog: &flakyCatalog{CatalogRepository: s.Catalog()}, listings: &flakyListings{ListingRepository: s.Listings()},
		feeds: &flakyFeeds{FeedRepository: s.Feeds()}, marks: &flakyMarks{NodeFeedMarks: s.Marks()},
		nodes: &flakyNodes{NodeStatusRepository: s.Nodes()}, inbox: &flakyInbox{ProcessedEvents: s.Inbox()},
		outbox: &flakyOutbox{EventOutbox: s.Outbox()},
	}
	e.deps = usecases.Deps{
		Catalog: e.catalog, Listings: e.listings, Feeds: e.feeds, Marks: e.marks, Nodes: e.nodes, Sellers: s.Sellers(), Inbox: e.inbox,
		Outbox: e.outbox, UoW: s.UnitOfWork(), Clock: e.clock, IDs: &memory.SequentialIDs{},
	}
	return e
}

// ---- assertions -----------------------------------------------------------

func (e *env) events() []shared.Event { return e.store.Outbox().Events() }

// eventNames returns the names of the outbox events, in order.
func (e *env) eventNames() []string {
	names := []string{}
	for _, ev := range e.events() {
		names = append(names, ev.EventName())
	}
	return names
}

func (e *env) assertEvents(want ...string) {
	e.t.Helper()
	got := e.eventNames()
	if len(got) != len(want) {
		e.t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			e.t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

// assertNothingStored proves a failed use case stored and changed nothing.
func (e *env) assertNothingStored(before memory.Counts) {
	e.t.Helper()
	if got := e.store.Counts(); got != before {
		e.t.Fatalf("a failed use case changed the store: %+v, was %+v", got, before)
	}
}

func wantIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func mustNot(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---- seeding --------------------------------------------------------------

func (e *env) seedItem(id catalog.ID) {
	e.t.Helper()
	mustNot(e.t, e.store.Catalog().Save(context.Background(), repocontract.NewItem(e.t, id)))
}

func (e *env) seedNode(id nodestatus.ID, status nodestatus.Status) {
	e.t.Helper()
	mustNot(e.t, e.store.Nodes().Save(context.Background(), repocontract.NewNodeView(e.t, id, status)))
}

// seedListing stores a LIVE listing that is fed with qty at asOf (no feed when qty < 0).
func (e *env) seedListing(id listing.ID, item listing.ItemID, node listing.NodeID, qty int, asOf time.Time) {
	e.t.Helper()
	l := repocontract.NewListing(e.t, id, item, node)
	mustNot(e.t, e.store.Listings().Save(context.Background(), l))
	if qty >= 0 {
		repocontract.Fed(e.t, l, qty, listing.BasisPhysical, asOf)
		mustNot(e.t, e.store.Listings().Save(context.Background(), l))
	}
}

func (e *env) listing(id listing.ID) *listing.Listing {
	e.t.Helper()
	l, err := e.store.Listings().Get(context.Background(), id)
	mustNot(e.t, err)
	return l
}
