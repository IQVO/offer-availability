package usecases_test

import (
	"context"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

const (
	uEach  = "06141410000120"
	uInner = "06141410000298"
	uCase  = "06141410000366"
	uSpare = "06141410000502"
)

func declare(e *env, item catalog.ID, level, gtin, child string, units int) (err error) {
	_, err = usecases.NewDeclarePackLevel(e.deps).Execute(context.Background(), usecases.DeclarePackLevelCommand{
		CatalogItemID: item, Level: level, GTIN: gtin, ChildGTIN: child, UnitsPerPack: units,
	})
	return err
}

func declareChain(t *testing.T, e *env, item catalog.ID) {
	t.Helper()
	mustNot(t, declare(e, item, "EACH", uEach, "", 1))
	mustNot(t, declare(e, item, "INNER", uInner, uEach, 6))
	mustNot(t, declare(e, item, "CASE", uCase, uInner, 24))
}

func TestCreateCatalogItemWithGTIN(t *testing.T) {
	e := newEnv(t)
	cmd := goodItem()
	cmd.GTIN = "6141410000120" // 13 digits: answered normalised to 14
	got, err := usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), cmd)
	mustNot(t, err)
	if got.Identifiers == nil || got.Identifiers.GTIN != uEach {
		t.Fatalf("identifiers = %+v", got.Identifiers)
	}
	if ev := e.events()[0].(catalog.CatalogItemCreated); ev.GTIN.String() != uEach {
		t.Fatalf("event gtin = %s", ev.GTIN)
	}
	// the same GTIN on a second item, as a different spelling, is refused
	cmd.GTIN = uEach
	_, err = usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), cmd)
	wantIs(t, err, catalog.ErrGtinAlreadyAssigned)
}

func TestCreateCatalogItemRejectsABadGTIN(t *testing.T) {
	for _, g := range []string{"06141410000121", "abc", "96141410000115"} {
		e := newEnv(t)
		cmd := goodItem()
		cmd.GTIN = g
		before := e.store.Counts()
		_, err := usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), cmd)
		wantIs(t, err, catalog.ErrInvalidGTIN)
		e.assertNothingStored(before)
	}
}

func TestDeclarePackLevelPublishesAndStores(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	declareChain(t, e, "item-1")
	e.assertEvents("CatalogItemPackagingDeclared", "CatalogItemPackagingDeclared", "CatalogItemPackagingDeclared")
	view, err := usecases.NewGetCatalogItem(e.deps.Catalog).Execute(context.Background(), "item-1")
	mustNot(t, err)
	if len(view.Packaging) != 3 || view.Packaging[2].Level != "CASE" || view.Packaging[2].UnitsPerPack != 24 || view.Packaging[2].ChildGTIN != uInner || !view.Packaging[2].Active {
		t.Fatalf("packaging = %+v", view.Packaging)
	}
	// the identical declaration raises nothing
	n := len(e.events())
	mustNot(t, declare(e, "item-1", "CASE", uCase, uInner, 24))
	if len(e.events()) != n {
		t.Fatalf("an identical declaration published %d event(s)", len(e.events())-n)
	}
}

func TestDeclarePackLevelSupersede(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	declareChain(t, e, "item-1")
	mustNot(t, declare(e, "item-1", "CASE", uSpare, uInner, 12))
	names := e.eventNames()
	if got := names[len(names)-2:]; got[0] != "CatalogItemPackLevelRetired" || got[1] != "CatalogItemPackagingDeclared" {
		t.Fatalf("events = %v", names)
	}
}

func TestDeclarePackLevelFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*env)
		do    func(*env) error
		want  error
	}{
		{"unknown item", func(*env) {}, func(e *env) error { return declare(e, "item-9", "EACH", uEach, "", 1) }, nil},
		{"bad level", func(*env) {}, func(e *env) error { return declare(e, "item-1", "BOX", uEach, "", 1) }, catalog.ErrInvalidPackLevel},
		{"bad gtin", func(*env) {}, func(e *env) error { return declare(e, "item-1", "EACH", "06141410000121", "", 1) }, catalog.ErrInvalidGTIN},
		{"bad child gtin", func(*env) {}, func(e *env) error { return declare(e, "item-1", "CASE", uCase, "123", 6) }, catalog.ErrInvalidGTIN},
		{"missing gtin", func(*env) {}, func(e *env) error { return declare(e, "item-1", "EACH", "", "", 1) }, catalog.ErrInvalidGTIN},
		{"no child level yet", func(*env) {}, func(e *env) error { return declare(e, "item-1", "CASE", uCase, uEach, 6) }, catalog.ErrPackLevelOrder},
		{"read fails", func(e *env) { e.catalog.getErr = errBoom }, func(e *env) error { return declare(e, "item-1", "EACH", uEach, "", 1) }, errBoom},
		{"save fails", func(e *env) { e.catalog.saveErrs = []error{errBoom} }, func(e *env) error { return declare(e, "item-1", "EACH", uEach, "", 1) }, errBoom},
		{"outbox fails", func(e *env) { e.outbox.err = errBoom }, func(e *env) error { return declare(e, "item-1", "EACH", uEach, "", 1) }, errBoom},
		{"gtin lookup fails", func(e *env) { e.catalog.findErr = errBoom }, func(e *env) error { return declare(e, "item-1", "EACH", uEach, "", 1) }, errBoom},
		{"gtin of another item", func(e *env) {
			e.seedItem("item-2")
			mustNot(e.t, declare(e, "item-2", "EACH", uEach, "", 1))
		}, func(e *env) error { return declare(e, "item-1", "EACH", uEach, "", 1) }, catalog.ErrGtinAlreadyAssigned},
		{"discontinued item", func(e *env) {
			_, err := usecases.NewDiscontinueItem(e.deps).Execute(context.Background(), "item-1")
			mustNot(e.t, err)
		}, func(e *env) error { return declare(e, "item-1", "EACH", uEach, "", 1) }, shared.ErrIllegalTransition},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.seedItem("item-1")
			c.setup(e)
			before := e.store.Counts()
			err := c.do(e)
			if c.want == nil {
				c.want = ports.ErrNotFound
			}
			wantIs(t, err, c.want)
			if c.name != "gtin of another item" && c.name != "discontinued item" {
				e.assertNothingStored(before)
			}
		})
	}
}

func TestDeclareImmutableUnitsAndRetire(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	declareChain(t, e, "item-1")
	wantIs(t, declare(e, "item-1", "CASE", uCase, uInner, 48), catalog.ErrPackQuantityImmutable)

	v, err := usecases.NewRetirePackLevel(e.deps).Execute(context.Background(), "item-1", "CASE")
	mustNot(t, err)
	if v.Packaging[2].Active {
		t.Fatalf("case still active: %+v", v.Packaging)
	}
	n := len(e.events())
	_, err = usecases.NewRetirePackLevel(e.deps).Execute(context.Background(), "item-1", "CASE")
	mustNot(t, err)
	if len(e.events()) != n {
		t.Fatalf("retiring twice published")
	}
	_, err = usecases.NewRetirePackLevel(e.deps).Execute(context.Background(), "item-1", "PALLET")
	wantIs(t, err, catalog.ErrPackLevelNotFound)
	_, err = usecases.NewRetirePackLevel(e.deps).Execute(context.Background(), "item-1", "BOX")
	wantIs(t, err, catalog.ErrInvalidPackLevel)
	_, err = usecases.NewRetirePackLevel(e.deps).Execute(context.Background(), "item-9", "CASE")
	wantIs(t, err, ports.ErrNotFound)
	// a retired GTIN is never reusable, not even by the same item
	wantIs(t, declare(e, "item-1", "CASE", uCase, uInner, 24), catalog.ErrGtinAlreadyAssigned)
}

func TestListingsPerOwner(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	e.seedNode("node-1", "ACTIVE")
	create := func(owner listing.OwnerRef) (string, error) {
		v, err := usecases.NewCreateListing(e.deps).Execute(context.Background(), usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1", OwnerRef: owner})
		return v.OwnerRef, err
	}
	if owner, err := create(""); err != nil || owner != "" {
		t.Fatalf("operator-owned: %q %v", owner, err)
	}
	if owner, err := create("seller-7"); err != nil || owner != "seller-7" {
		t.Fatalf("seller-owned: %q %v", owner, err)
	}
	if _, err := create("seller-8"); err != nil {
		t.Fatalf("a second seller: %v", err)
	}
	if _, err := create("seller-7"); err == nil {
		t.Fatalf("the same seller twice must be a duplicate")
	} else {
		wantIs(t, err, listing.ErrDuplicateListing)
	}
	if _, err := create(""); err == nil {
		t.Fatalf("operator-owned twice must be a duplicate")
	}
	_, err := create("bad owner")
	wantIs(t, err, shared.ErrValidation)
}
