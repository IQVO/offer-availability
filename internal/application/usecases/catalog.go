package usecases

import (
	"context"
	"errors"
	"fmt"

	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
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
	// GTIN is the optional identifiers.gtin, raw as sent ("" = absent).
	GTIN string
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
	if cmd.GTIN != "" {
		g, err := catalog.ParseGTIN(cmd.GTIN)
		if err != nil {
			return views.CatalogItem{}, err
		}
		spec.GTIN = g
	}
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.CatalogItem, error) {
		if err := uc.gtinFree(ctx, spec.GTIN, ""); err != nil {
			return views.CatalogItem{}, err
		}
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

// gtinFree refuses a GTIN another item already holds (as its identifier or as
// a pack level, retired ones included): a GTIN identifies one thing forever.
// The zero GTIN is absent and always free; except is the item being changed.
func (w writer) gtinFree(ctx context.Context, g gs1.GTIN, except catalog.ID) error {
	if g.IsZero() {
		return nil
	}
	owner, err := w.Catalog.FindByGTIN(ctx, g)
	hit, err := found(err)
	if err != nil {
		return err
	}
	if hit && owner != except {
		return fmt.Errorf("%w: %s identifies item %s", catalog.ErrGtinAlreadyAssigned, g, owner)
	}
	return nil
}

// DeclarePackLevelCommand is the input of DeclarePackLevel. The strings are
// raw (a GTIN is parsed here, never trusted from a client).
type DeclarePackLevelCommand struct {
	CatalogItemID catalog.ID
	Level         string
	GTIN          string
	ChildGTIN     string
	UnitsPerPack  int
	LengthMm      int
	WidthMm       int
	HeightMm      int
	GrossWeightG  int
}

// DeclarePackLevel declares (PUT) one pack level of an item.
type DeclarePackLevel struct{ writer }

// NewDeclarePackLevel builds the use case.
func NewDeclarePackLevel(d Deps) *DeclarePackLevel { return &DeclarePackLevel{writer{d}} }

// Execute parses the level and GTINs, refuses a GTIN another item holds, runs
// the domain rules (ADR 0006) and saves the item with the raised events. The
// identical declaration is returned unchanged with no event.
func (uc *DeclarePackLevel) Execute(ctx context.Context, cmd DeclarePackLevelCommand) (views.CatalogItem, error) {
	level, err := catalog.ParsePackLevel(cmd.Level)
	if err != nil {
		return views.CatalogItem{}, err
	}
	spec := catalog.PackSpec{
		UnitsPerPack: cmd.UnitsPerPack, LengthMm: cmd.LengthMm, WidthMm: cmd.WidthMm, HeightMm: cmd.HeightMm,
		GrossWeightG: cmd.GrossWeightG,
	}
	if cmd.GTIN != "" {
		if spec.GTIN, err = catalog.ParseGTIN(cmd.GTIN); err != nil {
			return views.CatalogItem{}, err
		}
	}
	if cmd.ChildGTIN != "" {
		if spec.ChildGTIN, err = catalog.ParseGTIN(cmd.ChildGTIN); err != nil {
			return views.CatalogItem{}, err
		}
	}
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.CatalogItem, error) {
		item, err := uc.Catalog.Get(ctx, cmd.CatalogItemID)
		if err != nil {
			return views.CatalogItem{}, err
		}
		if err := uc.gtinFree(ctx, spec.GTIN, item.ID()); err != nil {
			return views.CatalogItem{}, err
		}
		return uc.save(ctx, item, func() ([]shared.Event, error) { return item.DeclarePackLevel(level, spec, uc.now()) })
	})
}

// save runs change on the loaded item and, when it raised events, saves the
// item and appends them to the outbox.
func (w writer) save(ctx context.Context, item *catalog.Item, change func() ([]shared.Event, error)) (views.CatalogItem, error) {
	events, err := change()
	if err != nil {
		return views.CatalogItem{}, err
	}
	if len(events) == 0 {
		return views.CatalogItemFrom(item), nil
	}
	if err := w.Catalog.Save(ctx, item); err != nil {
		return views.CatalogItem{}, err
	}
	if err := w.publish(ctx, events); err != nil {
		return views.CatalogItem{}, err
	}
	return views.CatalogItemFrom(item), nil
}

// RetirePackLevel retires (POST :retire) the active pack level of an item.
type RetirePackLevel struct{ writer }

// NewRetirePackLevel builds the use case.
func NewRetirePackLevel(d Deps) *RetirePackLevel { return &RetirePackLevel{writer{d}} }

// Execute retires the active definition at level. A level that only has retired
// history is returned unchanged with no event; one never declared is
// catalog.ErrPackLevelNotFound.
func (uc *RetirePackLevel) Execute(ctx context.Context, id catalog.ID, level string) (views.CatalogItem, error) {
	l, err := catalog.ParsePackLevel(level)
	if err != nil {
		return views.CatalogItem{}, err
	}
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.CatalogItem, error) {
		item, err := uc.Catalog.Get(ctx, id)
		if err != nil {
			return views.CatalogItem{}, err
		}
		return uc.save(ctx, item, func() ([]shared.Event, error) { return item.RetirePackLevel(l, uc.now()) })
	})
}
