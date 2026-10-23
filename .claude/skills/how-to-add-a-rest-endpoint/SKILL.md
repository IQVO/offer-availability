---
name: how-to-add-a-rest-endpoint
description: Add or change a REST endpoint in this service in the hexagonal order (domain invariant, use case, handler, apis/openapi.yaml, godog scenario). Use when touching internal/adapters/inbound/http, apis/openapi.yaml, or exposing a use case over HTTP.
---

# How to add a REST endpoint

Follow this order: domain first, adapter last. A handler written before the
invariant it relies on validates nothing and gets bypassed.

The worked example is `POST /catalog-items/{catalogItemId}:discontinue`:
`internal/domain/catalog/catalog.go` `Discontinue` -> `internal/application/usecases/catalog.go`
`DiscontinueItem` -> `internal/adapters/inbound/http/handlers.go` `discontinueCatalogItem`.
The read side example is `GET /catalog/{catalogItemId}` (`usecases/queries.go`
`GetCatalogItem` -> `getCatalogItem`).

## 1. Domain first

Does the rule already exist in `internal/domain/<catalog|listing|feed|nodestatus>/`?
A handler holds no business logic. A new rule goes into the aggregate with a
table-driven test in the same package (`catalog_test.go`, `availability_test.go`,
`transitions_test.go`, ...) and a named `Err...` variable. Pure Go: no I/O, `now`
and ids passed in. Then re-run the mutation gate for that package
(`gremlins unleash ./internal/domain/<pkg>`).

## 2. Application: the use case

Add it to `internal/application/usecases/` (`catalog.go`, `listings.go`, `feeds.go`,
`node_events.go` for commands; `queries.go` for reads). A command embeds `writer`
(see `writer.go`): `transact` runs load, domain call, `Save` and `publish` (outbox
append) in ONE `ports.UnitOfWork` and retries once on `ports.ErrConcurrentUpdate`.
A query holds only its read port. Return a view from `internal/application/views`.
If a new repository method is needed, add it to `internal/application/ports/ports.go`
(interfaces only), implement it in `internal/adapters/outbound/postgres/` AND
`internal/adapters/outbound/memory/`, and extend the shared suite in
`internal/adapters/outbound/repocontract/` so both run it. Unit-test the use case in
`internal/application/usecases/*_test.go`.

## 3. Adapter: the handler

In `internal/adapters/inbound/http/`:

- add the request body to `dto.go` (strict decoding, unknown fields are 400);
- add the use-case interface and field to `Server` (`server.go`), the handler to
  `handlers.go` and its `mount...` line;
- register a state-changing route with `s.keyed(r, method, pattern, handler)` so it
  sits behind the Idempotency-Key middleware; a read uses `r.Get`;
- map a new domain `Err` in `problems.go` `problemCatalogue` (kebab-case slug, the
  status the OpenAPI documents); `TestProblemCatalogueMapsEveryModelError` in
  `support_test.go` walks the catalogue;
- wire the use case in `cmd/api/run.go` `buildServer`.

## 4. Contract: apis/openapi.yaml

Describe the operation, its `Idempotency-Key` parameter (writes), the success
body and every problem it can return, then lint:
`npx --yes @stoplight/spectral-cli lint apis/openapi.yaml --ruleset .spectral.yaml`
(the `api-lint` CI job runs the same). Keep `.claude/rules/rest-api.md` and the
README table in step.

## 5. Behaviour

Add the happy path and each refusal to `features/*.feature` (`features/world_test.go`
drives the use cases over the memory doubles; steps name refusals by the model's
`Err` name) and an end-to-end case to `internal/adapters/inbound/http/routes_test.go`.
The real middleware and Postgres are proven by `idempotency_integration_test.go` and
`cmd/api/e2e_integration_test.go`.

## 6. Verify before opening the PR

`make check`, `make coverage`, `go test -tags=integration ./internal/adapters/inbound/http/ ./cmd/... -race -count=1`,
and the spectral lint above.
