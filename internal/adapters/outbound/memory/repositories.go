package memory

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// checkVersion applies the optimistic-concurrency rule of the ports package: a
// create (version 1) needs the id to be free, an update needs the stored
// version to be version-1.
func checkVersion(stored int64, exists bool, version int64) error {
	if version == 1 {
		if exists {
			return fmt.Errorf("%w: id already stored", ports.ErrConcurrentUpdate)
		}
		return nil
	}
	if !exists || stored != version-1 {
		return fmt.Errorf("%w: stored version is not %d", ports.ErrConcurrentUpdate, version-1)
	}
	return nil
}

// sortedKeys returns the keys of m in ascending order.
func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// CatalogRepository is the in-memory ports.CatalogRepository.
type CatalogRepository struct{ s *Store }

// Get returns the item, or ports.ErrNotFound.
func (r *CatalogRepository) Get(ctx context.Context, id catalog.ID) (*catalog.Item, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.items[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return catalog.RestorePackaged(snap, r.s.packs[id])
}

// FindByGTIN returns the id of the item whose own GTIN or pack level GTIN is
// gtin, or ports.ErrNotFound.
func (r *CatalogRepository) FindByGTIN(ctx context.Context, gtin gs1.GTIN) (catalog.ID, error) {
	defer r.s.enter(ctx)()
	if id, ok := r.ownerOf(gtin, ""); ok {
		return id, nil
	}
	return "", ports.ErrNotFound
}

// ownerOf finds the item (other than except) that holds gtin.
func (r *CatalogRepository) ownerOf(gtin gs1.GTIN, except catalog.ID) (catalog.ID, bool) {
	for _, id := range sortedKeys(r.s.items) {
		if id == except {
			continue
		}
		if r.s.items[id].GTIN == gtin {
			return id, true
		}
		for _, d := range r.s.packs[id] {
			if d.GTIN == gtin {
				return id, true
			}
		}
	}
	return "", false
}

// List returns the items of the page by id ascending.
func (r *CatalogRepository) List(ctx context.Context, page repository.Page) ([]*catalog.Item, error) {
	defer r.s.enter(ctx)()
	out := []*catalog.Item{}
	for _, id := range sortedKeys(r.s.items) {
		if string(id) <= page.After || len(out) >= page.Limit {
			continue
		}
		it, err := catalog.RestorePackaged(r.s.items[id], r.s.packs[id])
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Save stores the item under the concurrency rule.
func (r *CatalogRepository) Save(ctx context.Context, i *catalog.Item) error {
	defer r.s.enter(ctx)()
	snap := i.Snapshot()
	stored, exists := r.s.items[snap.ID]
	if err := checkVersion(stored.Version, exists, snap.Version); err != nil {
		return err
	}
	for _, g := range gtinsOf(snap, i.PackLevels()) {
		if _, taken := r.ownerOf(g, snap.ID); taken {
			return catalog.ErrGtinAlreadyAssigned
		}
	}
	r.s.items[snap.ID] = snap
	r.s.packs[snap.ID] = i.PackLevels()
	return nil
}

// gtinsOf lists every GTIN an item holds.
func gtinsOf(snap catalog.Snapshot, defs []catalog.PackLevelDef) []gs1.GTIN {
	var out []gs1.GTIN
	if !snap.GTIN.IsZero() {
		out = append(out, snap.GTIN)
	}
	for _, d := range defs {
		out = append(out, d.GTIN)
	}
	return out
}

// ListingRepository is the in-memory ports.ListingRepository.
type ListingRepository struct{ s *Store }

// Get returns the listing, or ports.ErrNotFound.
func (r *ListingRepository) Get(ctx context.Context, id listing.ID) (*listing.Listing, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.listings[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return listing.Restore(snap)
}

// FindByItemAndNode returns the listing of the pair, or ports.ErrNotFound.
func (r *ListingRepository) FindByItemAndNode(ctx context.Context, item listing.ItemID, node listing.NodeID) (*listing.Listing, error) {
	return r.FindByItemNodeOwner(ctx, item, node, "")
}

// FindByItemNodeOwner returns the listing of the triple, or ports.ErrNotFound.
func (r *ListingRepository) FindByItemNodeOwner(ctx context.Context, item listing.ItemID, node listing.NodeID, owner listing.OwnerRef) (*listing.Listing, error) {
	defer r.s.enter(ctx)()
	for _, id := range sortedKeys(r.s.listings) {
		if snap := r.s.listings[id]; snap.ItemID == item && snap.NodeID == node && snap.OwnerRef == owner {
			return listing.Restore(snap)
		}
	}
	return nil, ports.ErrNotFound
}

// GetMany returns the stored listings among ids by id ascending.
func (r *ListingRepository) GetMany(ctx context.Context, ids []listing.ID) ([]*listing.Listing, error) {
	defer r.s.enter(ctx)()
	want := make(map[listing.ID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	return r.collect(func(s listing.Snapshot) bool { _, ok := want[s.ID]; return ok })
}

// ListByNode returns every listing of the node by id ascending.
func (r *ListingRepository) ListByNode(ctx context.Context, node listing.NodeID) ([]*listing.Listing, error) {
	defer r.s.enter(ctx)()
	return r.collect(func(s listing.Snapshot) bool { return s.NodeID == node })
}

// ListByItem returns every listing of the item by id ascending.
func (r *ListingRepository) ListByItem(ctx context.Context, item listing.ItemID) ([]*listing.Listing, error) {
	defer r.s.enter(ctx)()
	return r.collect(func(s listing.Snapshot) bool { return s.ItemID == item })
}

// List returns the matching listings of the page by id ascending.
func (r *ListingRepository) List(ctx context.Context, f repository.ListingFilter, page repository.Page) ([]*listing.Listing, error) {
	defer r.s.enter(ctx)()
	all, err := r.collect(func(s listing.Snapshot) bool {
		return string(s.ID) > page.After && (f.NodeID == "" || s.NodeID == f.NodeID) && (f.ItemID == "" || s.ItemID == f.ItemID)
	})
	if err != nil {
		return nil, err
	}
	if len(all) > page.Limit {
		all = all[:page.Limit]
	}
	return all, nil
}

// collect restores, in id order, the stored listings that keep accepts. The
// caller holds the store.
func (r *ListingRepository) collect(keep func(listing.Snapshot) bool) ([]*listing.Listing, error) {
	out := []*listing.Listing{}
	for _, id := range sortedKeys(r.s.listings) {
		snap := r.s.listings[id]
		if !keep(snap) {
			continue
		}
		l, err := listing.Restore(snap)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// Save stores the listing under the concurrency rule; a second listing for the
// same (item, node) is listing.ErrDuplicateListing.
func (r *ListingRepository) Save(ctx context.Context, l *listing.Listing) error {
	defer r.s.enter(ctx)()
	return r.save(l)
}

// SaveMany stores the listings, all or none.
func (r *ListingRepository) SaveMany(ctx context.Context, ls []*listing.Listing) error {
	defer r.s.enter(ctx)()
	before := cloneMap(r.s.listings)
	for _, l := range ls {
		if err := r.save(l); err != nil {
			r.s.listings = before
			return err
		}
	}
	return nil
}

func (r *ListingRepository) save(l *listing.Listing) error {
	snap := l.Snapshot()
	stored, exists := r.s.listings[snap.ID]
	if err := checkVersion(stored.Version, exists, snap.Version); err != nil {
		return err
	}
	if !exists {
		for id, other := range r.s.listings {
			if id != snap.ID && other.ItemID == snap.ItemID && other.NodeID == snap.NodeID && other.OwnerRef == snap.OwnerRef {
				return listing.ErrDuplicateListing
			}
		}
	}
	r.s.listings[snap.ID] = snap
	return nil
}

// FeedRepository is the in-memory ports.FeedRepository.
type FeedRepository struct{ s *Store }

// Get returns the feed, or ports.ErrNotFound.
func (r *FeedRepository) Get(ctx context.Context, id feed.ID) (*feed.InventoryFeed, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.feeds[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return feed.Restore(snap)
}

// Save stores the feed under the concurrency rule.
func (r *FeedRepository) Save(ctx context.Context, f *feed.InventoryFeed) error {
	defer r.s.enter(ctx)()
	snap := f.Snapshot()
	stored, exists := r.s.feeds[snap.ID]
	if err := checkVersion(stored.Version, exists, snap.Version); err != nil {
		return err
	}
	r.s.feeds[snap.ID] = snap
	return nil
}

// NodeFeedMarks is the in-memory ports.NodeFeedMarks. The unit of work already
// serialises everything, so Lock only reads.
type NodeFeedMarks struct{ s *Store }

// Lock returns the mark of the node, the zero time when none.
func (m *NodeFeedMarks) Lock(ctx context.Context, node listing.NodeID) (time.Time, error) {
	defer m.s.enter(ctx)()
	return m.s.marks[node], nil
}

// Advance raises the mark to asOf when asOf is later.
func (m *NodeFeedMarks) Advance(ctx context.Context, node listing.NodeID, asOf time.Time) error {
	defer m.s.enter(ctx)()
	if cur, ok := m.s.marks[node]; !ok || asOf.After(cur) {
		m.s.marks[node] = asOf.UTC()
	}
	return nil
}

// NodeStatusRepository is the in-memory ports.NodeStatusRepository.
type NodeStatusRepository struct{ s *Store }

// Get returns the view, or ports.ErrNotFound.
func (r *NodeStatusRepository) Get(ctx context.Context, id nodestatus.ID) (*nodestatus.View, error) {
	defer r.s.enter(ctx)()
	snap, ok := r.s.nodes[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return nodestatus.Restore(snap)
}

// Save stores the view under the concurrency rule.
func (r *NodeStatusRepository) Save(ctx context.Context, v *nodestatus.View) error {
	defer r.s.enter(ctx)()
	snap := v.Snapshot()
	stored, exists := r.s.nodes[snap.ID]
	if err := checkVersion(stored.Version, exists, snap.Version); err != nil {
		return err
	}
	r.s.nodes[snap.ID] = snap
	return nil
}

// ProcessedEvents is the in-memory consumer inbox.
type ProcessedEvents struct{ s *Store }

// MarkProcessed records the event id and reports whether it was new.
func (p *ProcessedEvents) MarkProcessed(ctx context.Context, eventID, eventType string, _ time.Time) (bool, error) {
	defer p.s.enter(ctx)()
	if _, seen := p.s.processed[eventID]; seen {
		return false, nil
	}
	p.s.processed[eventID] = eventType
	return true, nil
}
