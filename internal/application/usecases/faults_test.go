package usecases_test

import (
	"context"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// The fault-injecting wrappers of the use-case test bench (env in helpers_test.go).

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
	saveManySizes                                      []int
}

func (f *flakyListings) FindByItemAndNode(ctx context.Context, i listing.ItemID, n listing.NodeID) (*listing.Listing, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.ListingRepository.FindByItemAndNode(ctx, i, n)
}

func (f *flakyListings) FindByItemNodeOwner(ctx context.Context, i listing.ItemID, n listing.NodeID, o listing.OwnerRef) (*listing.Listing, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.ListingRepository.FindByItemNodeOwner(ctx, i, n, o)
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
	f.saveManySizes = append(f.saveManySizes, len(ls))
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
	err         error
	appendSizes []int
}

func (f *flakyOutbox) Append(ctx context.Context, events ...shared.Event) error {
	if f.err != nil {
		return f.err
	}
	f.appendSizes = append(f.appendSizes, len(events))
	return f.EventOutbox.Append(ctx, events...)
}
