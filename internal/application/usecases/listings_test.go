package usecases_test

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func repoNow() time.Time { return repocontract.Now }

type listingResult struct {
	id, state, reason string
	err               error
}

func createListing(e *env, item, node string) (listingResult, []string) {
	v, err := usecases.NewCreateListing(e.deps).Execute(context.Background(),
		usecases.CreateListingCommand{CatalogItemID: catalog.ID(item), NodeID: listing.NodeID(node)})
	return listingResult{v.ListingID, v.State, v.SuppressionReason, err}, e.eventNames()
}

func TestCreateListingStartsByNodeStatus(t *testing.T) {
	cases := []struct {
		name       string
		node       nodestatus.Status // "" = unknown node
		discont    bool
		wantState  string
		wantReason string
		wantEvents []string
	}{
		{"active node", nodestatus.StatusActive, false, "LIVE", "", []string{"ListingCreated"}},
		{"proposed node", nodestatus.StatusProposed, false, "SUPPRESSED", "NODE_NOT_SELLABLE", []string{"ListingCreated", "ListingSuppressed"}},
		{"suspended node", nodestatus.StatusSuspended, false, "SUPPRESSED", "NODE_NOT_SELLABLE", []string{"ListingCreated", "ListingSuppressed"}},
		{"retired node", nodestatus.StatusRetired, false, "SUPPRESSED", "NODE_NOT_SELLABLE", []string{"ListingCreated", "ListingSuppressed"}},
		{"unknown node", "", false, "SUPPRESSED", "NODE_NOT_SELLABLE", []string{"ListingCreated", "ListingSuppressed"}},
		{"discontinued item", nodestatus.StatusActive, true, "SUPPRESSED", "DISCONTINUED", []string{"ListingCreated", "ListingSuppressed"}},
	}
	for _, c := range cases {
		e := newEnv(t)
		e.seedItem("item-1")
		if c.node != "" {
			e.seedNode("node-1", c.node)
		}
		if c.discont {
			_, err := usecases.NewDiscontinueItem(e.deps).Execute(context.Background(), "item-1")
			mustNot(t, err)
		}
		got, events := createListing(e, "item-1", "node-1")
		mustNot(t, got.err)
		if got.id != "lst-1" || got.state != c.wantState || got.reason != c.wantReason {
			t.Errorf("%s: got %+v", c.name, got)
		}
		want := append([]string(nil), c.wantEvents...)
		if c.discont {
			want = append([]string{"CatalogItemDiscontinued"}, want...)
		}
		if len(events) != len(want) {
			t.Errorf("%s: events = %v, want %v", c.name, events, want)
		}
	}
}

func TestCreateListingViewHasTheInitialAvailability(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	e.seedNode("node-1", nodestatus.StatusActive)
	v, err := usecases.NewCreateListing(e.deps).Execute(context.Background(), usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"})
	mustNot(t, err)
	if v.Availability.AvailableQty != 0 || v.Availability.Basis != "UNKNOWN" || v.Availability.AsOf != nil || v.Availability.FeedID != "" ||
		v.CatalogItemID != "item-1" || v.NodeID != "node-1" {
		t.Fatalf("view = %+v", v)
	}
}

func TestCreateListingFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*env)
		cmd   usecases.CreateListingCommand
		want  error
	}{
		{"unknown item", func(*env) {}, usecases.CreateListingCommand{CatalogItemID: "item-9", NodeID: "node-1"}, ports.ErrNotFound},
		{"malformed item id", func(*env) {}, usecases.CreateListingCommand{CatalogItemID: "", NodeID: "node-1"}, shared.ErrValidation},
		{"malformed node id", func(*env) {}, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "a b"}, shared.ErrValidation},
		{"duplicate", func(e *env) { e.seedListing("lst-0", "item-1", "node-1", -1, repoNow()) }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, listing.ErrDuplicateListing},
		{"item read fails", func(e *env) { e.catalog.getErr = errBoom }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, errBoom},
		{"pair lookup fails", func(e *env) { e.listings.findErr = errBoom }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, errBoom},
		{"node view read fails", func(e *env) { e.nodes.getErr = errBoom }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, errBoom},
		{"save fails", func(e *env) { e.listings.saveErrs = []error{errBoom} }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, errBoom},
		{"save finds a racing duplicate", func(e *env) { e.listings.saveErrs = []error{listing.ErrDuplicateListing, listing.ErrDuplicateListing} }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, listing.ErrDuplicateListing},
		{"outbox fails", func(e *env) { e.outbox.err = errBoom }, usecases.CreateListingCommand{CatalogItemID: "item-1", NodeID: "node-1"}, errBoom},
	}
	for _, c := range cases {
		e := newEnv(t)
		e.seedItem("item-1")
		c.setup(e)
		before := e.store.Counts()
		_, err := usecases.NewCreateListing(e.deps).Execute(context.Background(), c.cmd)
		wantIs(t, err, c.want)
		e.assertNothingStored(before)
	}
}
