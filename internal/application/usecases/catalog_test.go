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

func goodItem() usecases.CreateCatalogItemCommand {
	return usecases.CreateCatalogItemCommand{Title: "Bottle", LengthMm: 270, WidthMm: 75, HeightMm: 75, WeightG: 340, Fragile: true}
}

func TestCreateCatalogItem(t *testing.T) {
	e := newEnv(t)
	got, err := usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), goodItem())
	mustNot(t, err)
	if got.CatalogItemID != "item-1" || got.State != "ACTIVE" || got.Title != "Bottle" || !got.Fragile || got.Hazmat || got.WeightG != 340 {
		t.Fatalf("view = %+v", got)
	}
	stored, err := e.store.Catalog().Get(context.Background(), "item-1")
	mustNot(t, err)
	if stored.Version() != 1 {
		t.Fatalf("stored version = %d", stored.Version())
	}
	e.assertEvents("CatalogItemCreated")
}

func TestCreateCatalogItemFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*env, *usecases.CreateCatalogItemCommand)
		want  error
	}{
		{"zero dimension", func(_ *env, c *usecases.CreateCatalogItemCommand) { c.WidthMm = 0 }, catalog.ErrInvalidItemDimensions},
		{"blank title", func(_ *env, c *usecases.CreateCatalogItemCommand) { c.Title = " " }, shared.ErrValidation},
		{"save fails", func(e *env, _ *usecases.CreateCatalogItemCommand) { e.catalog.saveErrs = []error{errBoom} }, errBoom},
		{"outbox fails", func(e *env, _ *usecases.CreateCatalogItemCommand) { e.outbox.err = errBoom }, errBoom},
	}
	for _, c := range cases {
		e := newEnv(t)
		cmd := goodItem()
		c.setup(e, &cmd)
		before := e.store.Counts()
		_, err := usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), cmd)
		if err == nil {
			t.Fatalf("%s: want an error", c.name)
		}
		wantIs(t, err, c.want)
		e.assertNothingStored(before)
	}
}

func TestWritesRetryOnceOnConcurrentUpdate(t *testing.T) {
	e := newEnv(t)
	e.catalog.saveErrs = []error{ports.ErrConcurrentUpdate}
	got, err := usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), goodItem())
	mustNot(t, err)
	if got.CatalogItemID != "item-2" {
		t.Fatalf("the retry mints a fresh id, got %s", got.CatalogItemID)
	}
	e.assertEvents("CatalogItemCreated")

	e = newEnv(t)
	e.catalog.saveErrs = []error{ports.ErrConcurrentUpdate, ports.ErrConcurrentUpdate}
	before := e.store.Counts()
	_, err = usecases.NewCreateCatalogItem(e.deps).Execute(context.Background(), goodItem())
	wantIs(t, err, ports.ErrConcurrentUpdate)
	e.assertNothingStored(before)
}

func TestDiscontinueItemSuppressesEveryListing(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	e.seedListing("lst-b", "item-1", "node-2", -1, repoNow())
	e.seedListing("lst-a", "item-1", "node-1", 5, repoNow().Add(1))
	e.seedListing("lst-other", "item-2", "node-1", -1, repoNow())
	// lst-c was already suppressed for the node: it changes reason.
	e.seedListing("lst-c", "item-1", "node-3", -1, repoNow())
	c := e.listing("lst-c")
	_, err := c.Suppress(listing.ReasonNodeNotSellable, repoNow())
	mustNot(t, err)
	mustNot(t, e.store.Listings().Save(context.Background(), c))
	// lst-d is already suppressed for DISCONTINUED: it is skipped.
	e.seedListing("lst-d", "item-1", "node-4", -1, repoNow())
	d := e.listing("lst-d")
	_, err = d.Suppress(listing.ReasonDiscontinued, repoNow())
	mustNot(t, err)
	mustNot(t, e.store.Listings().Save(context.Background(), d))

	got, err := usecases.NewDiscontinueItem(e.deps).Execute(context.Background(), "item-1")
	mustNot(t, err)
	if got.State != "DISCONTINUED" {
		t.Fatalf("view = %+v", got)
	}
	e.assertEvents("CatalogItemDiscontinued", "ListingSuppressed", "ListingSuppressed", "ListingSuppressed")
	for _, id := range []listing.ID{"lst-a", "lst-b", "lst-c", "lst-d"} {
		l := e.listing(id)
		if l.State() != listing.StateSuppressed || l.Reason() != listing.ReasonDiscontinued {
			t.Errorf("%s: state %s reason %s", id, l.State(), l.Reason())
		}
	}
	if other := e.listing("lst-other"); other.State() != listing.StateLive {
		t.Fatalf("a listing of another item was suppressed")
	}
	if e.listing("lst-d").Version() != 2 {
		t.Fatalf("a listing already suppressed for DISCONTINUED must not be saved again")
	}
	if e.listing("lst-a").Availability().Qty != 5 {
		t.Fatalf("the availability must survive the suppression")
	}
}

func TestDiscontinueItemTwiceIsANoOp(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	uc := usecases.NewDiscontinueItem(e.deps)
	_, err := uc.Execute(context.Background(), "item-1")
	mustNot(t, err)
	before := e.store.Counts()
	got, err := uc.Execute(context.Background(), "item-1")
	mustNot(t, err)
	if got.State != "DISCONTINUED" {
		t.Fatalf("view = %+v", got)
	}
	e.assertNothingStored(before)
}

func TestDiscontinueItemFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*env)
		id    catalog.ID
		want  error
	}{
		{"unknown item", func(*env) {}, "item-9", ports.ErrNotFound},
		{"get fails", func(e *env) { e.catalog.getErr = errBoom }, "item-1", errBoom},
		{"save fails", func(e *env) { e.catalog.saveErrs = []error{errBoom} }, "item-1", errBoom},
		{"list fails", func(e *env) { e.listings.byItemErr = errBoom }, "item-1", errBoom},
		{"save many fails", func(e *env) { e.listings.saveManyErrs = []error{errBoom} }, "item-1", errBoom},
		{"outbox fails", func(e *env) { e.outbox.err = errBoom }, "item-1", errBoom},
	}
	for _, c := range cases {
		e := newEnv(t)
		e.seedItem("item-1")
		e.seedListing("lst-1", "item-1", "node-1", -1, repoNow())
		c.setup(e)
		before := e.store.Counts()
		_, err := usecases.NewDiscontinueItem(e.deps).Execute(context.Background(), c.id)
		wantIs(t, err, c.want)
		e.assertNothingStored(before)
	}
}
