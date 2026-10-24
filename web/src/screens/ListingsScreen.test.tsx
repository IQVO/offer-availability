import { MemoryRouter } from "react-router-dom";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ListingsScreen } from "./ListingsScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { listing } from "../test/fixtures";

function renderScreen(at = "/listings") {
  return render(
    <MemoryRouter initialEntries={[at]}>
      <ListingsScreen />
    </MemoryRouter>,
  );
}

describe("ListingsScreen", () => {
  it("shows the loading state, then the listings of the first page", async () => {
    mockApi({ "GET /listings": pending() });
    renderScreen();
    expect(screen.getByRole("status")).toHaveTextContent("Loading listings");
  });

  it("lists listings with state, suppression reason, availability and feed link", async () => {
    mockApi({
      "GET /listings": json({
        listings: [
          listing(),
          listing({ listingId: "lst-2", catalogItemId: "item-2", nodeId: "node-b", state: "SUPPRESSED", suppressionReason: "DISCONTINUED", availability: { availableQty: 0, basis: "UNKNOWN" } }),
        ],
      }),
    });
    renderScreen();
    expect(await screen.findByText("lst-1")).toBeInTheDocument();
    expect(screen.getByText("node-a")).toBeInTheDocument();
    expect(screen.getByText("12 (physical) at 2026-10-24 09:00:00Z")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "feed-7" })).toHaveAttribute("href", "/feeds/feed-7");
    expect(screen.getByRole("link", { name: "item-1" })).toHaveAttribute("href", "/catalog/item-1");
    expect(screen.getByRole("link", { name: "item-2" })).toHaveAttribute("href", "/catalog/item-2");
    expect(screen.getByText("DISCONTINUED")).toBeInTheDocument();
    expect(document.querySelector('[data-status="SUPPRESSED"]')).not.toBeNull();
    expect(document.querySelector('[data-status="LIVE"]')).not.toBeNull();
  });

  it("says so when nothing matches", async () => {
    mockApi({ "GET /listings": json({ listings: [] }) });
    renderScreen();
    expect(await screen.findByText("No listing matches these filters.")).toBeInTheDocument();
  });

  it("shows the problem of a failed list", async () => {
    mockApi({ "GET /listings": problem(422, "validation-failed", "Validation failed", "limit must be between 1 and 500") });
    renderScreen();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Validation failed");
    expect(alert).toHaveTextContent("limit must be between 1 and 500");
  });

  it("applies the node and item filters as query parameters and restarts at page 1", async () => {
    const api = mockApi({ "GET /listings": json({ listings: [listing()], nextCursor: "lst-2" }) });
    renderScreen();
    await screen.findByText("lst-1");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await waitFor(() => expect(api.calls).toHaveLength(2));
    await user.type(screen.getByLabelText("Node"), "node-a");
    await user.type(screen.getByLabelText("Catalogue item"), "item-1");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(api.calls).toHaveLength(3));
    expect(Object.fromEntries(api.calls[2].query)).toEqual({ limit: "25", nodeId: "node-a", catalogItemId: "item-1" });
  });

  it("refuses a malformed node filter before asking", async () => {
    const api = mockApi({ "GET /listings": json({ listings: [] }) });
    renderScreen();
    await screen.findByText("No listing matches these filters.");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Node"), "a/b");
    expect(screen.getByRole("button", { name: "Apply filters" })).toBeDisabled();
    expect(screen.getByText(/cannot contain/)).toBeInTheDocument();
    expect(api.calls).toHaveLength(1);
  });

  it("seeds the node filter from the URL and asks with it at once", async () => {
    const api = mockApi({ "GET /listings": json({ listings: [listing()] }) });
    renderScreen("/listings?nodeId=node-a");
    await screen.findByText("lst-1");
    expect(Object.fromEntries(api.calls[0].query)).toEqual({ limit: "25" });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(api.calls).toHaveLength(2));
    expect(Object.fromEntries(api.calls[1].query)).toEqual({ limit: "25", nodeId: "node-a" });
  });
});
