# offer-availability

harness-template: v4

Retail network, bounded context Offer & Availability (Supporting, commerce tier). Owns CatalogItem, Listing and InventoryFeed and publishes them; consumes the four node-topology lifecycle events into a local node status view and calls no sibling.
Design source of truth: IQVO/retail-network-docs `model/model.yaml` (context offer-availability). This service belongs to the retail network, not the warehouse-systems fleet.
This project is a domain-driven design study, not associated with any company: never name a real company, brand or product in code, comments, ADRs, specs, tests or fixtures (README `## Disclaimer`).

## Commands
`make check` (fmt, vet, build, lint, unit + handler + BDD; the pre-push gate), `make coverage` (>= 90% over internal/domain + internal/application), `make mutation` (gremlins per internal/domain/<pkg>, thresholds below measurement),
`make arch-test`, `make integration` (testcontainers Postgres + Kafka, needs Docker; build tag `integration`), `make vuln`. Lint: `golangci-lint run --allow-parallel-runners ./...`.
Guides: `python3 scripts/harness/repo_lint.py`, `python3 scripts/harness/guide_lint.py` must stay clean. Never gate a commit on a piped command.

## Layout (hexagonal)
`internal/domain/{shared,catalog,listing,feed,nodestatus}` pure Go; `internal/application/{ports (interfaces only),repository (filters),views,usecases,outbox}`;
`internal/adapters/inbound/{http (chi),kafka (node-topology consumer + DLQ)}`; `internal/adapters/outbound/{postgres,kafka,outbox,clock,ids,memory,repocontract}`; `internal/adapters/kafka/cloudevents` (the ONLY envelope builder/decoder);
`internal/testing/{pgtest,kafkatest}` (testcontainers helpers); `cmd/api` (composition root, `run(ctx)`); `features/` (godog, never a .go file at the repo root). Migrations are embedded in `internal/adapters/outbound/postgres/migrations`.

## Runtime rule
Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS` and `NODE_TOPOLOGY_CONSUMER_GROUP` are mandatory (boot error naming the variable, raised before the listener binds; `MIGRATIONS_DATABASE_URL` is an optional direct DSN, falls back to `DATABASE_URL`).
Three loops are always on: the outbox relay (`OUTBOX_MAX_ATTEMPTS` default 20, 0 = unlimited; `OUTBOX_RELAY_INTERVAL` 1s), the node-topology consumer, the retention janitor (`RETENTION_INTERVAL` 1h); the process exits non-zero when one ends on its own.
`/healthz` = liveness; `/readyz` = drain gate + Postgres ping + Kafka metadata request with an EMPTY non-nil topic list (fresh connection per check; it never needs the topic to exist).
Shutdown: /readyz 503, `SHUTDOWN_DRAIN_DELAY` (5s), drain HTTP, stop the loops, close pool. cmd/** never imports internal/adapters/outbound/memory or repocontract. No authentication on REST. See docs/runtime.md.

## REST
chi router implementing every operation of apis/openapi.yaml (8 routes) plus /healthz, /readyz. Every state-changing route takes `Idempotency-Key` (table `idempotency_keys`, key = (route, key), 24h, replays the first response, same key + other request = 422 `idempotency-key-reused`).
NUL: 404 in the path, 400 `malformed-request-query|body` elsewhere. Errors are RFC 9457 problem+json, `type` = slug: not-found, illegal-transition, concurrent-update, validation-failed + one kebab-case slug per model Err (ADR 0002 A18).
A FAILED inventory feed answers 422 and is still stored (`keepWrites`, ADR 0005): GET /inventory-feeds/{feedId} shows it.

## Events: CloudEvents 1.0 is MANDATORY
Published: type `com.retailnetwork.commerce.offer-availability.<entity>.<Event>` (entity: catalogitem, listing, inventoryfeed); source `/retailnetwork/offer-availability`; dataschema `urn:retailnetwork:offer-availability:events:<Event>:v1`;
all seven events on `retailnetwork.offer-availability.events` (nothing analytics-only); subject and Kafka key = aggregate id; header `content-type: application/cloudevents+json; charset=UTF-8`.
Consumed: `com.retailnetwork.logistics.node-topology.node.{NodeRegistered,NodeActivated,NodeSuspended,NodeRetired}` from `retailnetwork.node-topology.events`, inbox `processed_events`, DLQ `<topic>.dlq`, group from `NODE_TOPOLOGY_CONSUMER_GROUP` (never a literal).
Use internal/adapters/kafka/cloudevents only. Transactional outbox: aggregates + outbox rows in ONE Postgres transaction, id minted once at append, at-least-once delivery. No `warehouse.*` topic, ever.
A new event: domain struct + apis/asyncapi.yaml + ADR 0004 `type:` line in the SAME commit + encoder payload + golden test (.claude/skills/how-to-add-an-integration-event).

## Domain
Aggregates, invariants and assumptions A1-A24 are in docs/adr/0002; consumed contracts and the local node status in 0003; relay cap, dead letters, janitor and the FAILED feed in 0005.
A feed is ONE transaction across many listings (G23): a stale line is skipped and counted, an older feed is SUPERSEDED, a FAILED feed id may be resent, at most 10000 lines. Listings start SUPPRESSED on a node that is not ACTIVE, discontinuing suppresses them, zeroing keeps the basis (G24).

## Workflow
GitFlow: feature/* -> PR into develop, CI green before merge, never push develop or main. lefthook: pre-commit fmt/vet/lint, pre-push `make check`. Commit and push after each step.
Open follow-up: the Helm chart (docs/before-first-release.md rows 5-8).
