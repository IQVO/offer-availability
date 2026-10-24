import { Link, useParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { getCatalogItem, listListings } from "../api";
import { Facts, ListingStatePill, PageHeader, Stack, availabilityLabel, suppressionLabel } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { dimensions, grams } from "../lib";

/** One catalogue item (GET /catalog/{catalogItemId}) with the listings that
 *  sell it (GET /listings?catalogItemId=): where the network can fulfil it,
 *  and the availability the vendors' feeds last reported. Read-only. */
export function CatalogItemRoute() {
  const { catalogItemId = "" } = useParams();
  const item = useRequest(() => getCatalogItem(catalogItemId), `item|${catalogItemId}`);
  const listings = useRequest(() => listListings({ catalogItemId, limit: 50 }), `item-listings|${catalogItemId}`);

  return (
    <Stack gap={5}>
      <PageHeader
        title={`Item ${catalogItemId}`}
        subtitle="the item's footprint and state, and every node that lists it"
      />
      <Card title="Item">
        <RequestView state={item} what="the item" whenIdle="Enter an item id.">
          {(it) => (
            <Facts
              items={[
                { label: "Title", value: it.title },
                { label: "Dimensions", value: dimensions(it) },
                { label: "Weight", value: grams(it.weightG) },
                { label: "Hazmat", value: it.hazmat ? "yes" : "no" },
                { label: "Fragile", value: it.fragile ? "yes" : "no" },
                { label: "State", value: it.state },
              ]}
            />
          )}
        </RequestView>
      </Card>
      <Card title="Listings of this item">
        <RequestView
          state={listings}
          what="listings"
          isEmpty={(p) => p.listings.length === 0}
          empty="No node lists this item."
        >
          {(p) => (
            <DataTable
              rowKey={(l) => l.listingId}
              rows={p.listings}
              columns={[
                {
                  key: "node",
                  header: "Node",
                  render: (l) => <Link to={`../../listings?nodeId=${encodeURIComponent(l.nodeId)}`}>{l.nodeId}</Link>,
                },
                { key: "state", header: "State", render: (l) => <ListingStatePill state={l.state} /> },
                { key: "reason", header: "Suppressed because", render: (l) => suppressionLabel(l.suppressionReason) },
                { key: "qty", header: "Available", render: (l) => availabilityLabel(l) },
                {
                  key: "feed",
                  header: "Last feed",
                  render: (l) =>
                    l.availability.feedId ? (
                      <Link to={`../../feeds/${encodeURIComponent(l.availability.feedId)}`}>{l.availability.feedId}</Link>
                    ) : (
                      "—"
                    ),
                },
              ]}
            />
          )}
        </RequestView>
      </Card>
    </Stack>
  );
}
