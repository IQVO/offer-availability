import { MemoryRouter } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { CatalogScreen } from "./CatalogScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { catalogItem } from "../test/fixtures";

function renderScreen() {
  return render(
    <MemoryRouter>
      <CatalogScreen />
    </MemoryRouter>,
  );
}

describe("CatalogScreen", () => {
  it("shows the loading state first", async () => {
    mockApi({ "GET /catalog": pending() });
    renderScreen();
    expect(screen.getByRole("status")).toHaveTextContent("Loading catalogue items");
  });

  it("lists the items of the page with relative links to the detail", async () => {
    mockApi({
      "GET /catalog": json({ items: [catalogItem(), catalogItem({ catalogItemId: "item-2", title: "Study torch", lengthMm: 90, weightG: 120, fragile: false })] }),
    });
    renderScreen();
    const link = await screen.findByRole("link", { name: "item-1" });
    expect(link).toHaveAttribute("href", "/catalog/item-1");
    expect(screen.getByText("Study torch")).toBeInTheDocument();
    expect(screen.getByText("220 × 160 × 180 mm · 900 g")).toBeInTheDocument();
    expect(screen.getByText("90 × 160 × 180 mm · 120 g")).toBeInTheDocument();
    expect(screen.getAllByText("fragile")).toHaveLength(1);
  });

  it("says so when the catalogue is empty", async () => {
    mockApi({ "GET /catalog": json({ items: [] }) });
    renderScreen();
    expect(await screen.findByText("The catalogue is empty.")).toBeInTheDocument();
  });

  it("shows the problem of a failed page", async () => {
    mockApi({ "GET /catalog": problem(500, "internal-error", "Internal server error", "an unexpected error occurred") });
    renderScreen();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Internal server error");
    expect(alert).toHaveTextContent("an unexpected error occurred");
  });

  it("walks forward with the nextCursor and back without asking the API to", async () => {
    const api = mockApi({
      "GET /catalog": (call) =>
        call.query.get("after") === "item-2"
          ? json({ items: [catalogItem({ catalogItemId: "item-page2" })] })
          : json({ items: [catalogItem({ catalogItemId: "item-page1" })], nextCursor: "item-2" }),
    });
    renderScreen();
    await screen.findByRole("link", { name: "item-page1" });
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByRole("link", { name: "item-page2" });
    expect(api.calls[1].query.get("after")).toBe("item-2");
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Previous page" }));
    await screen.findByRole("link", { name: "item-page1" });
    expect(screen.queryByText("item-page2")).toBeNull();
  });
});
