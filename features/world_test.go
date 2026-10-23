package features_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
)

// world is the state of one scenario: the memory doubles behind the real use
// cases, plus the aliases the feature files use instead of generated ids.
type world struct {
	store *memory.Store
	deps  usecases.Deps

	items    map[string]catalog.ID // alias -> catalogItemId
	listings map[string]listing.ID // "item@node" -> listingId
	feeds    map[string]views.InventoryFeed

	err          error
	eventsBefore int // outbox size before the last action
}

func newWorld() *world {
	s := memory.NewStore()
	return &world{
		store: s, items: map[string]catalog.ID{}, listings: map[string]listing.ID{}, feeds: map[string]views.InventoryFeed{},
		deps: usecases.Deps{
			Catalog: s.Catalog(), Listings: s.Listings(), Feeds: s.Feeds(), Marks: s.Marks(), Nodes: s.Nodes(), Inbox: s.Inbox(),
			Outbox: s.Outbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(repocontract.Now), IDs: &memory.SequentialIDs{},
		},
	}
}

// act marks the start of an action: the events it raises are "the last events".
func (w *world) act() { w.eventsBefore = len(w.store.Outbox().Events()) }

func (w *world) eventNames(from int) []string {
	names := []string{}
	for _, ev := range w.store.Outbox().Events()[from:] {
		names = append(names, ev.EventName())
	}
	return names
}

// errorNames maps the model's Err identifiers to the errors that carry them.
var errorNames = map[string]error{
	"ErrInvalidItemDimensions": catalog.ErrInvalidItemDimensions,
	"ErrDuplicateListing":      listing.ErrDuplicateListing,
	"ErrFeedAlreadyApplied":    feed.ErrFeedAlreadyApplied,
	"ErrListingNotOnNode":      feed.ErrListingNotOnNode,
	"ErrNegativeAvailability":  listing.ErrNegativeAvailability,
}

func (w *world) listingID(alias string) (listing.ID, error) {
	id, ok := w.listings[alias]
	if !ok {
		return "", fmt.Errorf("the scenario never created a listing %q", alias)
	}
	return id, nil
}

func (w *world) load(alias string) (*listing.Listing, error) {
	id, err := w.listingID(alias)
	if err != nil {
		return nil, err
	}
	return w.store.Listings().Get(context.Background(), id)
}

// ---- Given / When ---------------------------------------------------------

func (w *world) nodeIs(node, status string) error {
	v, err := nodestatus.New(nodestatus.ID(node), nodestatus.Status(status), repocontract.Now)
	if err != nil {
		return err
	}
	return w.store.Nodes().Save(context.Background(), v)
}

func (w *world) createItem(alias string, l, wd, h, g int) error {
	w.act()
	v, err := usecases.NewCreateCatalogItem(w.deps).Execute(context.Background(), usecases.CreateCatalogItemCommand{
		Title: alias, LengthMm: l, WidthMm: wd, HeightMm: h, WeightG: g,
	})
	w.err = err
	if err == nil {
		w.items[alias] = catalog.ID(v.CatalogItemID)
	}
	return nil
}

func (w *world) anItem(alias string) error {
	if err := w.createItem(alias, 100, 100, 100, 100); err != nil {
		return err
	}
	return w.mustSucceed()
}

func (w *world) discontinue(alias string) error {
	id, ok := w.items[alias]
	if !ok {
		return fmt.Errorf("unknown item %q", alias)
	}
	w.act()
	_, w.err = usecases.NewDiscontinueItem(w.deps).Execute(context.Background(), id)
	return nil
}

func (w *world) createListing(item, node string) error {
	id, ok := w.items[item]
	if !ok {
		return fmt.Errorf("unknown item %q", item)
	}
	w.act()
	v, err := usecases.NewCreateListing(w.deps).Execute(context.Background(), usecases.CreateListingCommand{CatalogItemID: id, NodeID: listing.NodeID(node)})
	w.err = err
	if err == nil {
		w.listings[item+"@"+node] = listing.ID(v.ListingID)
	}
	return nil
}

func (w *world) aListing(item, node string) error {
	if err := w.createListing(item, node); err != nil {
		return err
	}
	return w.mustSucceed()
}

func (w *world) mustSucceed() error {
	if w.err != nil {
		return fmt.Errorf("a setup step failed: %w", w.err)
	}
	return nil
}

func (w *world) applyFeed(full bool, id, node, asOf string, table *godog.Table) error {
	at, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return err
	}
	lines := []feed.Line{}
	for _, row := range table.Rows[1:] {
		lid, err := w.listingID(row.Cells[0].Value)
		if err != nil {
			return err
		}
		qty, err := strconv.Atoi(row.Cells[1].Value)
		if err != nil {
			return err
		}
		lines = append(lines, feed.Line{ListingID: lid, Qty: qty, Basis: listing.Basis(row.Cells[2].Value)})
	}
	w.act()
	v, err := usecases.NewApplyInventoryFeed(w.deps).Execute(context.Background(), usecases.ApplyInventoryFeedCommand{
		FeedID: feed.ID(id), NodeID: listing.NodeID(node), FullUpdate: full, AsOf: at, Lines: lines,
	})
	w.err = err
	if v.FeedID != "" {
		w.feeds[id] = v
	}
	return nil
}

func (w *world) applyPartial(id, node, asOf string, t *godog.Table) error {
	return w.applyFeed(false, id, node, asOf, t)
}
func (w *world) applyFull(id, node, asOf string, t *godog.Table) error {
	return w.applyFeed(true, id, node, asOf, t)
}

func (w *world) nodeEvent(eventID, kind, node, at string) error {
	instant, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return err
	}
	status := map[string]nodestatus.Status{
		"NodeRegistered": nodestatus.StatusProposed, "NodeActivated": nodestatus.StatusActive,
		"NodeSuspended": nodestatus.StatusSuspended, "NodeRetired": nodestatus.StatusRetired,
	}[kind]
	w.act()
	w.err = usecases.NewApplyNodeEvent(w.deps).Execute(context.Background(), usecases.NodeEvent{
		EventID: eventID, EventType: "com.retailnetwork.logistics.node-topology.node." + kind, NodeID: nodestatus.ID(node), Status: status, At: instant,
	})
	return nil
}

// ---- Then -----------------------------------------------------------------

func (w *world) itemIs(alias, state string) error {
	v, err := usecases.NewGetCatalogItem(w.deps.Catalog).Execute(context.Background(), w.items[alias])
	if err != nil {
		return err
	}
	if v.State != state {
		return fmt.Errorf("item %q is %s, want %s", alias, v.State, state)
	}
	return nil
}

func (w *world) listingIs(alias, state string) error {
	l, err := w.load(alias)
	if err != nil {
		return err
	}
	if string(l.State()) != state || l.Reason() != "" {
		return fmt.Errorf("listing %q is %s (%s), want %s", alias, l.State(), l.Reason(), state)
	}
	return nil
}

func (w *world) listingSuppressed(alias, reason string) error {
	l, err := w.load(alias)
	if err != nil {
		return err
	}
	if l.State() != listing.StateSuppressed || string(l.Reason()) != reason {
		return fmt.Errorf("listing %q is %s (%s), want SUPPRESSED for %s", alias, l.State(), l.Reason(), reason)
	}
	return nil
}

func (w *world) availabilityNever(alias string, qty int, basis string) error {
	return w.availability(alias, qty, basis, "")
}

func (w *world) availabilityAsOf(alias string, qty int, basis, asOf string) error {
	return w.availability(alias, qty, basis, asOf)
}

func (w *world) availability(alias string, qty int, basis, asOf string) error {
	l, err := w.load(alias)
	if err != nil {
		return err
	}
	a := l.Availability()
	got := ""
	if !a.AsOf.IsZero() {
		got = a.AsOf.Format(time.RFC3339)
	}
	if a.Qty != qty || string(a.Basis) != basis || got != asOf {
		return fmt.Errorf("listing %q has %d %s as of %q, want %d %s as of %q", alias, a.Qty, a.Basis, got, qty, basis, asOf)
	}
	return nil
}

func (w *world) requestFails(name string) error {
	want, ok := errorNames[name]
	if !ok {
		return fmt.Errorf("the scenario names an unknown error %q", name)
	}
	if !errors.Is(w.err, want) {
		return fmt.Errorf("the request failed with %v, want %s", w.err, name)
	}
	return nil
}

func (w *world) feedApplied(id string, applied, stale, zeroed int) error {
	f, err := w.feed(id, "APPLIED")
	if err != nil {
		return err
	}
	if f.AppliedCount != applied || f.StaleCount != stale || f.ZeroedCount != zeroed {
		return fmt.Errorf("feed %q counted %d/%d/%d applied/stale/zeroed, want %d/%d/%d", id, f.AppliedCount, f.StaleCount, f.ZeroedCount, applied, stale, zeroed)
	}
	return nil
}

func (w *world) feedSuperseded(id string) error {
	_, err := w.feed(id, "SUPERSEDED")
	return err
}

func (w *world) feedFailed(id, code string) error {
	f, err := w.feed(id, "FAILED")
	if err != nil {
		return err
	}
	if f.FailureCode != code {
		return fmt.Errorf("feed %q failed with %q, want %q", id, f.FailureCode, code)
	}
	return nil
}

// feed reads the stored feed (not the answer of the request) and checks its state.
func (w *world) feed(id, state string) (views.InventoryFeed, error) {
	v, err := usecases.NewGetInventoryFeed(w.deps.Feeds).Execute(context.Background(), feed.ID(id))
	if errors.Is(err, ports.ErrNotFound) {
		return v, fmt.Errorf("feed %q was never stored", id)
	}
	if err != nil {
		return v, err
	}
	if v.State != state {
		return v, fmt.Errorf("feed %q is %s, want %s", id, v.State, state)
	}
	return v, nil
}

func splitNames(list string) []string {
	if strings.TrimSpace(list) == "" {
		return []string{}
	}
	parts := strings.Split(list, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func sameNames(got, want []string) error {
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Errorf("events are [%s], want [%s]", strings.Join(got, ", "), strings.Join(want, ", "))
	}
	return nil
}

func (w *world) outboxHolds(list string) error { return sameNames(w.eventNames(0), splitNames(list)) }

func (w *world) outboxHoldsNothing() error { return sameNames(w.eventNames(0), nil) }

func (w *world) lastEvents(list string) error {
	return sameNames(w.eventNames(w.eventsBefore), splitNames(list))
}

func (w *world) lastActionRaisedNothing() error { return sameNames(w.eventNames(w.eventsBefore), nil) }

func initializeScenario(sc *godog.ScenarioContext) {
	w := newWorld()
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		*w = *newWorld()
		return ctx, nil
	})
	sc.Step(`^the node "([^"]*)" is (PROPOSED|ACTIVE|SUSPENDED|RETIRED)$`, w.nodeIs)
	sc.Step(`^I create the catalogue item "([^"]*)" of (-?\d+) x (-?\d+) x (-?\d+) mm and (-?\d+) g$`, w.createItem)
	sc.Step(`^the catalogue item "([^"]*)"$`, w.anItem)
	sc.Step(`^I discontinue the catalogue item "([^"]*)"$`, w.discontinue)
	sc.Step(`^I create a listing of "([^"]*)" on node "([^"]*)"$`, w.createListing)
	sc.Step(`^a listing of "([^"]*)" on node "([^"]*)"$`, w.aListing)
	sc.Step(`^I apply the feed "([^"]*)" for node "([^"]*)" as of "([^"]*)" with lines:$`, w.applyPartial)
	sc.Step(`^I apply the full feed "([^"]*)" for node "([^"]*)" as of "([^"]*)" with lines:$`, w.applyFull)
	sc.Step(`^node-topology publishes the event "([^"]*)" (NodeRegistered|NodeActivated|NodeSuspended|NodeRetired) for node "([^"]*)" at "([^"]*)"$`, w.nodeEvent)

	sc.Step(`^the catalogue item "([^"]*)" is (ACTIVE|DISCONTINUED)$`, w.itemIs)
	sc.Step(`^the listing "([^"]*)" is (LIVE)$`, w.listingIs)
	sc.Step(`^the listing "([^"]*)" is SUPPRESSED for (NODE_NOT_SELLABLE|DISCONTINUED)$`, w.listingSuppressed)
	sc.Step(`^the listing "([^"]*)" has availability (\d+) (PHYSICAL|THROUGHPUT_CONSTRAINED|UNKNOWN) and no asOf$`, w.availabilityNever)
	sc.Step(`^the listing "([^"]*)" has availability (\d+) (PHYSICAL|THROUGHPUT_CONSTRAINED|UNKNOWN) as of "([^"]*)"$`, w.availabilityAsOf)
	sc.Step(`^the request fails with "([^"]*)"$`, w.requestFails)
	sc.Step(`^the feed "([^"]*)" is APPLIED with (\d+) applied, (\d+) stale and (\d+) zeroed$`, w.feedApplied)
	sc.Step(`^the feed "([^"]*)" is SUPERSEDED$`, w.feedSuperseded)
	sc.Step(`^the feed "([^"]*)" is FAILED with (Err\w+)$`, w.feedFailed)
	sc.Step(`^the outbox holds "([^"]*)"$`, w.outboxHolds)
	sc.Step(`^the outbox holds nothing$`, w.outboxHoldsNothing)
	sc.Step(`^the last events are "([^"]*)"$`, w.lastEvents)
	sc.Step(`^the last action raised no events$`, w.lastActionRaisedNothing)
}
