import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CatalogItemRoute } from "./CatalogItemScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { catalogItem, listing } from "../test/fixtures";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/catalog/:catalogItemId" element={<CatalogItemRoute />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("CatalogItemRoute", () => {
  it("shows the item's footprint and the listings that sell it", async () => {
    mockApi({
      "GET /catalog/item-1": json(catalogItem()),
      "GET /listings": json({
        listings: [
          listing(),
          listing({ listingId: "lst-2", nodeId: "node-b", state: "SUPPRESSED", suppressionReason: "NODE_NOT_SELLABLE", availability: { availableQty: 0, basis: "UNKNOWN" } }),
        ],
      }),
    });
    renderAt("/catalog/item-1");
    expect(await screen.findByRole("heading", { name: "Item item-1" })).toBeInTheDocument();
    expect(screen.getByText("Study kettle")).toBeInTheDocument();
    expect(screen.getByText("220 × 160 × 180 mm")).toBeInTheDocument();
    expect(screen.getByText("900 g")).toBeInTheDocument();
    expect(screen.getByText("node-a")).toBeInTheDocument();
    expect(screen.getByText("12 (physical) at 2026-10-24 09:00:00Z")).toBeInTheDocument();
    // the listing's last feed links to the feed screen, the node to the listings filter
    expect(screen.getByRole("link", { name: "feed-7" })).toHaveAttribute("href", "/feeds/feed-7");
    expect(screen.getByText("NODE_NOT_SELLABLE")).toBeInTheDocument();
  });

  it("shows the item's GS1 identifiers and packaging hierarchy when it has them", async () => {
    mockApi({
      "GET /catalog/item-1": json(
        catalogItem({
          identifiers: { gtin: "00614141000232" },
          packaging: [
            { level: "CASE", gtin: "10614141000239", unitsPerPack: 12, grossWeightG: 4200, active: true },
            { level: "PALLET", gtin: "20614141000236", childGtin: "10614141000239", unitsPerPack: 60, active: true },
          ],
        }),
      ),
      "GET /listings": json({ listings: [listing({ ownerRef: "seller-42" })] }),
    });
    renderAt("/catalog/item-1");
    expect(await screen.findByText("00614141000232")).toBeInTheDocument();
    expect(screen.getByText(/CASE ×12 \(10614141000239\)/)).toBeInTheDocument();
    expect(screen.getByText(/PALLET ×60/)).toBeInTheDocument();
    expect(screen.getByText("seller-42")).toBeInTheDocument();
  });

  it("says so when no node lists the item", async () => {
    mockApi({
      "GET /catalog/item-1": json(catalogItem()),
      "GET /listings": json({ listings: [] }),
    });
    renderAt("/catalog/item-1");
    expect(await screen.findByText("No node lists this item.")).toBeInTheDocument();
  });

  it("shows the not-found problem of an unknown item", async () => {
    mockApi({
      "GET /catalog/item-none": problem(404, "not-found", "Resource not found", "no catalogue item item-none"),
      "GET /listings": json({ listings: [] }),
    });
    renderAt("/catalog/item-none");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Resource not found");
    expect(alert).toHaveTextContent("no catalogue item item-none");
  });

  it("sends the item filter of the listings read", async () => {
    const api = mockApi({
      "GET /catalog/item-1": json(catalogItem()),
      "GET /listings": json({ listings: [listing()] }),
    });
    renderAt("/catalog/item-1");
    await screen.findByRole("link", { name: "feed-7" });
    expect(Object.fromEntries(api.to("GET /listings")[0].query)).toMatchObject({ catalogItemId: "item-1" });
  });

  it("links carry the encoded ids", async () => {
    mockApi({
      "GET /catalog/item%201": json(catalogItem({ catalogItemId: "item 1" })),
      "GET /listings": json({ listings: [listing({ catalogItemId: "item 1" })] }),
    });
    renderAt("/catalog/item%201");
    expect(await screen.findByRole("heading", { name: "Item item 1" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "feed-7" })).toHaveAttribute("href", "/feeds/feed-7");
  });
});
