# ADR 0012: Agent authority levels and complete MCP surface

## Status

Accepted (2026-11-02). Applies the fleet decision "Agentic Standard v2"
(`docs/architecture/agentic-standard.md` and the fleet rule
`agents/fleet/agent-authority.md` in IQVO/warehouse-docs) to this context.
Extends [ADR 0007](./0007-mcp-server-adoption.md) (the read-only MCP server)
and [ADR 0010](./0010-a2a-inbound-adapter.md) (the read-only A2A agent);
both charters stay read-only, and nothing changes for
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md).

## Context

An agent host mounting this server alongside the fleet's other MCP servers had
three gaps:

1. **Nothing declared how much an agent may do with a tool.** The MCP
   `ReadOnlyHint` annotation and the A2A `read` tag say *whether* a call
   changes state, not *what level of authority* the call represents, so a
   host cannot gate on it uniformly across the fleet.
2. **The surface was tools-only.** No resource let a host load bounded
   context data without a tool call, and no prompt encoded this context's
   operating procedure, so every client re-invented it.
3. **The Streamable HTTP handler ran stateful by default**, which advertises
   only the 2025-11-25 protocol revision and older: a client speaking the
   2026-07-28 per-request handshake (`server/discover`) was refused with
   `-32022`.

## Decision

### Authority levels, declared where the surface is registered

Authority levels are the fleet strings `L1`..`L4`. This context is read-only
on both agent surfaces (ADR 0007, ADR 0010), so every tool and skill is `L1`:

| Surface | Name | Level | Why |
|---|---|---|---|
| MCP | `browse_catalog` | `L1` | reads facts, changes nothing |
| MCP | `get_catalog_item` | `L1` | reads facts, changes nothing |
| MCP | `list_listings` | `L1` | reads facts, changes nothing |
| MCP | `get_inventory_feed` | `L1` | reads facts, changes nothing |
| A2A | `check_availability` | `L1` | reads; the same facts composed for a goal |
| A2A | `feed_outcome` | `L1` | reads one feed's outcome |

- **MCP** declares the level in each tool's `Meta` under
  `com.warehouse/authority` (`internal/adapters/inbound/mcp/authority.go`
  pins the fleet shape: `authorityMetaKey`, `authorityL1..L4`,
  `authorityMeta()`); it is returned verbatim in `tools/list` `_meta` and is
  pinned by `testdata/tool_registry.golden.json`, so a level can only change
  through a reviewed golden edit.
- **A2A** declares it as the skill tag `authority:L1` next to the `read` tag
  on the Agent Card.
- **No `L2`+ exists here.** `L2` (a computed recommendation) would need a
  proposing use case this context does not have; `L3` (a state change) and
  `L4` (an autonomous envelope) are forbidden by the read-only charters
  until an ADR amends them. The level is a label for callers and reviewers;
  **it is not access control** — the surface stays unauthenticated, like
  REST, per the fleet decision.

The static sensor `internal/architecture/mcp_authority_fitness_test.go`
(copied verbatim from `warehouse-harness-template`; the only local
adaptation is dropping one const this package already declares) fails the
build when any `mcp.Tool` literal lacks a readable `Meta` level, when a
write tool is below `L3`, or when any `a2a.AgentSkill` lacks exactly one
`authority:L<n>` tag.

### Stateless MCP

`mcp.NewStreamableHTTPHandler` now receives
`&mcp.StreamableHTTPOptions{Stateless: true}`. No `Mcp-Session-Id` is
issued; `server/discover` answers for the 2026-07-28 revision while a legacy
`initialize` (for example 2025-06-18) still answers 200, so existing
session-based SDK clients keep working. `stateless_test.go` pins the wire
facts with raw POSTs: `server/discover` advertises 2026-07-28,
`tools/list` carries the authority key, and a legacy `initialize` answers
200.

### One resource and one prompt

- **Resource** `warehouse://offer-availability/catalog/{catalogItemId}`
  (JSON): one catalogue item — dimensions, weight, hazmat/fragile flags,
  state, GS1 identifier, packaging hierarchy — built from the same
  `GetCatalogItem` read use case as the tool and `GET /catalog/{id}`. Bounded
  by construction (one aggregate), never a table dump.
- **Prompt** `investigate_availability`: the operator SOP for investigating
  where an item is sold, why a listing is suppressed and what a vendor's feed
  last changed. Its arguments are typed (`catalogItemId`, `nodeId`, both
  optional); it names the read tools but never calls them and hides no
  writes.

Resources and prompts are `L1` by nature; they carry no tool `Meta`.

## Consequences

- `tools/list` gains a `_meta` entry per tool; strict input schemas,
  annotations, names and results are unchanged, and unknown arguments are
  still rejected.
- The Agent Card tags gain `authority:L1`; existing A2A clients ignore
  unknown tags.
- The fleet catalogue generator (IQVO/warehouse-docs) reads the authority
  level from this repo's golden file and card tags.
- A host that gated on `Mcp-Session-Id` must stop: none is issued. The
  MCP docs page's calling guide is updated accordingly.
