package features_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
)

// This file holds the vocabulary of the documentation-derived scenarios
// (catalog_rules, feed_rules, suppression_rules, received_stock_rules and
// http_rules .feature): rules from ADR 0002, 0003, 0013 and 0014 and the
// network pack's use-cases and aggregate canvas that no earlier scenario pins.

// rulesState is what the rules steps remember across the steps of a scenario.
type rulesState struct {
	received usecases.ReceivedStockResult
	skips    []string // the reasons ReceivedSkipped counted, in order
}

// skipRecorder is the ReceivedStockMetrics double: it remembers every count.
type skipRecorder struct{ st *rulesState }

func (r skipRecorder) ReceivedSkipped(_ context.Context, _, reason string) {
	r.st.skips = append(r.st.skips, reason)
}

const sellerEventPrefix = "com.retailnetwork.partner.seller-inbound."

func registerRuleSteps(sc *godog.ScenarioContext, w *world) {
	st := &rulesState{}
	// Registered after the world's own Before hook, so it runs on the fresh world.
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		*st = rulesState{}
		w.deps.ReceivedMetrics = skipRecorder{st}
		return ctx, nil
	})
	registerCatalogRuleSteps(sc, w)
	registerSellerRuleSteps(sc, w)
	registerReceivedStockSteps(sc, w, st)
	registerFeedRuleSteps(sc, w)
}

// ---- catalogue -------------------------------------------------------------

func registerCatalogRuleSteps(sc *godog.ScenarioContext, w *world) {
	create := func(spec usecases.CreateCatalogItemCommand) error {
		w.act()
		v, err := usecases.NewCreateCatalogItem(w.deps).Execute(context.Background(), spec)
		w.err = err
		if err == nil {
			w.items[spec.Title] = catalog.ID(v.CatalogItemID)
		}
		return nil
	}
	sc.Step(`^I create the catalogue item "([^"]*)" of (\d+) x (\d+) x (\d+) mm and (\d+) g with hazmat (true|false) and fragile (true|false)$`,
		func(alias string, l, wd, h, g int, hazmat, fragile string) error {
			return create(usecases.CreateCatalogItemCommand{
				Title: alias, LengthMm: l, WidthMm: wd, HeightMm: h, WeightG: g, Hazmat: hazmat == "true", Fragile: fragile == "true",
			})
		})
	sc.Step(`^I create the catalogue item "([^"]*)" with a title of (\d+) characters and (-?\d+) x (-?\d+) x (-?\d+) mm and (-?\d+) g$`,
		func(alias string, n, l, wd, h, g int) error {
			if err := create(usecases.CreateCatalogItemCommand{Title: strings.Repeat("t", n), LengthMm: l, WidthMm: wd, HeightMm: h, WeightG: g}); err != nil {
				return err
			}
			if w.err == nil {
				w.items[alias] = w.items[strings.Repeat("t", n)]
			}
			return nil
		})
	sc.Step(`^I create the catalogue item "([^"]*)" with a (leading|trailing) space in its title$`, func(alias, where string) error {
		title := alias + " "
		if where == "leading" {
			title = " " + alias
		}
		return create(usecases.CreateCatalogItemCommand{Title: title, LengthMm: 100, WidthMm: 100, HeightMm: 100, WeightG: 100})
	})
}

// ---- sellers ---------------------------------------------------------------

func registerSellerRuleSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the seller "([^"]*)" is (ACTIVE|SUSPENDED)$`, func(seller, status string) error {
		v, err := sellerstatus.New(sellerstatus.ID(seller), sellerstatus.Status(status), repocontract.Now)
		if err != nil {
			return err
		}
		return w.store.Sellers().Save(context.Background(), v)
	})
	sc.Step(`^seller-inbound publishes the event "([^"]*)" (SellerSuspended|SellerReinstated) for seller "([^"]*)" at "([^"]*)"$`,
		func(eventID, kind, seller, at string) error {
			instant, err := time.Parse(time.RFC3339, at)
			if err != nil {
				return err
			}
			status := sellerstatus.StatusSuspended
			if kind == "SellerReinstated" {
				status = sellerstatus.StatusActive
			}
			w.act()
			w.err = usecases.NewApplySellerEvent(w.deps).Execute(context.Background(), usecases.SellerEvent{
				EventID: eventID, EventType: sellerEventPrefix + "seller." + kind, SellerID: sellerstatus.ID(seller), Status: status, At: instant,
			})
			return nil
		})
	sc.Step(`^the listing "([^"]*)" is SUPPRESSED because (OWNER_SUSPENDED|NODE_NOT_SELLABLE|DISCONTINUED)$`, w.listingSuppressed)
}

// ---- received stock ----------------------------------------------------------

func registerReceivedStockSteps(sc *godog.ScenarioContext, w *world, st *rulesState) {
	publish := func(eventID, seller, node string, present bool, lines []usecases.ReceivedLine) error {
		w.act()
		st.skips = nil // the counts of the last event only
		res, err := usecases.NewApplyReceivedStock(w.deps).Execute(context.Background(), usecases.ReceivedStockEvent{
			EventID: eventID, EventType: sellerEventPrefix + "inboundshipment.InboundShipmentReceived", ShipmentID: "shp-1",
			SellerID: listing.OwnerRef(seller), NodeID: listing.NodeID(node), ReceivedPresent: present, Received: lines,
		})
		w.err, st.received = err, res
		w.aliasOwnedListings(node, seller)
		return nil
	}
	sc.Step(`^seller-inbound publishes the event "([^"]*)" InboundShipmentReceived for seller "([^"]*)" to node "([^"]*)" with received lines:$`,
		func(eventID, seller, node string, table *godog.Table) error {
			var lines []usecases.ReceivedLine
			for _, row := range table.Rows[1:] {
				var qty int
				if _, err := fmt.Sscanf(row.Cells[2].Value, "%d", &qty); err != nil {
					return err
				}
				lines = append(lines, usecases.ReceivedLine{CatalogItemID: w.itemRef(row.Cells[0].Value), GTIN: row.Cells[1].Value, QtyEaches: qty})
			}
			return publish(eventID, seller, node, true, lines)
		})
	sc.Step(`^seller-inbound publishes the event "([^"]*)" InboundShipmentReceived for seller "([^"]*)" to node "([^"]*)" without a received list$`,
		func(eventID, seller, node string) error { return publish(eventID, seller, node, false, nil) })

	sc.Step(`^the received event created (\d+) listings?, found (\d+) existing and skipped (\d+) lines?$`, func(created, existing, skipped int) error {
		r := st.received
		if len(r.Created) != created || r.Existing != existing || len(r.Skipped) != skipped {
			return fmt.Errorf("the event created %d, found %d existing and skipped %d, want %d/%d/%d",
				len(r.Created), r.Existing, len(r.Skipped), created, existing, skipped)
		}
		return nil
	})
	sc.Step(`^the received lines were counted as skipped with the reasons "([^"]*)"$`, func(list string) error {
		return sameNames(st.skips, splitNames(list))
	})
	sc.Step(`^no received line was counted as skipped$`, func() error { return sameNames(st.skips, nil) })
	sc.Step(`^the received event was a duplicate$`, func() error {
		if !st.received.Duplicate {
			return errors.New("the event was applied, want it skipped as a duplicate")
		}
		return nil
	})
	sc.Step(`^the received event carried no received list$`, func() error {
		if !st.received.Unknown {
			return errors.New("the event carried a received list")
		}
		return nil
	})
	sc.Step(`^there is no listing of "([^"]*)" on node "([^"]*)" owned by "([^"]*)"$`, func(item, node, owner string) error {
		_, err := w.store.Listings().FindByItemNodeOwner(context.Background(), listing.ItemID(w.itemRef(item)), listing.NodeID(node), listing.OwnerRef(owner))
		if !errors.Is(err, ports.ErrNotFound) {
			return fmt.Errorf("the seller %q has a listing of %q on %q (err %v), want none", owner, item, node, err)
		}
		return nil
	})
}

// itemRef turns a table cell into a catalogItemId: a known alias, a raw id, or
// empty (the producer could not resolve the item).
func (w *world) itemRef(cell string) catalog.ID {
	if id, ok := w.items[cell]; ok {
		return id
	}
	return catalog.ID(cell)
}

// aliasOwnedListings registers the listing of every known item on node for
// owner under the alias "item@node/owner", so later steps can name it.
func (w *world) aliasOwnedListings(node, owner string) {
	for alias, id := range w.items {
		l, err := w.store.Listings().FindByItemNodeOwner(context.Background(), listing.ItemID(id), listing.NodeID(node), listing.OwnerRef(owner))
		if err == nil {
			w.listings[alias+"@"+node+"/"+owner] = l.ID()
		}
	}
}

// ---- feeds -------------------------------------------------------------------

func registerFeedRuleSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I apply the feed "([^"]*)" for node "([^"]*)" as of "([^"]*)" with (\d+) lines naming listings that do not exist$`,
		func(id, node, asOf string, n int) error {
			at, err := time.Parse(time.RFC3339, asOf)
			if err != nil {
				return err
			}
			lines := make([]feed.Line, n)
			for i := range lines {
				lines[i] = feed.Line{ListingID: listing.ID(fmt.Sprintf("lst-ghost-%d", i)), Qty: 1, Basis: listing.BasisPhysical}
			}
			w.act()
			res, err := usecases.NewApplyInventoryFeed(w.deps).Execute(context.Background(), usecases.ApplyInventoryFeedCommand{
				FeedID: feed.ID(id), NodeID: listing.NodeID(node), AsOf: at, Lines: lines,
			})
			w.err = err
			if res.Failed() {
				w.err = res.Failure
			}
			return nil
		})
	sc.Step(`^the feed "([^"]*)" was never stored$`, func(id string) error {
		_, err := usecases.NewGetInventoryFeed(w.deps.Feeds).Execute(context.Background(), feed.ID(id))
		if !errors.Is(err, ports.ErrNotFound) {
			return fmt.Errorf("feed %q is stored (err %v), want it never recorded", id, err)
		}
		return nil
	})
	sc.Step(`^the feed "([^"]*)" ends (APPLIED|FAILED|SUPERSEDED)$`, func(id, state string) error {
		_, err := w.feed(id, state)
		return err
	})
	sc.Step(`^the last action raised (\d+) "([A-Za-z]+)" events?$`, func(want int, name string) error {
		got := 0
		for _, n := range w.eventNames(w.eventsBefore) {
			if n == name {
				got++
			}
		}
		if got != want {
			return fmt.Errorf("the last action raised %d %s events, want %d", got, name, want)
		}
		return nil
	})
}
