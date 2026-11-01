---
id: tools
title: MCP tools
sidebar_label: MCP tools
sidebar_position: 2
---

# MCP tools

`cmd/mcp` is a read-only [Model Context Protocol](https://modelcontextprotocol.io) server over Streamable HTTP
([ADR 0007](/docs/adr/0007-mcp-server-adoption)). It serves `/mcp` and `/` on `MCP_ADDR` (default `:8090`), plus
`GET /healthz`; through Kong the chart route is `/api/mcp/offer-availability`, rewritten to `/mcp`. It reads Postgres
through the same read use cases as REST, starts no relay or consumer and never dials Kafka. There is **no authentication**,
by fleet decision, and no write tool: creating items, listing them on nodes and applying feeds stay REST operations.
`TestToolSurface` pins the exact set and fails the build on any write-verb name; `TestToolRegistryGolden` pins the
registry.

The table below comes from a live `tools/list` against `cmd/mcp` built from this branch (server name
`offer-availability-mcp`, version `1.0.0`). Every tool carries the annotations `readOnlyHint: true`,
`idempotentHint: true` and `openWorldHint: false`. Source: `internal/adapters/inbound/mcp/tools.go`.

| Tool | Input fields | Read/write | Returns |
| --- | --- | --- | --- |
| `browse_catalog` | `limit` (integer, optional, 1..500, omitted or 0 = 100), `after` (string, optional, the previous page's `nextCursor`) | read-only | `items` (catalogue items by `catalogItemId` ascending) and `nextCursor` (absent on the last page) |
| `get_catalog_item` | `catalogItemId` (string, **required**) | read-only | One item: `title`, `lengthMm`, `widthMm`, `heightMm`, `weightG`, `hazmat`, `fragile`, `state` (`ACTIVE` or `DISCONTINUED`), `identifiers.gtin`, `packaging` (per level: `level`, `gtin`, `childGtin`, `unitsPerPack`, `dimsMm`, `grossWeightG`, `active`) |
| `list_listings` | `nodeId` (optional), `catalogItemId` (optional), `limit` (optional, 1..500), `after` (optional) | read-only | `listings` (by `listingId` ascending): `state` `LIVE` or `SUPPRESSED`, `suppressionReason` (`NODE_NOT_SELLABLE` or `DISCONTINUED`), `ownerRef`, `availability` (`availableQty`, `basis`, `asOf`, `feedId`), and `nextCursor` |
| `get_inventory_feed` | `feedId` (string, **required**) | read-only | The feed and its outcome: `nodeId`, `fullUpdate`, `asOf`, `state` (`APPLIED`, `SUPERSEDED`, `FAILED`), `lineCount`, `appliedCount`, `staleCount`, `zeroedCount`, `receivedAt`, `appliedAt`, `failureCode` (`listing-not-on-node` or `negative-availability`) |

All input schemas set `additionalProperties: false`: an unknown argument is refused. Optional inputs carry `omitempty`
(`TestMCPOptionalToolInputsCarryOmitempty`), so omitting them is valid.

## Errors

A failing call returns a normal tool result with `isError: true` and the error text, never a protocol error. Use cases
raise the same classifications as REST: an unknown id is `not-found: not found`, and a `limit` outside 1 to 500 is
`validation-failed`. Anything unrecognised is an internal error whose detail is logged server-side
(`internal/adapters/inbound/mcp/errors.go`).

## Making a call

```sh
curl -si -X POST localhost:8090/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"docs","version":"0"}}}'
```

Take `Mcp-Session-Id` from the response, send `notifications/initialized` with it, then `tools/list` and `tools/call` with
`{"name":"get_inventory_feed","arguments":{"feedId":"feed-1"}}`. A `tools/list` sent before the session is initialised is
refused. The full exchange with real responses is in the [Quickstart](/docs/overview/quickstart#7-first-calls-on-mcp).

## Deployment

Off by default in the chart (`mcp.enabled: false`); when enabled it renders its own Deployment and Service on `:8090`, an
optional Kong route (`mcp.route`) and `OTEL_SERVICE_NAME=<release>-mcp`. It needs `DATABASE_URL` and nothing else
([Configuration](/docs/operations/configuration#cmdmcp)). How MCP relates to REST and A2A is on the
[API overview](/docs/api-reference).
