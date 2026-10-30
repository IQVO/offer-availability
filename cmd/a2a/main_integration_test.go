//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/offer-availability/internal/adapters/inbound/a2a"
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
// already migrated, a second run must be a no-op), then answers over the real
// JSON-RPC router exactly what the write use cases persisted, and its skills
// add no outbox row: the agent is read-only by construction.
func TestA2AAgainstPostgres_ReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
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

	card := inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: "test"})
	srv := httptest.NewServer(newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), card, quietLogger(), "offer-availability-a2a"))
	defer srv.Close()
	card.SupportedInterfaces[0].URL = srv.URL + "/"
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}

	assertSeededReads(t, client, itemID, listingID)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the A2A binary wrote an event", before, after)
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
		FeedID: feed.ID("feed-a2ait-1"), NodeID: listing.NodeID("node-a"), AsOf: at,
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

// skillData sends one skill request over the real wire and returns the
// answer's data object (failing the test on a problem answer).
func skillData(t *testing.T, client *a2aclient.Client, skill string, args map[string]any) map[string]any {
	t.Helper()
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args})),
	})
	if err != nil {
		t.Fatalf("%s: %v", skill, err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) == 0 {
		t.Fatalf("%s: reply = %T", skill, resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("%s: first reply part is %T", skill, msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env["problem"] != nil {
		t.Fatalf("%s answered a problem: %v", skill, env["problem"])
	}
	return env["data"].(map[string]any)
}

// assertSeededReads reads back, through both skills, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, client *a2aclient.Client, itemID, listingID string) {
	t.Helper()

	av := skillData(t, client, "check_availability", map[string]any{"catalogItemId": itemID})["availability"].(map[string]any)
	item := av["item"].(map[string]any)
	if item["catalogItemId"] != itemID || item["title"] != "Study kettle" || item["state"] != "ACTIVE" {
		t.Fatalf("check_availability item = %v", item)
	}
	listings := av["listings"].([]any)
	if len(listings) != 1 {
		t.Fatalf("check_availability listings = %v", listings)
	}
	got := listings[0].(map[string]any)
	if got["listingId"] != listingID || got["state"] != "LIVE" {
		t.Fatalf("check_availability listings[0] = %v", got)
	}
	avail := got["availability"].(map[string]any)
	if avail["availableQty"] != 12.0 || avail["basis"] != "PHYSICAL" || avail["feedId"] != "feed-a2ait-1" {
		t.Fatalf("the listing's availability = %v", avail)
	}

	f := skillData(t, client, "feed_outcome", map[string]any{"feedId": "feed-a2ait-1"})["feedOutcome"].(map[string]any)
	if f["state"] != "APPLIED" || f["appliedCount"] != 1.0 || f["nodeId"] != "node-a" {
		t.Fatalf("feed_outcome = %v", f)
	}
}
