import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { getInventoryFeed } from "../api";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { Facts, FeedStatePill, PageHeader, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, utcLabel } from "../lib";

/** Feed lookup: GET /inventory-feeds/{feedId}. The feed's outcome is the
 *  answer to "why did this node's numbers move": APPLIED with its counters,
 *  SUPERSEDED by a newer feed, or FAILED with the invariant that broke. */
export function FeedLookupScreen() {
  const navigate = useNavigate();
  const [feedId, setFeedId] = useState("");
  const problem = idProblem("feed", feedId.trim());

  return (
    <Stack gap={5}>
      <PageHeader
        title="Inventory feeds"
        subtitle="offer-availability · one vendor feed and its outcome: applied, superseded or failed"
      />
      <Card title="Lookup">
        <Form
          label="Feed lookup"
          onSubmit={() => navigate(`../feeds/${encodeURIComponent(feedId.trim())}`)}
        >
          <FormRow>
            <TextField
              label="Feed id"
              value={feedId}
              onChange={setFeedId}
              placeholder="feed-7"
              hint={problem ?? "The id the vendor sent the feed under"}
            />
            <SubmitButton disabled={problem !== null}>Read the feed</SubmitButton>
          </FormRow>
        </Form>
      </Card>
    </Stack>
  );
}

/** One feed and its outcome (GET /inventory-feeds/{feedId}). */
export function FeedRoute() {
  const { feedId = "" } = useParams();
  const feed = useRequest(feedId ? () => getInventoryFeed(feedId) : null, `feed|${feedId}`);

  return (
    <Stack gap={5}>
      <PageHeader title={`Feed ${feedId}`} subtitle="what the vendor sent, and how it settled" />
      <Card title="Outcome">
        <RequestView state={feed} what="the feed" whenIdle="Enter a feed id.">
          {(f) => (
            <Stack gap={4}>
              {f.state === "FAILED" && (
                <div
                  role="note"
                  style={{
                    color: "var(--wh-color-status-danger)",
                    background: "var(--wh-color-status-danger-bg)",
                    borderRadius: "var(--wh-radius-md)",
                    padding: "10px 14px",
                    fontSize: "var(--wh-font-size-sm)",
                    fontWeight: 600,
                  }}
                >
                  This feed FAILED: {f.failureCode}. Nothing was applied; the vendor may send the same feed id again.
                </div>
              )}
              <Facts
                items={[
                  { label: "Feed", value: f.feedId },
                  { label: "Node", value: f.nodeId },
                  { label: "State", value: <FeedStatePill state={f.state} /> },
                  { label: "Full update", value: f.fullUpdate ? "yes (omitted listings were zeroed)" : "no" },
                  { label: "As of", value: utcLabel(f.asOf) },
                  {
                    label: "Counters",
                    value: `${f.appliedCount} applied · ${f.staleCount} stale · ${f.zeroedCount} zeroed of ${f.lineCount} lines`,
                  },
                  { label: "Received", value: utcLabel(f.receivedAt) },
                  { label: "Applied", value: f.appliedAt ? utcLabel(f.appliedAt) : "—" },
                  { label: "Failure code", value: f.failureCode ?? "—" },
                ]}
              />
            </Stack>
          )}
        </RequestView>
      </Card>
    </Stack>
  );
}
