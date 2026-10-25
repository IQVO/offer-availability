// Package memory holds in-memory TEST DOUBLES of the application's out ports.
// They are for unit, BDD and contract tests only: cmd/** never imports this
// package (enforced by internal/architecture) because the service runs on real
// Postgres and Kafka.
//
// Every repository round-trips its aggregate through Snapshot and Restore, so
// a loaded aggregate never aliases stored state, a corrupt row can not become a
// live aggregate and the doubles keep the persistence contract of the real
// adapters: a miss is ports.ErrNotFound, a stale or duplicate save is
// ports.ErrConcurrentUpdate, the unique index on (catalog item, node) raises
// listing.ErrDuplicateListing.
//
// One Store backs every double. Its UnitOfWork is atomic: it holds the store
// for the whole unit and restores the state taken at the start when the unit
// fails or panics, outbox and inbox included.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// Store is the shared state of the in-memory doubles.
type Store struct {
	mu        sync.Mutex
	items     map[catalog.ID]catalog.Snapshot
	packs     map[catalog.ID][]catalog.PackLevelDef
	listings  map[listing.ID]listing.Snapshot
	feeds     map[feed.ID]feed.Snapshot
	marks     map[listing.NodeID]time.Time
	nodes     map[nodestatus.ID]nodestatus.Snapshot
	processed map[string]string
	outbox    []shared.Event
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{
		items: map[catalog.ID]catalog.Snapshot{}, packs: map[catalog.ID][]catalog.PackLevelDef{}, listings: map[listing.ID]listing.Snapshot{},
		feeds: map[feed.ID]feed.Snapshot{}, marks: map[listing.NodeID]time.Time{},
		nodes: map[nodestatus.ID]nodestatus.Snapshot{}, processed: map[string]string{},
	}
}

// state is a copy of everything a unit of work can change. Stored snapshots are
// replaced, never edited in place, so copying the maps is enough.
type state struct {
	items     map[catalog.ID]catalog.Snapshot
	packs     map[catalog.ID][]catalog.PackLevelDef
	listings  map[listing.ID]listing.Snapshot
	feeds     map[feed.ID]feed.Snapshot
	marks     map[listing.NodeID]time.Time
	nodes     map[nodestatus.ID]nodestatus.Snapshot
	processed map[string]string
	outbox    []shared.Event
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (s *Store) capture() state {
	return state{
		items: cloneMap(s.items), packs: cloneMap(s.packs), listings: cloneMap(s.listings), feeds: cloneMap(s.feeds), marks: cloneMap(s.marks),
		nodes: cloneMap(s.nodes), processed: cloneMap(s.processed), outbox: append([]shared.Event(nil), s.outbox...),
	}
}

func (s *Store) restore(st state) {
	s.items, s.packs, s.listings, s.feeds, s.marks, s.nodes, s.processed, s.outbox =
		st.items, st.packs, st.listings, st.feeds, st.marks, st.nodes, st.processed, st.outbox
}

// txKey marks a ctx that carries the open unit of work of one store.
type txKey struct{ store *Store }

// enter locks the store for one repository call, unless ctx already carries the
// unit of work that holds it. The returned func releases the lock.
func (s *Store) enter(ctx context.Context) func() {
	if ctx.Value(txKey{s}) != nil {
		return func() {}
	}
	s.mu.Lock()
	return s.mu.Unlock
}

// Catalog returns the CatalogRepository double.
func (s *Store) Catalog() *CatalogRepository { return &CatalogRepository{s} }

// Listings returns the ListingRepository double.
func (s *Store) Listings() *ListingRepository { return &ListingRepository{s} }

// Feeds returns the FeedRepository double.
func (s *Store) Feeds() *FeedRepository { return &FeedRepository{s} }

// Marks returns the NodeFeedMarks double.
func (s *Store) Marks() *NodeFeedMarks { return &NodeFeedMarks{s} }

// Nodes returns the NodeStatusRepository double.
func (s *Store) Nodes() *NodeStatusRepository { return &NodeStatusRepository{s} }

// Inbox returns the ProcessedEvents double.
func (s *Store) Inbox() *ProcessedEvents { return &ProcessedEvents{s} }

// Outbox returns the EventOutbox double.
func (s *Store) Outbox() *Outbox { return &Outbox{s} }

// UnitOfWork returns the UnitOfWork double.
func (s *Store) UnitOfWork() *UnitOfWork { return &UnitOfWork{s} }

// Counts is how many rows each table of the store holds.
type Counts struct {
	Items, Listings, Feeds, Marks, Nodes, Processed, OutboxEvents int
	// VersionSum is the sum of every aggregate version: any saved change moves it.
	VersionSum int64
}

// Counts returns the size of every table and the sum of the aggregate versions,
// so a test can prove that a failed use case stored and changed nothing.
func (s *Store) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	var versions int64
	for _, i := range s.items {
		versions += i.Version
	}
	for _, l := range s.listings {
		versions += l.Version
	}
	for _, f := range s.feeds {
		versions += f.Version
	}
	for _, n := range s.nodes {
		versions += n.Version
	}
	return Counts{
		Items: len(s.items), Listings: len(s.listings), Feeds: len(s.feeds), Marks: len(s.marks), Nodes: len(s.nodes),
		Processed: len(s.processed), OutboxEvents: len(s.outbox), VersionSum: versions,
	}
}
