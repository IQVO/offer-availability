package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/offer-availability/internal/adapters/inbound/mcp"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
)

// now is the fixture's fixed "now".
var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// listingRef is the handle a seeded listing is known by: its three ids.
type listingRef struct {
	id, item, node string
}

// harness is a connected in-memory MCP client over the real server wired to
// the in-memory repositories, exactly as cmd/mcp wires them (minus Postgres).
// The write use cases exist ONLY here, to seed data the way the REST adapter
// does: the MCP surface itself has none.
type harness struct {
	session *sdk.ClientSession
	store   *memory.Store
	create  *usecases.CreateCatalogItem
	list    *usecases.CreateListing
	apply   *usecases.ApplyInventoryFeed

	// the ids of the world seededWorld builds
	itemID, liveID, suppressedID string
}

// newHarness wires the four READ use cases into the MCP server and keeps the
// write use cases for seeding.
func newHarness(t *testing.T) *harness {
	t.Helper()
	s := memory.NewStore()
	d := usecases.Deps{
		Catalog: s.Catalog(), Listings: s.Listings(), Feeds: s.Feeds(), Marks: s.Marks(), Nodes: s.Nodes(),
		Inbox: s.Inbox(), Outbox: s.Outbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(now), IDs: &memory.SequentialIDs{},
	}
	deps := inboundmcp.Deps{
		BrowseCatalog:    usecases.NewBrowseCatalog(d.Catalog),
		GetCatalogItem:   usecases.NewGetCatalogItem(d.Catalog),
		ListListings:     usecases.NewListListings(d.Listings),
		GetInventoryFeed: usecases.NewGetInventoryFeed(d.Feeds),
	}
	return &harness{
		session: connectSession(t, deps), store: s,
		create: usecases.NewCreateCatalogItem(d), list: usecases.NewCreateListing(d), apply: usecases.NewApplyInventoryFeed(d),
	}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// activate makes the node ACTIVE in the local node status view, so a listing
// on it starts LIVE (ADR 0002, A5).
func (h *harness) activate(t *testing.T, node string) {
	t.Helper()
	v, err := nodestatus.New(nodestatus.ID(node), nodestatus.StatusActive, now)
	must(t, err)
	must(t, h.store.Nodes().Save(t.Context(), v))
}

// createItem creates one ACTIVE catalogue item and returns its id.
func (h *harness) createItem(t *testing.T) string {
	t.Helper()
	item, err := h.create.Execute(t.Context(), usecases.CreateCatalogItemCommand{
		Title: "Study kettle", LengthMm: 220, WidthMm: 160, HeightMm: 180, WeightG: 900,
	})
	must(t, err)
	return item.CatalogItemID
}

// listOn creates the listing of item on node. With active=true the node is
// made ACTIVE first, so the listing starts LIVE; otherwise it starts
// SUPPRESSED with reason NODE_NOT_SELLABLE.
func (h *harness) listOn(t *testing.T, item, node string, active bool) listingRef {
	t.Helper()
	if active {
		h.activate(t, node)
	}
	id := catalog.ID(item)
	if !id.Valid() {
		t.Fatalf("malformed item id %q", item)
	}
	l, err := h.list.Execute(t.Context(), usecases.CreateListingCommand{CatalogItemID: id, NodeID: listing.NodeID(node)})
	must(t, err)
	return listingRef{id: l.ListingID, item: item, node: node}
}

// applyFeed applies one one-line feed on node for the given listing, the way
// vendor-connectivity does, and returns the feed id.
func (h *harness) applyFeed(t *testing.T, feedID, node, listingID string, qty int) string {
	t.Helper()
	_, err := h.apply.Execute(t.Context(), usecases.ApplyInventoryFeedCommand{
		FeedID: feed.ID(feedID), NodeID: listing.NodeID(node), AsOf: now,
		Lines: []feed.Line{{ListingID: listing.ID(listingID), Qty: qty, Basis: listing.BasisPhysical}},
	})
	must(t, err)
	return feedID
}

// seededWorld gives every read tool something to see: one item, two listings
// (one LIVE with availability on node-a, one SUPPRESSED on node-b, which was
// never activated) and the feed that set node-a's availability.
func seededWorld(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.itemID = h.createItem(t)
	live := h.listOn(t, h.itemID, "node-a", true)
	suppressed := h.listOn(t, h.itemID, "node-b", false)
	h.applyFeed(t, "feed-7", "node-a", live.id, 12)
	h.liveID, h.suppressedID = live.id, suppressed.id
	return h
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
