package mcp_test

import (
	"reflect"
	"testing"
)

func TestBrowseCatalog(t *testing.T) {
	h := seededWorld(t)
	got := h.ok(t, "browse_catalog", map[string]any{})
	items := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("browse_catalog = %v, want the one seeded item", got)
	}
	first := items[0].(map[string]any)
	want := map[string]any{
		"catalogItemId": h.itemID, "title": "Study kettle", "lengthMm": 220.0, "widthMm": 160.0,
		"heightMm": 180.0, "weightG": 900.0, "hazmat": false, "fragile": false, "state": "ACTIVE",
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("browse_catalog item = %v, want %v", first, want)
	}
	if _, has := got["nextCursor"]; has {
		t.Fatalf("a single-item page must not carry nextCursor: %v", got)
	}
}

func TestBrowseCatalogPagesWithTheCursor(t *testing.T) {
	h := newHarness(t)
	h.createItem(t)
	h.createItem(t)
	first := h.ok(t, "browse_catalog", map[string]any{"limit": 1})
	cursor, _ := first["nextCursor"].(string)
	if len(first["items"].([]any)) != 1 || cursor == "" {
		t.Fatalf("first page = %v", first)
	}
	second := h.ok(t, "browse_catalog", map[string]any{"limit": 1, "after": cursor})
	items := second["items"].([]any)
	if len(items) != 1 || second["nextCursor"] != nil {
		t.Fatalf("second page = %v", second)
	}
	if first["items"].([]any)[0].(map[string]any)["catalogItemId"] == items[0].(map[string]any)["catalogItemId"] {
		t.Fatal("the pages overlap")
	}
}

func TestBrowseCatalogErrors(t *testing.T) {
	h := newHarness(t)
	h.createItem(t)
	failWith(t, h.session, "browse_catalog", map[string]any{"limit": 9999}, "validation-failed")
	failWith(t, h.session, "browse_catalog", map[string]any{"limit": -1}, "validation-failed")
}

func TestGetCatalogItem(t *testing.T) {
	h := seededWorld(t)
	got := h.ok(t, "get_catalog_item", map[string]any{"catalogItemId": h.itemID})
	if got["catalogItemId"] != h.itemID || got["title"] != "Study kettle" || got["state"] != "ACTIVE" {
		t.Fatalf("get_catalog_item = %v", got)
	}
}

func TestGetCatalogItemErrors(t *testing.T) {
	h := newHarness(t)
	h.createItem(t)
	failWith(t, h.session, "get_catalog_item", map[string]any{"catalogItemId": "item-nope"}, "not-found")
}

func TestListListings(t *testing.T) {
	h := seededWorld(t)
	all := h.ok(t, "list_listings", map[string]any{})
	listings := all["listings"].([]any)
	if len(listings) != 2 {
		t.Fatalf("list_listings = %v, want the two seeded listings", all)
	}
	byNode := h.ok(t, "list_listings", map[string]any{"nodeId": "node-a"})
	mine := byNode["listings"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["listingId"] != h.liveID {
		t.Fatalf("nodeId filter = %v", byNode)
	}
	live := mine[0].(map[string]any)
	avail := live["availability"].(map[string]any)
	if live["state"] != "LIVE" || live["catalogItemId"] != h.itemID || avail["availableQty"] != 12.0 ||
		avail["basis"] != "PHYSICAL" || avail["feedId"] != "feed-7" || avail["asOf"] != "2026-10-23T12:00:00Z" {
		t.Fatalf("the live listing = %v", live)
	}
	suppressed := h.ok(t, "list_listings", map[string]any{"nodeId": "node-b"})["listings"].([]any)[0].(map[string]any)
	if suppressed["state"] != "SUPPRESSED" || suppressed["suppressionReason"] != "NODE_NOT_SELLABLE" {
		t.Fatalf("the suppressed listing = %v", suppressed)
	}
	if got := h.ok(t, "list_listings", map[string]any{"catalogItemId": "item-none"}); len(got["listings"].([]any)) != 0 {
		t.Fatalf("catalogItemId filter = %v", got)
	}
}

func TestListListingsPagesWithTheCursor(t *testing.T) {
	h := seededWorld(t)
	first := h.ok(t, "list_listings", map[string]any{"limit": 1})
	cursor, _ := first["nextCursor"].(string)
	if len(first["listings"].([]any)) != 1 || cursor == "" {
		t.Fatalf("first page = %v", first)
	}
	second := h.ok(t, "list_listings", map[string]any{"limit": 1, "after": cursor})
	if len(second["listings"].([]any)) != 1 || second["nextCursor"] != nil {
		t.Fatalf("second page = %v", second)
	}
}

func TestListListingsErrors(t *testing.T) {
	h := newHarness(t)
	h.createItem(t)
	failWith(t, h.session, "list_listings", map[string]any{"limit": 501}, "validation-failed")
	failWith(t, h.session, "list_listings", map[string]any{"nodeId": "node-a", "limit": -3}, "validation-failed")
}

func TestGetInventoryFeed(t *testing.T) {
	h := seededWorld(t)
	got := h.ok(t, "get_inventory_feed", map[string]any{"feedId": "feed-7"})
	want := map[string]any{
		"feedId": "feed-7", "nodeId": "node-a", "fullUpdate": false, "asOf": "2026-10-23T12:00:00Z",
		"state": "APPLIED", "lineCount": 1.0, "appliedCount": 1.0, "staleCount": 0.0, "zeroedCount": 0.0,
		"receivedAt": "2026-10-23T12:00:00Z", "appliedAt": "2026-10-23T12:00:00Z",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("get_inventory_feed = %v, want %v", got, want)
	}
	if _, has := got["failureCode"]; has {
		t.Fatalf("an APPLIED feed has no failureCode: %v", got)
	}
}

func TestGetInventoryFeedErrors(t *testing.T) {
	h := seededWorld(t)
	failWith(t, h.session, "get_inventory_feed", map[string]any{"feedId": "feed-nope"}, "not-found")
}

func TestReadsEmitNothing(t *testing.T) {
	h := seededWorld(t)
	before := h.store.Counts().OutboxEvents
	if before == 0 {
		t.Fatal("the seeded world published no event; the sensor below would pass vacuously")
	}
	h.ok(t, "browse_catalog", map[string]any{})
	h.ok(t, "get_catalog_item", map[string]any{"catalogItemId": h.itemID})
	h.ok(t, "list_listings", map[string]any{})
	h.ok(t, "get_inventory_feed", map[string]any{"feedId": "feed-7"})
	if after := h.store.Counts().OutboxEvents; after != before {
		t.Fatalf("outbox rows %d -> %d: a read tool emitted an event", before, after)
	}
}
