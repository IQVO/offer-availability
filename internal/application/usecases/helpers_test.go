package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
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
		Catalog: e.catalog, Listings: e.listings, Feeds: e.feeds, Marks: e.marks, Nodes: e.nodes, Inbox: e.inbox,
		Outbox: e.outbox, UoW: s.UnitOfWork(), Clock: e.clock, IDs: &memory.SequentialIDs{},
	}
	return e
}

// ---- fault injection ------------------------------------------------------

// failAt returns errs[i] for the i-th call, nil past the end.
func failAt(calls *int, errs []error) error {
	i := *calls
	*calls++
	if i < len(errs) {
		return errs[i]
	}
	return nil
}

type flakyCatalog struct {
	ports.CatalogRepository
	getErr, listErr, findErr error
	saveErrs                 []error
	saves                    int
}

func (f *flakyCatalog) Get(ctx context.Context, id catalog.ID) (*catalog.Item, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.CatalogRepository.Get(ctx, id)
}

func (f *flakyCatalog) FindByGTIN(ctx context.Context, g gs1.GTIN) (catalog.ID, error) {
	if f.findErr != nil {
		return "", f.findErr
	}
	return f.CatalogRepository.FindByGTIN(ctx, g)
}

func (f *flakyCatalog) List(ctx context.Context, p repository.Page) ([]*catalog.Item, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.CatalogRepository.List(ctx, p)
}

func (f *flakyCatalog) Save(ctx context.Context, i *catalog.Item) error {
	if err := failAt(&f.saves, f.saveErrs); err != nil {
		return err
	}
	return f.CatalogRepository.Save(ctx, i)
}

type flakyListings struct {
	ports.ListingRepository
	findErr, getManyErr, byNodeErr, byItemErr, listErr error
	saveErrs, saveManyErrs                             []error
	saves, saveManys                                   int
}

func (f *flakyListings) FindByItemAndNode(ctx context.Context, i listing.ItemID, n listing.NodeID) (*listing.Listing, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.ListingRepository.FindByItemAndNode(ctx, i, n)
}

func (f *flakyListings) GetMany(ctx context.Context, ids []listing.ID) ([]*listing.Listing, error) {
	if f.getManyErr != nil {
		return nil, f.getManyErr
	}
	return f.ListingRepository.GetMany(ctx, ids)
}

func (f *flakyListings) ListByNode(ctx context.Context, n listing.NodeID) ([]*listing.Listing, error) {
	if f.byNodeErr != nil {
		return nil, f.byNodeErr
	}
	return f.ListingRepository.ListByNode(ctx, n)
}

func (f *flakyListings) ListByItem(ctx context.Context, i listing.ItemID) ([]*listing.Listing, error) {
	if f.byItemErr != nil {
		return nil, f.byItemErr
	}
	return f.ListingRepository.ListByItem(ctx, i)
}

func (f *flakyListings) List(ctx context.Context, fl repository.ListingFilter, p repository.Page) ([]*listing.Listing, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.ListingRepository.List(ctx, fl, p)
}

func (f *flakyListings) Save(ctx context.Context, l *listing.Listing) error {
	if err := failAt(&f.saves, f.saveErrs); err != nil {
		return err
	}
	return f.ListingRepository.Save(ctx, l)
}

func (f *flakyListings) SaveMany(ctx context.Context, ls []*listing.Listing) error {
	if err := failAt(&f.saveManys, f.saveManyErrs); err != nil {
		return err
	}
	return f.ListingRepository.SaveMany(ctx, ls)
}

type flakyFeeds struct {
	ports.FeedRepository
	getErr   error
	saveErrs []error
	saves    int
}

func (f *flakyFeeds) Get(ctx context.Context, id feed.ID) (*feed.InventoryFeed, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.FeedRepository.Get(ctx, id)
}

func (f *flakyFeeds) Save(ctx context.Context, x *feed.InventoryFeed) error {
	if err := failAt(&f.saves, f.saveErrs); err != nil {
		return err
	}
	return f.FeedRepository.Save(ctx, x)
}

type flakyMarks struct {
	ports.NodeFeedMarks
	lockErr, advanceErr error
}

func (f *flakyMarks) Lock(ctx context.Context, n listing.NodeID) (time.Time, error) {
	if f.lockErr != nil {
		return time.Time{}, f.lockErr
	}
	return f.NodeFeedMarks.Lock(ctx, n)
}

func (f *flakyMarks) Advance(ctx context.Context, n listing.NodeID, at time.Time) error {
	if f.advanceErr != nil {
		return f.advanceErr
	}
	return f.NodeFeedMarks.Advance(ctx, n, at)
}

type flakyNodes struct {
	ports.NodeStatusRepository
	getErr, saveErr error
}

func (f *flakyNodes) Get(ctx context.Context, id nodestatus.ID) (*nodestatus.View, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.NodeStatusRepository.Get(ctx, id)
}

func (f *flakyNodes) Save(ctx context.Context, v *nodestatus.View) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.NodeStatusRepository.Save(ctx, v)
}

type flakyInbox struct {
	ports.ProcessedEvents
	err error
}

func (f *flakyInbox) MarkProcessed(ctx context.Context, id, typ string, at time.Time) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.ProcessedEvents.MarkProcessed(ctx, id, typ, at)
}

type flakyOutbox struct {
	ports.EventOutbox
	err error
}

func (f *flakyOutbox) Append(ctx context.Context, events ...shared.Event) error {
	if f.err != nil {
		return f.err
	}
	return f.EventOutbox.Append(ctx, events...)
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
