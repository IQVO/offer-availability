import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { catalogItem, feed, listing } from "./test/fixtures";

// The console mounts the remote under its own splat route; the remote's
// relative links must resolve against the mount point, not the full URL.
function renderUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/offer-availability/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

const ROUTES = {
  "GET /catalog": json({ items: [catalogItem()] }),
  "GET /catalog/item-1": json(catalogItem()),
  "GET /listings": json({ listings: [listing()] }),
  "GET /inventory-feeds/feed-7": json(feed()),
};

describe("App", () => {
  it("redirects the mount point to the catalogue", async () => {
    mockApi(ROUTES);
    renderUnderHost("/offer-availability");
    expect(await screen.findByRole("heading", { name: "Catalogue" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Catalogue" })).toHaveAttribute("href", "/offer-availability/catalog");
  });

  it("keeps the sub-nav at the mount point from a deep route", async () => {
    mockApi(ROUTES);
    renderUnderHost("/offer-availability/catalog/item-1");
    await screen.findByRole("heading", { name: "Item item-1" });
    expect(screen.getByRole("link", { name: "Listings" })).toHaveAttribute("href", "/offer-availability/listings");
    expect(screen.getByRole("link", { name: "Inventory feeds" })).toHaveAttribute("href", "/offer-availability/feeds");
  });

  it("navigates between the sections and into an item, its listings and its feed", async () => {
    mockApi(ROUTES);
    renderUnderHost("/offer-availability/catalog");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: "item-1" }));
    await screen.findByRole("heading", { name: "Item item-1" });
    await user.click(screen.getByRole("link", { name: "Listings" }));
    expect(await screen.findByRole("heading", { name: "Listings" })).toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "feed-7" }));
    await screen.findByRole("heading", { name: "Feed feed-7" });
    expect(screen.getByText("1 applied · 0 stale · 0 zeroed of 1 lines")).toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "Inventory feeds" }));
    expect(await screen.findByRole("heading", { name: "Inventory feeds" })).toBeInTheDocument();
  });
});
