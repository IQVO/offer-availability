import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { CatalogScreen } from "./screens/CatalogScreen";
import { CatalogItemRoute } from "./screens/CatalogItemScreen";
import { ListingsScreen } from "./screens/ListingsScreen";
import { FeedLookupScreen, FeedRoute } from "./screens/FeedScreen";

const SUB_NAV = [
  { to: "catalog", label: "Catalogue" },
  { to: "listings", label: "Listings" },
  { to: "feeds", label: "Inventory feeds" },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/offer-availability/*">`. A relative `<NavLink>` rendered straight in
 *  that splat route resolves against the FULL current URL (react-router 7 uses
 *  the last path-contributing match's pathname, splat included), so from
 *  /offer-availability/listings the link `catalog` would point at
 *  /offer-availability/listings/catalog. A pathless layout route does not help
 *  (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as the interleaving and returns
 *  remotes; App.test.tsx mounts under the host's splat route to prove it.) */
function AvailabilityLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Offer & availability sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as offer_availability_mfe/App via Module Federation. Takes NO
 *  props: the console mounts it under `/offer-availability/*` and provides
 *  the BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`.
 *  Routes here are RELATIVE, so the component works identically under that
 *  prefix (in the shell) or at / (standalone dev, see main.tsx). It calls
 *  ONLY this context's own API (/api/offer-availability), and it READS only:
 *  creating items and listings and applying feeds are API/vendor operations,
 *  not console ones (ADR 0008).
 *
 *   - (index): redirects to the catalogue.
 *   - catalog: the catalogue, one keyset page at a time.
 *   - catalog/:catalogItemId: one item, with its listings one click away.
 *   - listings: the listings of a node and/or item (filters + pages).
 *   - feeds: a feed lookup; feeds/:feedId: one feed and its outcome. */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<AvailabilityLayout />}>
        <Route index element={<Navigate to="catalog" replace />} />
        <Route path="catalog" element={<CatalogScreen />} />
        <Route path="catalog/:catalogItemId" element={<CatalogItemRoute />} />
        <Route path="listings" element={<ListingsScreen />} />
        <Route path="feeds" element={<FeedLookupScreen />} />
        <Route path="feeds/:feedId" element={<FeedRoute />} />
      </Route>
    </Routes>
  );
}
