package usecases

import (
	"context"
	"errors"

	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// CreateCatalogItemCommand is the input of CreateCatalogItem.
type CreateCatalogItemCommand struct {
	Title    string
	LengthMm int
	WidthMm  int
	HeightMm int
	WeightG  int
	Hazmat   bool
	Fragile  bool
}

// CreateCatalogItem creates an ACTIVE catalogue item.
type CreateCatalogItem struct{ writer }

// NewCreateCatalogItem builds the use case.
func NewCreateCatalogItem(d Deps) *CreateCatalogItem { return &CreateCatalogItem{writer{d}} }

// Execute mints the id, builds the item and saves it with CatalogItemCreated.
func (uc *CreateCatalogItem) Execute(ctx context.Context, cmd CreateCatalogItemCommand) (views.CatalogItem, error) {
	spec := catalog.Spec{
		Title: cmd.Title, LengthMm: cmd.LengthMm, WidthMm: cmd.WidthMm, HeightMm: cmd.HeightMm,
		WeightG: cmd.WeightG, Hazmat: cmd.Hazmat, Fragile: cmd.Fragile,
	}
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.CatalogItem, error) {
		item, events, err := catalog.Create(uc.IDs.NewCatalogItemID(), spec, uc.now())
		if err != nil {
			return views.CatalogItem{}, err
		}
		if err := uc.Catalog.Save(ctx, item); err != nil {
			return views.CatalogItem{}, err
		}
		if err := uc.publish(ctx, events); err != nil {
			return views.CatalogItem{}, err
		}
		return views.CatalogItemFrom(item), nil
	})
}

// DiscontinueItem discontinues a catalogue item and suppresses its listings.
type DiscontinueItem struct{ writer }

// NewDiscontinueItem builds the use case.
func NewDiscontinueItem(d Deps) *DiscontinueItem { return &DiscontinueItem{writer{d}} }

// Execute loads the item (unknown is ports.ErrNotFound) and discontinues it. A
// DISCONTINUED item is returned unchanged with no event (ADR 0002, A3).
// Otherwise, in the same unit of work, every listing of the item that is not
// already suppressed for DISCONTINUED is suppressed with that reason (A4); the
// events leave in the order CatalogItemDiscontinued, then ListingSuppressed per
// listing by listing id.
func (uc *DiscontinueItem) Execute(ctx context.Context, id catalog.ID) (views.CatalogItem, error) {
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.CatalogItem, error) {
		item, err := uc.Catalog.Get(ctx, id)
		if err != nil {
			return views.CatalogItem{}, err
		}
		events := item.Discontinue(uc.now())
		if len(events) == 0 {
			return views.CatalogItemFrom(item), nil
		}
		if err := uc.Catalog.Save(ctx, item); err != nil {
			return views.CatalogItem{}, err
		}
		more, err := uc.suppressListings(ctx, listing.ItemID(id))
		if err != nil {
			return views.CatalogItem{}, err
		}
		if err := uc.publish(ctx, append(events, more...)); err != nil {
			return views.CatalogItem{}, err
		}
		return views.CatalogItemFrom(item), nil
	})
}

// suppressListings suppresses, with reason DISCONTINUED, every listing of the
// item for which that is a legal transition, saves them and returns their events.
func (uc *DiscontinueItem) suppressListings(ctx context.Context, item listing.ItemID) ([]shared.Event, error) {
	listings, err := uc.Listings.ListByItem(ctx, item)
	if err != nil {
		return nil, err
	}
	var (
		changed []*listing.Listing
		events  []shared.Event
	)
	for _, l := range listings {
		ev, err := l.Suppress(listing.ReasonDiscontinued, uc.now())
		if errors.Is(err, shared.ErrIllegalTransition) {
			continue
		}
		if err != nil {
			return nil, err
		}
		changed = append(changed, l)
		events = append(events, ev...)
	}
	if err := uc.Listings.SaveMany(ctx, changed); err != nil {
		return nil, err
	}
	return events, nil
}
