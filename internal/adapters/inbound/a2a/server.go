// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the agent ecosystem as a third driving adapter over the same
// application-layer READ use cases the REST and MCP adapters use. It is built
// on the official A2A Go SDK (a2aproject/a2a-go, v2) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per docs/adr/0010-a2a-inbound-adapter.md, A2A is the DELEGATION channel: an
// agent client sends a goal-shaped skill request as a structured message part
// and this agent answers from its own state through its own use cases. It is
// not a data-sync channel; facts between contexts travel as CloudEvents on
// Kafka (ADR 0004). The surface is READ-ONLY because the MCP surface is
// (ADR 0007): the write side stays on REST with Idempotency-Key and the
// transactional outbox. Deps therefore holds no write use case at all.
//
// Like the MCP adapter, this package depends inward on the application layer
// and the domain only, never on an outbound adapter or the REST adapter, and
// nothing else may depend on it (internal/architecture's
// TestA2AAdapterDependencyRule). The composition root (cmd/a2a) wires concrete
// repositories into the use cases. Domain structs never leak across the
// message boundary. There is no auth of any kind (fleet-wide revert
// 2026-09-11).
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/offer-availability/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. The composition root supplies it from
// A2A_AGENT_URL; the card MUST advertise a URL the agent clients can dial
// (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME read use cases the REST and MCP adapters use and
// nothing that can write (docs/adr/0007, docs/adr/0010); the adapter never
// constructs an outbound adapter itself.
type Deps struct {
	GetCatalogItem   *usecases.GetCatalogItem
	ListListings     *usecases.ListListings
	GetInventoryFeed *usecases.GetInventoryFeed
}

// Card builds this agent's Agent Card (docs/adr/0010). The surface is two
// curated, goal-shaped READ skills, not one skill per endpoint: the same
// curation discipline ADR 0007 applied to the MCP tools. The card declares no
// security schemes on purpose: this surface is unauthenticated by the same
// fleet-wide decision as REST and MCP; schemes are added here, not in a
// redesign, if that decision is ever reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "offer-availability",
		Description: "Offer & Availability agent of the retail network (read-only): answers where an item can be sold and how much is available there (the catalogue item, which nodes list it, whether each listing is LIVE or SUPPRESSED and why, and the quantity a vendor's feed last reported), and what became of one vendor inventory feed (applied, superseded or failed, with its counters and failure code). It never creates or discontinues an item, never lists an item on a node and never applies a feed: those stay REST operations of offer-availability.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/offer-availability",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillCheckAvailability,
				Name:        "Check where an item is sold and how much is available",
				Description: "Composed read of this context's own listings, narrowed by catalogItemId and/or nodeId (at least one is required). With catalogItemId the answer also carries that catalogue item (title, dimensions in mm, weightG, hazmat and fragile flags, state ACTIVE or DISCONTINUED) and an unknown item is a not-found problem. Each listing says LIVE or SUPPRESSED (reason NODE_NOT_SELLABLE or DISCONTINUED), its ownerRef when owned by a seller, and the availability a vendor's feed last gave it (availableQty, basis, asOf, feedId). Listings come one keyset page at a time: pass the returned nextCursor as after; limit is 1..500 (default 100).",
				Tags:        []string{"offer-availability", "availability", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"check_availability","arguments":{"catalogItemId":"item-1"}}`,
					`{"skill":"check_availability","arguments":{"nodeId":"node-a","limit":50}}`,
				},
			},
			{
				ID:          skillFeedOutcome,
				Name:        "Read the outcome of one vendor inventory feed",
				Description: "Read one vendor inventory feed by feedId: the node it was sent for (nodeId), whether it was a fullUpdate, its asOf, its state APPLIED, SUPERSEDED or FAILED with the counters (lineCount, appliedCount, staleCount, zeroedCount), receivedAt, appliedAt and, when it FAILED, the failureCode (listing-not-on-node or negative-availability). An unknown feedId is a not-found problem. Reading a feed changes nothing; applying one stays a REST operation.",
				Tags:        []string{"offer-availability", "inventory-feed", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"feed_outcome","arguments":{"feedId":"feed-7"}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both skills are transactional request/response reads, so there is
// no long-running Task lifecycle to track. The response carries the caller's
// contextId (and taskId when the client referenced one) so a delegating
// saga can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler: the only transport this
// agent ships (docs/adr/0010), mirroring ADR 0007's "Streamable HTTP only"
// minimalism for MCP.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
