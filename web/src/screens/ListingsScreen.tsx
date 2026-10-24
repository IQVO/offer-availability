import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listListings } from "../api";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { ListingStatePill, PageHeader, Stack, availabilityLabel, suppressionLabel } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { optionalIdProblem } from "../lib";

interface Filters {
  nodeId: string;
  catalogItemId: string;
}

const INITIAL: Filters = { nodeId: "", catalogItemId: "" };

/**
 * Which node sells what (GET /listings): filter by node and/or catalogue
 * item (both optional, combined with AND), one keyset page at a time. Each
 * listing says LIVE or SUPPRESSED (and why), plus the availability the
 * vendor's feed last set. Read-only: creating a listing is an API operation.
 */
export function ListingsScreen() {
  const [params] = useSearchParams();
  const [draft, setDraft] = useState<Filters>({
    nodeId: params.get("nodeId") ?? "",
    catalogItemId: params.get("catalogItemId") ?? "",
  });
  const [applied, setApplied] = useState<Filters>(INITIAL);
  // cursors[i] is the cursor of page i+1 ("" = the first page).
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const page = useRequest(
    () =>
      listListings({
        limit: 25,
        after: cursor || undefined,
        nodeId: applied.nodeId || undefined,
        catalogItemId: applied.catalogItemId || undefined,
      }),
    `listings|${applied.nodeId}|${applied.catalogItemId}|${cursor}`,
  );

  const nodeProblem = optionalIdProblem("node", draft.nodeId.trim());
  const itemProblem = optionalIdProblem("catalogue item", draft.catalogItemId.trim());
  const apply = () => {
    if (nodeProblem || itemProblem) return;
    setApplied({ nodeId: draft.nodeId.trim(), catalogItemId: draft.catalogItemId.trim() });
    setCursors([""]);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Listings"
        subtitle="offer-availability · which node sells which item, and the availability a vendor's feed last reported"
      />
      <Card title="Filters">
        <Form label="Listing filters" onSubmit={apply}>
          <FormRow>
            <TextField
              label="Node"
              value={draft.nodeId}
              onChange={(v) => setDraft({ ...draft, nodeId: v })}
              placeholder="Any node"
              hint={nodeProblem ?? "Optional"}
            />
            <TextField
              label="Catalogue item"
              value={draft.catalogItemId}
              onChange={(v) => setDraft({ ...draft, catalogItemId: v })}
              placeholder="Any item"
              hint={itemProblem ?? "Optional"}
            />
            <SubmitButton disabled={nodeProblem !== null || itemProblem !== null}>Apply filters</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <Card
        title={`Page ${cursors.length}`}
        actions={
          <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
            <SubmitButton
              type="button"
              secondary
              disabled={cursors.length === 1 || page.status === "loading"}
              onClick={() => setCursors(cursors.slice(0, -1))}
            >
              Previous page
            </SubmitButton>
            <SubmitButton
              type="button"
              secondary
              disabled={page.status !== "success" || !page.data.nextCursor}
              onClick={() => {
                if (page.status === "success" && page.data.nextCursor) {
                  setCursors([...cursors, page.data.nextCursor]);
                }
              }}
            >
              Next page
            </SubmitButton>
          </div>
        }
      >
        <RequestView
          state={page}
          what="listings"
          isEmpty={(p) => p.listings.length === 0}
          empty={cursors.length === 1 ? "No listing matches these filters." : "No more listings on this page."}
        >
          {(p) => (
            <DataTable
              rowKey={(l) => l.listingId}
              rows={p.listings}
              columns={[
                { key: "listing", header: "Listing", render: (l) => l.listingId },
                { key: "node", header: "Node", render: (l) => l.nodeId },
                {
                  key: "item",
                  header: "Item",
                  render: (l) => <Link to={`../catalog/${encodeURIComponent(l.catalogItemId)}`}>{l.catalogItemId}</Link>,
                },
                { key: "state", header: "State", render: (l) => <ListingStatePill state={l.state} /> },
                { key: "reason", header: "Suppressed because", render: (l) => suppressionLabel(l.suppressionReason) },
                { key: "qty", header: "Available", render: (l) => availabilityLabel(l) },
                {
                  key: "feed",
                  header: "Last feed",
                  render: (l) =>
                    l.availability.feedId ? (
                      <Link to={`../feeds/${encodeURIComponent(l.availability.feedId)}`}>{l.availability.feedId}</Link>
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
