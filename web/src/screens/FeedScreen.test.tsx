import { MemoryRouter, Outlet, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { FeedLookupScreen, FeedRoute } from "./FeedScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { feed } from "../test/fixtures";

function renderLookup() {
  // The lookup's relative `../feeds/<id>` navigation must land on the feed's
  // own route: render both siblings like the App does.
  return render(
    <MemoryRouter initialEntries={["/feeds"]}>
      <Routes>
        <Route path="/" element={<Outlet />}>
          <Route path="feeds" element={<FeedLookupScreen />} />
          <Route path="feeds/:feedId" element={<FeedRoute />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

function renderFeed(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/feeds/:feedId" element={<FeedRoute />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("FeedLookupScreen", () => {
  it("navigates to the feed's own route on submit and shows its outcome", async () => {
    mockApi({ "GET /inventory-feeds/feed-7": json(feed()) });
    renderLookup();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Feed id"), "feed-7");
    await user.click(screen.getByRole("button", { name: "Read the feed" }));
    expect(await screen.findByRole("heading", { name: "Feed feed-7" })).toBeInTheDocument();
  });

  it("refuses a malformed id before navigating", async () => {
    mockApi({ "GET /inventory-feeds/feed-7": json(feed()) });
    renderLookup();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Feed id"), "a/b");
    expect(screen.getByRole("button", { name: "Read the feed" })).toBeDisabled();
    expect(screen.getByText(/cannot contain/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Feed feed-7" })).toBeNull();
  });
});

describe("FeedRoute", () => {
  it("shows an APPLIED feed's outcome and counters", async () => {
    mockApi({ "GET /inventory-feeds/feed-7": json(feed()) });
    renderFeed("/feeds/feed-7");
    expect(await screen.findByRole("heading", { name: "Feed feed-7" })).toBeInTheDocument();
    expect(screen.getByText("1 applied · 0 stale · 0 zeroed of 1 lines")).toBeInTheDocument();
    expect(screen.getByText("2026-10-24 09:00:00Z")).toBeInTheDocument();
    expect(document.querySelector('[data-status="APPLIED"]')).not.toBeNull();
    expect(screen.queryByRole("note")).toBeNull();
  });

  it("names the failure code of a FAILED feed", async () => {
    mockApi({
      "GET /inventory-feeds/feed-9": json(
        feed({ feedId: "feed-9", state: "FAILED", failureCode: "listing-not-on-node", appliedCount: 0, appliedAt: undefined }),
      ),
    });
    renderFeed("/feeds/feed-9");
    const note = await screen.findByRole("note");
    expect(note).toHaveTextContent("This feed FAILED: listing-not-on-node");
    expect(screen.getByText("listing-not-on-node")).toBeInTheDocument();
  });

  it("shows the not-found problem of an unknown feed", async () => {
    mockApi({
      "GET /inventory-feeds/feed-none": problem(404, "not-found", "Resource not found", "no inventory feed feed-none"),
    });
    renderFeed("/feeds/feed-none");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Resource not found");
    expect(alert).toHaveTextContent("no inventory feed feed-none");
  });
});
