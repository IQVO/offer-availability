# ADR 0007: a read-only MCP server

## Status

Accepted (2026-10-24).

## Context

Every other bounded context of the fleet that exposes an HTTP API also ships an
MCP server so that agents (warehouse-ops-agent first) can read its data without
a bespoke REST client. offer-availability had none. The service already has the
read use cases behind its REST adapter (`BrowseCatalog`, `GetCatalogItem`,
`ListListings`, `GetInventoryFeed`), so an MCP adapter is another driving
adapter over them, not new domain.

## Decision

- **A second driving adapter, a separate binary.** `internal/adapters/inbound/mcp`
  is built on the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`
  v1.8.0) and served over Streamable HTTP only (no stdio, no SSE) by `cmd/mcp`,
  mounted at `/mcp` and `/` with an open `/healthz` on `MCP_ADDR` (default
  `:8090`). It is a second deployable beside `cmd/api`; the Helm chart gets an
  mcp Deployment, Service and (off by default) Kong HTTPRoute rewriting
  `/api/mcp/offer-availability` to `/mcp`.
- **Four read tools over the existing queries, nothing else.**
  `browse_catalog(limit?, after?)`, `get_catalog_item(catalogItemId)`,
  `list_listings(nodeId?, catalogItemId?, limit?, after?)` and
  `get_inventory_feed(feedId)`: every GET of `apis/openapi.yaml`, grouped one
  tool per read use case. Arguments and results are lowerCamelCase like the
  REST contract; typed errors carry the REST problem slug (`not-found`,
  `validation-failed`) as the prefix of the tool error. Creating an item,
  discontinuing one, creating a listing and applying a feed stay REST-only.
- **Read-only is structural.** The adapter's `Deps` holds the four read use
  cases and nothing that can write; the adapter and `cmd/mcp` import no event
  sink, no outbox, no Kafka adapter, no consumer and no janitor. `cmd/mcp`
  never dials Kafka and never starts the relay: those stay in `cmd/api`.
  `TestMCPAdapterDependencyRule` (internal/architecture) pins the imports and
  that nothing else depends on the adapter; `TestToolSurface` pins the exact
  tool set (budget 4), the read-only annotations and the absence of write
  verbs in tool names; `TestToolRegistryGolden` pins the whole advertised
  registry (input AND output schemas, byte for byte); the integration test
  proves the binary reads what `cmd/api`'s use cases wrote in a real Postgres
  and adds no outbox row. Optional tool inputs are published as optional
  (`omitempty` on the json tag; `TestMCPOptionalToolInputsCarryOmitempty`).
- **No sibling calls, no auth.** The server reads this context's own tables
  only, calls no other context over REST or MCP, and has no auth layer (the
  fleet-wide revert of 2026-09-11; `TestNoAuthMiddlewareReintroduced` scans
  every inbound tree, this one included). Access control is the in-cluster
  ClusterIP boundary.
- **Boot.** `DATABASE_URL` is mandatory (no in-memory mode); the binary runs
  the idempotent embedded migrations on start, exactly like `cmd/api` (with
  `MIGRATIONS_DATABASE_URL` for a direct DSN behind PgBouncer), wrapping the
  migrations and the first ping in `internal/bootretry`, so it can start
  against a fresh database before `cmd/api` has. Graceful shutdown drains the
  HTTP server on SIGINT/SIGTERM.

## Consequences

- Agents can answer "which nodes can ship item X, what availability do they
  report, and what happened to feed Y" without REST glue.
- Adding a tool is a reviewed act: the tool budget, the curated list and the
  golden file all change together (an amendment of this ADR).
- The write side stays exactly where it was: REST with `Idempotency-Key`, the
  transactional outbox and the consumers of ADR 0005.
