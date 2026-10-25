//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/offer-availability/internal/adapters/inbound/mcp"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/clock"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/offer-availability/internal/adapters/outbound/kafka"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then reads through MCP
// tools exactly what the write use cases persisted, and its reads add no
// outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	countOutbox := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n); err != nil {
			t.Fatalf("count outbox_events: %v", err)
		}
		return n
	}

	itemID, listingID := seedThroughWriteUseCases(t, url)
	before := countOutbox()
	if before < 2 {
		t.Fatalf("outbox rows after seeding = %d, want at least the item's and the listing's events", before)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertSeededReads(t, session, itemID, listingID)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the MCP binary wrote an event", before, after)
	}
}

// seedThroughWriteUseCases creates one catalogue item, activates node-a in
// the local node status view, lists the item on it and applies one feed line,
// the way cmd/api does (same use cases, Postgres adapters, transactional
// outbox). It returns the item id and the listing id.
func seedThroughWriteUseCases(t *testing.T, url string) (itemID, listingID string) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	outbox := postgres.NewOutboxRepo(pool)
	d := usecases.Deps{
		Catalog: postgres.NewCatalogRepo(pool), Listings: postgres.NewListingRepo(pool), Feeds: postgres.NewFeedRepo(pool),
		Marks: postgres.NewNodeFeedMarks(pool), Nodes: postgres.NewNodeStatusRepo(pool), Inbox: postgres.NewProcessedEventRepo(pool),
		Outbox: postgres.NewEventOutbox(outbox, outboundkafka.NewEncoder()), UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: ids.UUID{},
	}
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

	item, err := usecases.NewCreateCatalogItem(d).Execute(ctx, usecases.CreateCatalogItemCommand{
		Title: "Study kettle", LengthMm: 220, WidthMm: 160, HeightMm: 180, WeightG: 900,
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	view, err := nodestatus.New(nodestatus.ID("node-a"), nodestatus.StatusActive, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Nodes.Save(ctx, view); err != nil {
		t.Fatalf("activate node: %v", err)
	}
	l, err := usecases.NewCreateListing(d).Execute(ctx, usecases.CreateListingCommand{
		CatalogItemID: mustCatalogID(t, item.CatalogItemID), NodeID: listing.NodeID("node-a"),
	})
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if _, err := usecases.NewApplyInventoryFeed(d).Execute(ctx, usecases.ApplyInventoryFeedCommand{
		FeedID: feed.ID("feed-mcpit-1"), NodeID: listing.NodeID("node-a"), AsOf: at,
		Lines: []feed.Line{{ListingID: listing.ID(l.ListingID), Qty: 12, Basis: listing.BasisPhysical}},
	}); err != nil {
		t.Fatalf("apply feed: %v", err)
	}
	return item.CatalogItemID, l.ListingID
}

// mustCatalogID parses an item id, failing the test when malformed.
func mustCatalogID(t *testing.T, id string) catalog.ID {
	t.Helper()
	out := catalog.ID(id)
	if !out.Valid() {
		t.Fatalf("malformed item id %q", id)
	}
	return out
}

// assertSeededReads reads back, through every MCP tool, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, session *sdk.ClientSession, itemID, listingID string) {
	t.Helper()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}

	page := call("browse_catalog", map[string]any{})
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["catalogItemId"] != itemID {
		t.Fatalf("browse_catalog = %v, want item %s", page, itemID)
	}
	item := call("get_catalog_item", map[string]any{"catalogItemId": itemID})
	if item["title"] != "Study kettle" || item["state"] != "ACTIVE" {
		t.Fatalf("get_catalog_item = %v", item)
	}
	listings := call("list_listings", map[string]any{"nodeId": "node-a"})["listings"].([]any)
	if len(listings) != 1 {
		t.Fatalf("list_listings = %v", listings)
	}
	got := listings[0].(map[string]any)
	if got["listingId"] != listingID || got["state"] != "LIVE" {
		t.Fatalf("list_listings[0] = %v", got)
	}
	avail := got["availability"].(map[string]any)
	if avail["availableQty"] != 12.0 || avail["basis"] != "PHYSICAL" || avail["feedId"] != "feed-mcpit-1" {
		t.Fatalf("the listing's availability = %v", avail)
	}
	f := call("get_inventory_feed", map[string]any{"feedId": "feed-mcpit-1"})
	if f["state"] != "APPLIED" || f["appliedCount"] != 1.0 || f["nodeId"] != "node-a" {
		t.Fatalf("get_inventory_feed = %v", f)
	}
}
