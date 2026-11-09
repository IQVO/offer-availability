package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// investigateAvailabilitySOP is the operational standard-operating-procedure
// the investigate_availability prompt hands to the model. Per the charter,
// prompts encode how to interpret results, when to escalate, and what "done"
// means — they standardise agent behaviour across clients rather than leaving
// procedure implicit. The prompt names tools but never calls them, and it
// performs no writes (every tool it names is read-only).
const investigateAvailabilitySOP = `You are investigating offer & availability for the retail network: where an item can be sold, whether each node's listing is live, and how much a vendor's feed last said is available. Use only the read-only MCP tools of this context; never assume state. Creating an item, listing it on a node and applying an inventory feed are REST operations a human or integration performs — this context never writes.

Procedure:
1. If you only have a description, browse_catalog (keyset pages: pass the previous page's nextCursor as after) and match on title. If you already have the item's id, get_catalog_item gives its dimensions, weight, hazmat and fragile flags and whether it is DISCONTINUED.
2. Call list_listings with catalogItemId to see every node that sells it (narrow further with nodeId when the question is about one node). Each listing is LIVE or SUPPRESSED with a reason: NODE_NOT_SELLABLE means the node cannot sell at all; DISCONTINUED means the item was discontinued after the listing was created; OWNER_SUSPENDED means the seller that owns the listing (ownerRef) is suspended.
3. Read the availability each listing carries (availableQty, the vendor's basis, asOf and the feedId). When availability looks stale, zero, or surprising, call get_inventory_feed with that feedId to see what became of the feed: APPLIED means the numbers you saw were taken; SUPERSEDED means a newer feed replaced it; FAILED means nothing was taken, with failureCode listing-not-on-node (the vendor named a listing this context does not have) or negative-availability (the vendor sent a negative quantity).

Interpretation:
- A SUPPRESSED listing is not an availability problem: no quantity can be right until the suppression reason is cleared (a node-status or catalogue decision, not a feed issue).
- A FAILED feed with listing-not-on-node usually means the vendor's catalogue of listings drifted from this context's; flag the listing id to the vendor, do not "fix" data.
- availableQty reflects the vendor's last APPLIED feed, not a live count; treat it as a claim with an asOf timestamp.

Escalate to a human when: a feed FAILED and the same failure repeats across feeds, an item must be sellable but every listing is SUPPRESSED, or a decision to discontinue/relist an item is called for. Those are decisions, not reads.

Done means: for the item you were asked about you have reported which nodes list it, each listing's state and suppression reason when suppressed, the availability claim (qty, basis, asOf) where one exists, and what the latest feed did when you had to check it — plus a one-line verdict on why the observed availability is what it is.`

// registerPrompts adds the workflow prompt (the operator SOP for this
// context; Agentic Standard v2, AS-3). Prompts are L1 by nature: they are
// text, they hide no writes and they never call the tools they name.
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "investigate_availability",
		Description: "Standard operating procedure for investigating where an item is sold, why a listing is suppressed and what a vendor's feed last changed — using only this context's read-only tools.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "catalogItemId",
				Description: "The catalogue item to investigate, e.g. item-1; when omitted the SOP starts from browse_catalog.",
				Required:    false,
			},
			{
				Name:        "nodeId",
				Description: "Narrow the listing walk to this node, e.g. node-a; optional.",
				Required:    false,
			},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		sop := investigateAvailabilitySOP
		if item := req.Params.Arguments["catalogItemId"]; item != "" {
			sop = "Investigate catalogue item " + item + ".\n\n" + sop
		}
		if node := req.Params.Arguments["nodeId"]; node != "" {
			sop += "\n\nFocus on node " + node + "."
		}
		return &mcp.GetPromptResult{
			Description: "How to investigate availability: find the item, walk its listings, read each availability claim and check the feed it came from; know when to escalate.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: sop},
			}},
		}, nil
	})
}
