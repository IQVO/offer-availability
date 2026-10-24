import { describe, expect, it } from "vitest";
import { ApiError, browseCatalog, getCatalogItem, getInventoryFeed, listListings, toApiError, withQuery } from "./api";
import { OFFER_API_BASE } from "./config";
import { json, mockApi, problem } from "./test/fetchMock";
import { catalogItem, feed, listing } from "./test/fixtures";

describe("withQuery", () => {
  it("drops empty values and encodes the rest", () => {
    expect(withQuery("/listings", { limit: 25, nodeId: "", catalogItemId: undefined, after: "itm 1" })).toBe(
      "/listings?limit=25&after=itm+1",
    );
    expect(withQuery("/catalog", {})).toBe("/catalog");
    expect(withQuery("/catalog")).toBe("/catalog");
  });
});

describe("the API base", () => {
  it("is this context's own /api/offer-availability or the developer port, never a sibling", () => {
    expect(OFFER_API_BASE).toBe("http://localhost:8080");
  });
});

describe("requests", () => {
  it("browses the catalogue with the page as query parameters and no custom header", async () => {
    const api = mockApi({ "GET /catalog": json({ items: [catalogItem()], nextCursor: "item-2" }) });
    const page = await browseCatalog({ limit: 25, after: "item-1" });
    expect(page.nextCursor).toBe("item-2");
    expect(page.items).toHaveLength(1);
    const call = api.calls[0];
    expect(call.url.startsWith(OFFER_API_BASE + "/catalog?")).toBe(true);
    expect(Object.fromEntries(call.query)).toEqual({ limit: "25", after: "item-1" });
    expect(call.headers).toEqual({});
  });

  it("issues GETs only -- the remote is read-only (ADR 0007)", async () => {
    const api = mockApi({
      "GET /catalog": json({ items: [] }),
      "GET /listings": json({ listings: [] }),
      "GET /catalog/item-1": json(catalogItem()),
      "GET /inventory-feeds/feed-7": json(feed()),
    });
    await browseCatalog({});
    await listListings({ nodeId: "node-a" });
    await getCatalogItem("item-1");
    await getInventoryFeed("feed-7");
    expect(api.calls.map((c) => c.method)).toEqual(["GET", "GET", "GET", "GET"]);
  });

  it("normalizes a missing item list and an empty cursor", async () => {
    mockApi({ "GET /catalog": json({ nextCursor: "" }) });
    expect(await browseCatalog({})).toEqual({ items: [], nextCursor: undefined });
  });

  it("lists listings with both filters as query parameters", async () => {
    const api = mockApi({ "GET /listings": json({ listings: [listing()] }) });
    await listListings({ nodeId: "node-a", catalogItemId: "item-1", limit: 50 });
    expect(Object.fromEntries(api.calls[0].query)).toEqual({
      nodeId: "node-a",
      catalogItemId: "item-1",
      limit: "50",
    });
  });

  it("reads one item and one feed by escaped id", async () => {
    const api = mockApi({
      "GET /catalog/item%201": json(catalogItem({ catalogItemId: "item 1" })),
      "GET /inventory-feeds/feed%207": json(feed({ feedId: "feed 7" })),
    });
    expect((await getCatalogItem("item 1")).catalogItemId).toBe("item 1");
    expect((await getInventoryFeed("feed 7")).feedId).toBe("feed 7");
    expect(api.calls.map((c) => c.path)).toEqual(["/catalog/item%201", "/inventory-feeds/feed%207"]);
  });

  it("calls nothing but this context's API", async () => {
    const api = mockApi({
      "GET /catalog": json({ items: [] }),
      "GET /listings": json({ listings: [] }),
    });
    await browseCatalog({});
    await listListings({});
    for (const c of api.calls) expect(c.url.startsWith(OFFER_API_BASE + "/")).toBe(true);
  });
});

describe("errors", () => {
  it("keeps the problem's title, detail and slug", async () => {
    mockApi({
      "GET /catalog/item-none": problem(404, "not-found", "Resource not found", "no catalogue item item-none"),
    });
    const err = await getCatalogItem("item-none").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect([e.status, e.slug, e.title, e.detail]).toEqual([404, "not-found", "Resource not found", "no catalogue item item-none"]);
    expect(e.message).toBe("no catalogue item item-none");
  });

  it("falls back to the status text for a body that is not a problem", async () => {
    mockApi({ "GET /catalog/x": new Response("<html>", { status: 502, statusText: "Bad Gateway" }) });
    const e = (await getCatalogItem("x").catch((err: unknown) => err)) as ApiError;
    expect([e.status, e.title, e.slug, e.problem]).toEqual([502, "502 Bad Gateway", "", null]);
  });

  it("turns a network failure into an ApiError with status 0", async () => {
    mockApi({});
    const e = (await getInventoryFeed("x").catch((err: unknown) => err)) as ApiError;
    expect(e.status).toBe(0);
    expect(e.title).toBe("Network error");
    expect(toApiError(new Error("boom")).detail).toBe("boom");
    expect(toApiError("text").detail).toBe("text");
    expect(toApiError(e)).toBe(e);
  });
});
