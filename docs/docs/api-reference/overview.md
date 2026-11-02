---
id: overview
title: API overview
slug: /api-reference
sidebar_label: Overview
sidebar_position: 1
---

# API overview

:::info Implemented by cmd/api, cmd/mcp and cmd/a2a
`cmd/api` serves every REST route below (`internal/adapters/inbound/http`) on `HTTP_ADDR` (default `:8080`). The contracts
are `apis/openapi.yaml` and `apis/asyncapi.yaml`, pinned by
[ADR 0001](/docs/adr/0001-offer-availability-bounded-context), [ADR 0002](/docs/adr/0002-aggregates-and-invariants) and
[ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue). The router registers exactly the ten operations of the
spec plus `/healthz` and `/readyz`, which the spec does not list. See the [Quickstart](/docs/overview/quickstart) for real
responses.
:::

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST | `apis/openapi.yaml` (OpenAPI 3.0.3, version 0.1.0) | The pages under *REST* are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. The spec names one server, `http://localhost:8080`; through Kong in the kind cluster the base is `http://localhost:8000/api/offer-availability` (prefix stripped before the routes). |
| Events | `apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0) | Nine published CloudEvents types on `retailnetwork.offer-availability.events` and four consumed; see the [event catalogue](/docs/api-reference/events) and [Domain events](/docs/ddd/domain-events). |
| MCP | [ADR 0007](/docs/adr/0007-mcp-server-adoption), `internal/adapters/inbound/mcp` | `cmd/mcp` serves Streamable HTTP on `/mcp` and `/` (port 8090; through Kong `/api/mcp/offer-availability`). Four read-only tools, no write tool: `browse_catalog`, `get_catalog_item`, `list_listings`, `get_inventory_feed`. No auth, by fleet decision. See [MCP tools](/docs/mcp/tools). |
| A2A | [ADR 0010](/docs/adr/0010-a2a-inbound-adapter), `internal/adapters/inbound/a2a` | `cmd/a2a` serves JSON-RPC 2.0 on `/` and `/a2a` (port 8093; through Kong `/api/a2a/offer-availability`) and the Agent Card at `/.well-known/agent-card.json`. Two read-only skills, no write skill: `check_availability` and `feed_outcome`. No auth, by fleet decision. |

## Endpoints

| Tag | Endpoint | Operation id | Success |
| --- | --- | --- | --- |
| Catalog items | `POST /catalog-items` | `createCatalogItem` | `201`, the item (`ACTIVE`) |
| Catalog items | `POST /catalog-items/{catalogItemId}/discontinuations` | `discontinueCatalogItem` | `200`, the item (`DISCONTINUED`); its listings are suppressed |
| Catalog items | `PUT /catalog-items/{catalogItemId}/packaging/{level}` | `declarePackLevel` | `200`, the item with its packaging |
| Catalog items | `POST /catalog-items/{catalogItemId}/packaging/{level}/retirements` | `retirePackLevel` | `200`, the item |
| Catalog | `GET /catalog` | `browseCatalog` | `200 {items, nextCursor?}` |
| Catalog | `GET /catalog/{catalogItemId}` | `getCatalogItem` | `200`, the item; `404 not-found` |
| Listings | `POST /listings` | `createListing` | `201`, the listing (`LIVE` or `SUPPRESSED`) |
| Listings | `GET /listings` | `listListings` | `200 {listings, nextCursor?}`, filters `nodeId`, `catalogItemId` |
| Inventory feeds | `POST /inventory-feeds` | `applyInventoryFeed` | `201`, the feed (`APPLIED` or `SUPERSEDED`); `422` with the problem when it ends `FAILED` |
| Inventory feeds | `GET /inventory-feeds/{feedId}` | `getInventoryFeed` | `200`, the feed and its counters; `404 not-found` |
| Health | `GET /healthz`, `GET /readyz` | none (not in the spec) | `200`, or `503` while draining or when Postgres or Kafka is unusable |

## Conventions in the contract

- **`Idempotency-Key`.** Required on every `POST` (`PUT` is idempotent by method and takes none). 1 to 128
  printable ASCII characters, GLOBAL: `400 idempotency-key-required` when absent, `400 idempotency-key-invalid` when
  malformed. The same key with the same method, path and body replays the first answer (`Idempotency-Replayed: true`,
  24 hours); with any other request it is `409 idempotency-key-reused`. A 5xx is never stored. Implemented in
  `internal/adapters/inbound/http/idempotency.go` ([ADR 0011](/docs/adr/0011-adopt-the-fleet-rest-api-guideline)).
- **Paging.** `GET /catalog` and `GET /listings` answer `{ "items": [...], "nextCursor": "..." }`: `limit` 1 to 500
  (default 100) and the opaque `cursor` (the previous page's `nextCursor`); no `nextCursor` marks the last page. A bad
  `limit` is `400 invalid-limit`, an undecodable cursor `400 invalid-cursor`, any other query parameter (`after`,
  `offset`, ...) `400 unknown-query-parameter`.
- **Identifiers.** `catalogItemId` is `item-<uuid>`, `listingId` is `lst-<uuid>`; `feedId` and `nodeId` are chosen by the
  caller. A GTIN is accepted as 8, 12, 13 or 14 digits and stored as 14.
- **Request limits.** Bodies at most 64 KiB; a feed at most 10000 lines; a NUL character anywhere is `400`.
- **Errors** are RFC 9457 `application/problem+json` with `type` (a kebab-case slug such as `not-found`,
  `listing-not-on-node`), `title`, `status`, `detail` and `instance`. The catalogue is in
  `internal/adapters/inbound/http/problems.go`; the mapping from errors is on
  [Use cases](/docs/ddd/use-cases#errors-and-the-problem-types-they-become) and, with remedies, on
  [Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).
- **Auth**: none. REST, MCP and A2A are unauthenticated fleet-wide, and `TestNoAuthMiddlewareReintroduced` fails CI if
  authentication middleware returns under `internal/adapters/inbound`.
- **Writes are REST only.** MCP and A2A hold no write use case (`TestA2AAdapterIsReadOnly`, `TestToolSurface`).

## A2A

The Agent Card (`GET /.well-known/agent-card.json`) names the agent `offer-availability`, provider `IQVO`, transport
JSON-RPC at the URL given in `A2A_AGENT_URL`, input and output modes `application/json` and `text/plain`, no streaming and
no security schemes. It is a message-only agent: each request is one JSON-RPC `SendMessage` and the answer is one agent
message, with no long-running task.

The first message part carries, as a data part (or a text part holding the same JSON), `{"skill": "<id>", "arguments": {...}}`.
Unknown fields are refused, never ignored.

| Skill | Arguments | Answer |
| --- | --- | --- |
| `check_availability` | `catalogItemId` and/or `nodeId` (at least one), optional `limit` 1..500, `after` | The catalogue item (when `catalogItemId` was given) and a page of listings with state, suppression reason, `ownerRef` and availability (`availableQty`, `basis`, `asOf`, `feedId`), plus a one-line text summary |
| `feed_outcome` | `feedId` (required) | The feed: node, `fullUpdate`, `asOf`, state `APPLIED`/`SUPERSEDED`/`FAILED`, counters, `receivedAt`, `appliedAt`, `failureCode` |

Request-shape problems and domain rejections are answered **inside the message envelope** as problems, never as JSON-RPC
errors: `malformed-skill-request`, `unknown-skill`, `invalid-argument` (all status 400), `not-found` (404),
`validation-failed` (422) and `internal-error` (500). The first calls are in the
[Quickstart](/docs/overview/quickstart#8-first-calls-on-a2a).
