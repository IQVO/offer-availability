import type { CatalogItem, InventoryFeed, Listing } from "../types";

export function catalogItem(over: Partial<CatalogItem> = {}): CatalogItem {
  return {
    catalogItemId: "item-1",
    title: "Study kettle",
    lengthMm: 220,
    widthMm: 160,
    heightMm: 180,
    weightG: 900,
    hazmat: false,
    fragile: true,
    state: "ACTIVE",
    ...over,
  };
}

export function listing(over: Partial<Listing> = {}): Listing {
  return {
    listingId: "lst-1",
    catalogItemId: "item-1",
    nodeId: "node-a",
    state: "LIVE",
    availability: {
      availableQty: 12,
      basis: "PHYSICAL",
      asOf: "2026-10-24T09:00:00Z",
      feedId: "feed-7",
    },
    ...over,
  };
}

export function feed(over: Partial<InventoryFeed> = {}): InventoryFeed {
  return {
    feedId: "feed-7",
    nodeId: "node-a",
    fullUpdate: false,
    asOf: "2026-10-24T09:00:00Z",
    state: "APPLIED",
    lineCount: 1,
    appliedCount: 1,
    staleCount: 0,
    zeroedCount: 0,
    receivedAt: "2026-10-24T09:00:01Z",
    appliedAt: "2026-10-24T09:00:02Z",
    ...over,
  };
}
