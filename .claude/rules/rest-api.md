---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Route -> use case, kept in sync with `apis/openapi.yaml` (the routes are
registered in `internal/adapters/inbound/http/handlers.go`, wired in
`cmd/api/run.go` `buildServer`):

- `POST /catalog-items` -> `CreateCatalogItem` (201)
- `POST /catalog-items/{catalogItemId}:discontinue` -> `DiscontinueItem` (200, no body)
- `GET /catalog?limit=&after=` -> `BrowseCatalog` (`{"items":[...],"nextCursor"}`)
- `GET /catalog/{catalogItemId}` -> `GetCatalogItem`
- `POST /listings` -> `CreateListing` (201)
- `GET /listings?nodeId=&catalogItemId=&limit=&after=` -> `ListListings`
  (`{"listings":[...],"nextCursor"}`)
- `POST /inventory-feeds` -> `ApplyInventoryFeed` (201 for an `APPLIED` or
  `SUPERSEDED` feed, 422 for a `FAILED` one)
- `GET /inventory-feeds/{feedId}` -> `GetInventoryFeed`
- `GET /healthz` (liveness), `GET /readyz` (readiness): not business operations.

## Conventions

- **Errors** are RFC 9457 `application/problem+json`: `type` is a kebab-case
  slug (`internal/adapters/inbound/http/problems.go`), plus `title`, `status`,
  `detail`, `instance`. Shared: `not-found` 404, `illegal-transition` 409,
  `concurrent-update` 409, `validation-failed` 422; one slug per model `Err...`
  (`invalid-item-dimensions` 422, `duplicate-listing` 409, `negative-availability`
  422, `listing-not-on-node` 422, `feed-already-applied` 409, ...); an unmapped
  error is an opaque 500 `internal-error`. A new `Err` needs a catalogue row in
  `problems.go` (`TestProblemCatalogueMapsEveryModelError` walks the catalogue).
- **FAILED feed**: the 422 has the `instance` `/inventory-feeds/{feedId}` and the
  feed row is kept (`keepWrites`, ADR 0005): `GET /inventory-feeds/{feedId}` shows
  `FAILED` and its `failureCode`.
- **Idempotency-Key** is required on all four state-changing routes
  (`idempotency.go`): same key + same request replays the first response
  (`Idempotency-Replayed: true`), same key + other request is 422
  `idempotency-key-reused`, missing key 400 `idempotency-key-required`, keys live
  24 h in `idempotency_keys`. A new state-changing route registers through
  `Server.keyed`, never `r.Post`.
- **NUL** (U+0000) is rejected before anything else (`nul.go`): 404 in the path,
  400 `malformed-request-query` / `malformed-request-body` elsewhere.
- Bodies are decoded strictly (`decodeJSON`): unknown fields are 400
  `malformed-request-body`; a value the domain refuses is 422; a body over 64 KiB
  is 413. A `limit` that is not an integer or out of 1-500 is 422.
- **Handlers hold no business logic**: decode, call a use case, encode the view.
- **Auth**: none. REST is unauthenticated, as in the rest of the network
  (`internal/architecture/fitness_test.go` `TestNoAuthMiddlewareReintroduced`
  keeps it so).
- Unit tests (`routes_test.go`, `support_test.go`) run the real use cases over the
  memory test doubles with a pass-through for the key; the real middleware is
  proven on Postgres in `idempotency_integration_test.go`.
