# ADR 0011: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Supersedes the colon-verb routes of
[ADR 0006](./0006-catalog-gtin-packaging-and-listing-owner.md), the (route, key)
scope and the 422 reuse answer of [ADR 0005](./0005-runtime-relay-consumer-and-idempotency.md)
and the `after` cursor and `listings` key of [ADR 0002](./0002-aggregates-and-invariants.md)
(A16). Those ADR bodies stay immutable.

## Context

The fleet REST API guideline (R1-R8, enforced by `.spectral.yaml` with
`.spectral/functions/restGuideline.js`) is a hard cutover with no aliases: URLs name
things, every POST and PATCH is idempotent, a reused key is 409, and every collection
is keyset paginated as `{items,nextCursor}`.

## Decision

- **Noun routes** (operationIds unchanged):
  `POST /catalog-items/{id}:discontinue` -> `POST /catalog-items/{id}/discontinuations`;
  `POST /catalog-items/{id}/packaging/{level}:retire` -> `POST /catalog-items/{id}/packaging/{level}/retirements`.
  Status codes and bodies are unchanged. `TestRetiredRoutesAreGone` pins the old routes at 404/405.
- **Idempotency.** The key is GLOBAL (migration 0003: primary key `key`; the `route` column is kept as
  information), 1 to 128 printable ASCII characters. The fingerprint is method + path + sha256(body). Same key and
  fingerprint replays with `Idempotency-Replayed: true`; same key, other request is `409 idempotency-key-reused`
  (was 422); missing is `400 idempotency-key-required`, malformed `400 idempotency-key-invalid`. The key row,
  aggregate and outbox rows already commit in one transaction (the middleware binds its transaction into the unit
  of work), a 4xx is stored, a 5xx is not. A FAILED inventory feed still answers 422 and is kept (`keepWrites`).
- **`PUT` takes no key.** `PUT .../packaging/{level}` is idempotent by effect (the identical declaration changes
  nothing), so it no longer goes through the middleware; a key sent to it is ignored.
- **Pagination.** `GET /catalog` and `GET /listings` take `limit` and `cursor` and answer `{items,nextCursor}`
  (`listings` is now `items`). The cursor is the fleet `page` package format (base64url of the sort key and id; here
  the sort key is the unique id); `page.Unknown` makes `after`, `offset`, `nextToken` or any undeclared parameter a
  `400 unknown-query-parameter`; bad `limit` is `400 invalid-limit` (was 422), bad `cursor` `400 invalid-cursor`.
  The use cases keep returning the raw last id; only the HTTP adapter encodes it.
- **MCP and A2A keep their own tool schemas** (`after`, `listings`): they call the use cases, not REST.
- **No exemptions** (`x-pagination-exempt` is not used). Every other GET is addressed by an id.

## Consumers

The console remote (`web/src/api.ts`, screens) reads `items`, sends `cursor`, and the item screen follows
`nextCursor` (`listAllListings`). It issues no POST. No sibling service is known to call these routes.

## Consequences

Breaking for any REST client; it deploys with the rest of the REST cutover set.
