import { useState } from "react";
import { Card } from "@warehouse/ui-kit";
import { browseCatalog } from "../api";
import { CatalogTable, PageHeader, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { SubmitButton } from "../components/formkit";
import { useRequest } from "../hooks/useRequest";

/**
 * The catalogue (GET /catalog), one keyset page at a time: every item's
 * title, dimensions, weight, hazmat/fragile flags and state. Paging follows
 * the opaque `nextCursor`; the cursors already visited are kept so
 * "Previous" walks back without the API having to support it.
 */
export function CatalogScreen() {
  // cursors[i] is the cursor of page i+1 ("" = the first page).
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const page = useRequest(
    () => browseCatalog({ limit: 25, after: cursor || undefined }),
    `catalog|${cursor}`,
  );

  return (
    <Stack gap={5}>
      <PageHeader
        title="Catalogue"
        subtitle="offer-availability · what the network can sell, and whether an item is still active"
      />
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
          what="catalogue items"
          isEmpty={(p) => p.items.length === 0}
          empty={cursors.length === 1 ? "The catalogue is empty." : "No more items on this page."}
        >
          {(p) => <CatalogTable items={p.items} itemTo={(id) => `../catalog/${encodeURIComponent(id)}`} />}
        </RequestView>
      </Card>
    </Stack>
  );
}
