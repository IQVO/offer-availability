package features_test

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

func (w *world) itemID(alias string) (catalog.ID, error) {
	id, ok := w.items[alias]
	if !ok {
		return "", fmt.Errorf("unknown item %q", alias)
	}
	return id, nil
}

func (w *world) createItemWithGTIN(alias, gtin string) error {
	w.act()
	v, err := usecases.NewCreateCatalogItem(w.deps).Execute(context.Background(), usecases.CreateCatalogItemCommand{
		Title: alias, LengthMm: 100, WidthMm: 100, HeightMm: 100, WeightG: 100, GTIN: gtin,
	})
	w.err = err
	if err == nil {
		w.items[alias] = catalog.ID(v.CatalogItemID)
	}
	return nil
}

func (w *world) anItemWithGTIN(alias, gtin string) error {
	if err := w.createItemWithGTIN(alias, gtin); err != nil {
		return err
	}
	return w.mustSucceed()
}

func (w *world) declare(level, alias, gtin, child string, units int) error {
	id, err := w.itemID(alias)
	if err != nil {
		return err
	}
	w.act()
	_, w.err = usecases.NewDeclarePackLevel(w.deps).Execute(context.Background(), usecases.DeclarePackLevelCommand{
		CatalogItemID: id, Level: level, GTIN: gtin, ChildGTIN: child, UnitsPerPack: units,
	})
	return nil
}

func (w *world) declareEach(level, alias, gtin string, units int) error {
	return w.declare(level, alias, gtin, "", units)
}

func (w *world) declareOver(level, alias, gtin, child string, units int) error {
	return w.declare(level, alias, gtin, child, units)
}

// packChain declares EACH(1), INNER(6) and CASE(24) as setup.
func (w *world) packChain(alias string) error {
	for _, s := range []struct {
		level, gtin, child string
		units              int
	}{
		{"EACH", "06141410000120", "", 1},
		{"INNER", "06141410000298", "06141410000120", 6},
		{"CASE", "06141410000366", "06141410000298", 24},
	} {
		if err := w.declare(s.level, alias, s.gtin, s.child, s.units); err != nil {
			return err
		}
		if err := w.mustSucceed(); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) retire(level, alias string) error {
	id, err := w.itemID(alias)
	if err != nil {
		return err
	}
	w.act()
	_, w.err = usecases.NewRetirePackLevel(w.deps).Execute(context.Background(), id, level)
	return nil
}

func (w *world) packaging(alias string) ([]catalog.PackLevelDef, error) {
	id, err := w.itemID(alias)
	if err != nil {
		return nil, err
	}
	it, err := w.store.Catalog().Get(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return it.PackLevels(), nil
}

func (w *world) hasActiveLevel(alias, level, gtin string, units int) error {
	defs, err := w.packaging(alias)
	if err != nil {
		return err
	}
	for _, d := range defs {
		if string(d.Level) == level && d.Active && d.GTIN.String() == gtin && d.UnitsPerPack == units {
			return nil
		}
	}
	return fmt.Errorf("item %q has no active %s level %s of %d units: %+v", alias, level, gtin, units, defs)
}

func (w *world) hasRetiredLevel(alias, level, gtin string) error {
	defs, err := w.packaging(alias)
	if err != nil {
		return err
	}
	for _, d := range defs {
		if string(d.Level) == level && !d.Active && d.GTIN.String() == gtin && !d.RetiredAt.IsZero() {
			return nil
		}
	}
	return fmt.Errorf("item %q has no retired %s level %s: %+v", alias, level, gtin, defs)
}

func (w *world) hasGTIN(alias, gtin string) error {
	id, err := w.itemID(alias)
	if err != nil {
		return err
	}
	it, err := w.store.Catalog().Get(context.Background(), id)
	if err != nil {
		return err
	}
	if it.GTIN().String() != gtin {
		return fmt.Errorf("item %q has gtin %q, want %q", alias, it.GTIN(), gtin)
	}
	return nil
}

func (w *world) createOwnedListing(item, node, owner string) error {
	id, err := w.itemID(item)
	if err != nil {
		return err
	}
	w.act()
	v, err := usecases.NewCreateListing(w.deps).Execute(context.Background(), usecases.CreateListingCommand{
		CatalogItemID: id, NodeID: listing.NodeID(node), OwnerRef: listing.OwnerRef(owner),
	})
	w.err = err
	if err == nil {
		w.listings[item+"@"+node+"/"+owner] = listing.ID(v.ListingID)
	}
	return nil
}

func (w *world) anOwnedListing(item, node, owner string) error {
	if err := w.createOwnedListing(item, node, owner); err != nil {
		return err
	}
	return w.mustSucceed()
}

func (w *world) listingOwnedBy(key, owner string) error {
	l, err := w.load(key)
	if err != nil {
		return err
	}
	if string(l.OwnerRef()) != owner {
		return fmt.Errorf("listing %q has owner %q, want %q", key, l.OwnerRef(), owner)
	}
	return nil
}

func registerPackagingSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I create the catalogue item "([^"]*)" with gtin "([^"]*)"$`, w.createItemWithGTIN)
	sc.Step(`^the catalogue item "([^"]*)" with gtin "([^"]*)"$`, w.anItemWithGTIN)
	sc.Step(`^I declare the (EACH|INNER|CASE|PALLET) level of "([^"]*)" with gtin "([^"]*)" and (\d+) units$`, w.declareEach)
	sc.Step(`^I declare the (EACH|INNER|CASE|PALLET) level of "([^"]*)" with gtin "([^"]*)" over "([^"]*)" and (\d+) units$`, w.declareOver)
	sc.Step(`^the pack chain of "([^"]*)"$`, w.packChain)
	sc.Step(`^I retire the (EACH|INNER|CASE|PALLET) level of "([^"]*)"$`, w.retire)
	sc.Step(`^the item "([^"]*)" has the active (EACH|INNER|CASE|PALLET) level "([^"]*)" of (\d+) units per pack$`, w.hasActiveLevel)
	sc.Step(`^the item "([^"]*)" has the retired (EACH|INNER|CASE|PALLET) level "([^"]*)"$`, w.hasRetiredLevel)
	sc.Step(`^the item "([^"]*)" has the gtin "([^"]*)"$`, w.hasGTIN)
	sc.Step(`^I create a listing of "([^"]*)" on node "([^"]*)" owned by "([^"]*)"$`, w.createOwnedListing)
	sc.Step(`^a listing of "([^"]*)" on node "([^"]*)" owned by "([^"]*)"$`, w.anOwnedListing)
	sc.Step(`^the listing "([^"]*)" is owned by "([^"]*)"$`, w.listingOwnedBy)
}
