# offer-availability

Retail network, bounded context **Offer & Availability** (Supporting, commerce
tier): what the network can sell and from which node. It owns the catalogue
items (with their GS1 packaging hierarchies), the per-node listings, vendor
inventory feeds and a local copy of every node's status. It does **not** own
node lifecycle (`node-topology` does), orders (`customer-ordering` does) or
pricing. It is an Open Host Service — REST plus a published CloudEvents
language on `retailnetwork.offer-availability.events` — and calls no other
service. This repository is a service of the retail network, not of the
warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

---

## Documentation

This repo has no docs site of its own. The context's design lives on the
warehouse-docs retail-network pages:

| Area | Pages |
|---|---|
| Retail network | [Network overview](https://iqvo.github.io/warehouse-docs/network) |
| This context | [Index / service facts](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability), [Business context](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/business-context), [Domain events](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/domain-events), [Use cases](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/use-cases) |
| DDD artifact pack | [Ubiquitous language](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/ubiquitous-language), [Context map](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/context-map), [Core domain chart](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/core-domain-chart), [Bounded context canvas](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/bounded-context-canvas), [Aggregate design canvas](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/aggregate-design-canvas), [EventStorming](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/eventstorming), [Class diagram](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/class-diagram), [Sequence diagrams](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/sequence-diagrams), [ER diagram](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/entity-relationship), [Domain message flow](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability/domain-message-flow) |
| Decisions | [Network ADRs](https://iqvo.github.io/warehouse-docs/network/adr) |

In-tree: [`docs/adr/`](docs/adr/index.md) (decisions, index),
[`docs/runtime.md`](docs/runtime.md) (settings, probes, shutdown order,
outbox runbook), [`docs/operations/runbook.md`](docs/operations/runbook.md),
[`docs/before-first-release.md`](docs/before-first-release.md) (packaging
checklist).

---

## What it does

- **Catalogue**: create a catalogue item (title, dimensions, weight, hazmat, fragile) and discontinue it; discontinuing suppresses every listing of the item.
- **Identifiers and packaging**: an item may carry a GS1 `identifiers.gtin` and a packaging hierarchy `EACH < INNER < CASE < PALLET` (each level a GTIN with `unitsPerPack` in eaches, a child GTIN chain, optional dimensions and weight). The `unitsPerPack` of a GTIN never changes: a new count is a new GTIN and the old level is retired ([ADR 0006](docs/adr/0006-catalog-gtin-packaging-and-listing-owner.md)).
- **Listings**: one listing per (catalogue item, node, optional `ownerRef`), `LIVE` while its node is `ACTIVE`, `SUPPRESSED` (reason `NODE_NOT_SELLABLE` or `DISCONTINUED`) otherwise. `ownerRef` is the optional seller that owns the stock; absent means operator-owned.
- **Inventory feeds**: a vendor sends the quantity a node can ship for its listings, with the basis it gave (`PHYSICAL`, `THROUGHPUT_CONSTRAINED`).
  A feed is applied in one transaction; a stale line is skipped and counted, an older feed is `SUPERSEDED`, a broken line makes the feed `FAILED`
  (recorded, and it may be sent again), and a full feed zeroes the listings it omits.
- **Node status**: the four node-topology lifecycle events keep a local copy of every node's status, and suspending or retiring a node suppresses its
  listings, activating it restores them.
- Every change publishes a CloudEvents 1.0 event through a transactional outbox. The service calls no other service.

Aggregates, invariants and every assumption the model left open: [docs/adr/0002](docs/adr/0002-aggregates-and-invariants.md).

## Architecture

Hexagonal / ports & adapters. Dependencies point inward only:

```
cmd/api/                     the REST service (composition root): HTTP, the outbox relay, the node-topology consumer and the retention janitor in one process
cmd/mcp/                     read-only MCP server (Streamable HTTP, :8090)
cmd/a2a/                     read-only A2A agent (JSON-RPC over HTTP, Agent Card at /.well-known/agent-card.json)
internal/
  domain/                    pure Go: shared, catalog, listing, feed, nodestatus
  application/               ports (OUT interfaces), usecases, views, repository, outbox
  adapters/
    inbound/http/            chi handlers, DTOs, RFC 9457 problem+json errors, /healthz + /readyz
    inbound/kafka/           the node-topology consumer
    inbound/mcp/  inbound/a2a/   the read-only agent surfaces
    outbound/postgres/       pgx repos, embedded golang-migrate SQL, transactional outbox
    outbound/kafka/          the integration-topic writer
    outbound/outbox/         the relay
    outbound/clock/ ids/     the wall clock and id source
    outbound/memory/ repocontract/   test doubles, never imported by cmd/** (enforced)
    outbound/telemetry/      OpenTelemetry wiring
  architecture/              the fitness tests
features/                    godog BDD (Gherkin)
web/                         the offer_availability_mfe console remote
```

The layering rules are executable tests in `internal/architecture/`, not
conventions: `architecture_test.go` (hexagonal dependencies),
`runtime_no_memory_fitness_test.go` (no binary links the in-memory doubles),
`kafka_writer_topic_fitness_test.go` / `kafka_writer_acks_fitness_test.go`
(the writer's topic and acks), `outbox_relay_cap_fitness_test.go`,
`a2a_fitness_test.go` (the agent is read-only and Kafka-free) and
`no_go_files_at_root_fitness_test.go`, among others — run by `make arch-test`.

## Domain events

Contract: [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md).
Published to `retailnetwork.offer-availability.events`, CloudEvents 1.0 structured JSON, type
`com.retailnetwork.commerce.offer-availability.<entity>.<Event>` (`catalogitem`, `listing`, `inventoryfeed`), `subject` and Kafka key = aggregate id:
`CatalogItemCreated`, `CatalogItemDiscontinued`, `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired`, `ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored`,
`InventoryFeedApplied`. Delivery is at-least-once with a stable event id ([ADR 0005](docs/adr/0005-runtime-relay-consumer-and-idempotency.md)). Real types, verbatim from the contract:

```
com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated
com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemPackagingDeclared
```

Consumed from `retailnetwork.node-topology.events` ([ADR 0003](docs/adr/0003-consumed-contracts-and-local-node-status.md)): `NodeRegistered`,
`NodeActivated`, `NodeSuspended`, `NodeRetired`.

## Running it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS` and `NODE_TOPOLOGY_CONSUMER_GROUP` are mandatory (a boot error names the missing
variable); the embedded migrations run at boot and the outbox relay, the consumer and the retention janitor are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/offer_availability?sslmode=disable
export KAFKA_BROKERS=localhost:29092                   # whatever your broker advertises
export NODE_TOPOLOGY_CONSUMER_GROUP=offer-availability-local
go run ./cmd/api                                       # listens on :8080 (HTTP_ADDR)
```

All settings, the probes, the shutdown order and the outbox runbook: [docs/runtime.md](docs/runtime.md).

### Configuration

`cmd/api`:

| Env var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | **required** | Postgres DSN; unset ⇒ boot error naming the variable |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations |
| `KAFKA_BROKERS` | **required** | Comma-separated `host:port` list; unset ⇒ boot error |
| `NODE_TOPOLOGY_CONSUMER_GROUP` | **required** | Stable node-topology consumer group id, never defaulted (a local run cannot silently join a live cluster's group) |
| `HTTP_ADDR` | `:8080` | Listen address |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Relay sleep between passes (Go duration or plain seconds) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Outbox attempt cap before a row is excluded from the relay |
| `RETENTION_INTERVAL` | `1h` | How often the retention janitor deletes expired rows |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Drain wait before shutdown completes |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |

`cmd/mcp` reads `DATABASE_URL` (**required**), `MIGRATIONS_DATABASE_URL`
(default `DATABASE_URL`), `MCP_ADDR` (default `:8090`) and `LOG_LEVEL`.
`cmd/a2a` reads `DATABASE_URL` (**required**), `A2A_AGENT_URL` (**required**,
the absolute URL the Agent Card advertises), `MIGRATIONS_DATABASE_URL`
(default `DATABASE_URL`), `A2A_ADDR` (default `:8093`) and `LOG_LEVEL`.

All three binaries export telemetry and read `OTEL_EXPORTER_OTLP_ENDPOINT`
(default `localhost:4317`), `OTEL_SERVICE_NAME` (default `offer-availability`;
`offer-availability-mcp` / `offer-availability-a2a` for the agent binaries),
`SERVICE_VERSION` (default `dev`) and `ENVIRONMENT` (default `local`) — see
[Observability](#observability).

## Observability

Logs, metrics and traces follow the fleet standard ([ADR 0009](docs/adr/0009-fleet-observability-standard.md)): JSON logs on stdout carry `trace_id`/`span_id`
and a `request_id`; `cmd/api`, `cmd/mcp` and `cmd/a2a` export OTLP/gRPC to the Collector (`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `SERVICE_VERSION`, `ENVIRONMENT`;
export never blocks, so no Collector costs telemetry, nothing else) with HTTP request duration, Go runtime metrics and the business counter
`availability.inventory_feeds.applied` (`outcome`, `reason`); the trace continues across Kafka in the `traceparent` header of each published event and is picked up
by the node-topology consumer. The chart's `otel.*` values render the env.

## API

Contract: [`apis/openapi.yaml`](apis/openapi.yaml). No authentication. Errors are RFC 9457 `application/problem+json` (`type` is a slug such as
`not-found`, `validation-failed`, `concurrent-update`, `duplicate-listing`, `feed-already-applied`, `invalid-gtin`, `gtin-already-assigned`, `pack-quantity-immutable`).
Every state-changing request needs an `Idempotency-Key` header: a repeat with the same request replays the first response for 24 hours.

| Method and path | Operation |
| --- | --- |
| `POST /catalog-items`, `POST /catalog-items/{catalogItemId}:discontinue` | create, discontinue |
| `PUT /catalog-items/{catalogItemId}/packaging/{level}`, `POST /catalog-items/{catalogItemId}/packaging/{level}:retire` | declare, retire a pack level |
| `GET /catalog`, `GET /catalog/{catalogItemId}` | browse (keyset pages), read |
| `POST /listings`, `GET /listings?nodeId=&catalogItemId=` | create, list |
| `POST /inventory-feeds`, `GET /inventory-feeds/{feedId}` | apply a feed, read its outcome |
| `GET /healthz`, `GET /readyz` | liveness, readiness (served by the binary; the spec covers the nine resource paths above) |

## Helm chart

The Helm chart is [`charts/offer-availability`](charts/offer-availability) (one `api` component). It refuses to render without a database
source, `kafka.brokers` and `config.nodeTopologyConsumerGroup` (a stable, service-prefixed id, never the pod name). `DATABASE_URL` always comes
from a Secret.

```sh
helm install offer-availability charts/offer-availability \
  --set database.existingSecret=offer-availability-db \
  --set kafka.brokers=kafka:9092 \
  --set config.nodeTopologyConsumerGroup=offer-availability-node-topology
```

Values that matter: `database.url` / `database.existingSecret` (key
`DATABASE_URL`), `kafka.brokers`, `config.*` (`httpAddr`, `logLevel`,
`outboxRelayInterval`, `outboxMaxAttempts`, `retentionInterval`,
`shutdownDrainDelay`, `nodeTopologyConsumerGroup`), the `otel.*` block
(`enabled`, `endpoint`, `serviceName`), `environment`, and the optional
`mcp`, `a2a` and `frontend` components (all `enabled: false` by default).
Render invariants are asserted by
`charts/offer-availability/tests/render_test.sh` and
`test_service_selectors.py` (every Service selects exactly one Deployment;
the OLTP Service never selects the MCP, A2A or frontend pods), over the
`ci/default-values.yaml` and `ci/all-components-values.yaml` values files.
Linted in CI with `ct lint --charts charts/offer-availability`.

### Running the MCP server in Kubernetes

A second, read-only binary — `cmd/mcp` — exposes the same reads to agents over
the [Model Context Protocol](https://modelcontextprotocol.io) ([ADR 0007](docs/adr/0007-mcp-server-adoption.md)):
Streamable HTTP at `/mcp` (and `/`), an open `/healthz` on `MCP_ADDR` (default `:8090`), no auth (fleet-wide revert), no Kafka, no writes.

| Tool | Mirrors |
| --- | --- |
| `browse_catalog(limit?, after?)` | `GET /catalog` |
| `get_catalog_item(catalogItemId)` | `GET /catalog/{catalogItemId}` |
| `list_listings(nodeId?, catalogItemId?, limit?, after?)` | `GET /listings` |
| `get_inventory_feed(feedId)` | `GET /inventory-feeds/{feedId}` |

Errors carry the REST problem slugs (`not-found`, `validation-failed`); the whole advertised tool registry is pinned by a golden file.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/offer_availability?sslmode=disable
go run ./cmd/mcp                                        # listens on :8090 (MCP_ADDR)
```

In the cluster it is the optional `mcp` chart component (`--set mcp.enabled=true`, plus `mcp.route.enabled` for the Kong route `/api/mcp/offer-availability` → `/mcp`).

### Running the A2A agent in Kubernetes

A third, read-only binary — `cmd/a2a` — lets another agent *delegate a goal* to this context over the
[Agent2Agent protocol](https://a2a-protocol.org) ([ADR 0010](docs/adr/0010-a2a-inbound-adapter.md)): JSON-RPC at `/` and `/a2a`, the Agent Card at
`/.well-known/agent-card.json`, an open `/healthz` on `A2A_ADDR` (default `:8093`), no auth (fleet-wide revert), no Kafka, no writes — the MCP surface is
read-only, so the agent is too. A2A carries delegation only; facts still leave as CloudEvents from `cmd/api`.

| Skill | Goal | Built from |
| --- | --- | --- |
| `check_availability(catalogItemId?, nodeId?, limit?, after?)` (at least one filter) | where an item is sold and how much is available (the item plus its listings) | `GET /catalog/{catalogItemId}` + `GET /listings` |
| `feed_outcome(feedId)` | what became of one vendor inventory feed | `GET /inventory-feeds/{feedId}` |

Send `{"skill":"check_availability","arguments":{"catalogItemId":"item-1"}}` as a data part (or a text part holding that JSON). Rejections are answered in the
message as RFC 7807-shaped problems with the REST slugs and statuses (`not-found` 404, `validation-failed` 422, plus `invalid-argument`, `unknown-skill`
and `malformed-skill-request` 400), never as JSON-RPC errors.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/offer_availability?sslmode=disable
export A2A_AGENT_URL=http://localhost:8093/               # the URL the Agent Card advertises (mandatory)
go run ./cmd/a2a                                          # listens on :8093 (A2A_ADDR)
```

In the cluster it is the optional `a2a` chart component (`--set a2a.enabled=true`; `A2A_AGENT_URL` defaults to the in-cluster Service DNS, override it with
`a2a.agentUrl`; `a2a.route.enabled` adds the Kong route `/api/a2/offer-availability` → `/`).

## Operator micro-frontend (`web/`)

`web/` is the `offer_availability_mfe` Module Federation remote for the warehouse-console
shell ([ADR 0008](docs/adr/0008-console-remote.md)): read-only screens over the REST reads above — the catalogue (keyset pages), one item
with its listings, the listings of a node and/or item, and an inventory-feed lookup with its outcome. The API base is resolved at runtime from
the shell's `window.__WAREHOUSE_CONFIG__.apiOrigin` (`${apiOrigin}/api/offer-availability`); the remote is served by its own nginx pod behind the web gateway at `/mfes/offer-availability/` (the chart's optional
`frontend` component, ClusterIP only).

```sh
cd web && npm install && npm run dev        # standalone on :4018 (strictPort)
docker build --build-context uikit=../../warehouse-ui-kit -t warehouse/offer-availability-frontend:local .
```

## Local development / quality gate

Every CI sensor is a `make` target. `make help` lists them all.

| Command | What it runs |
| --- | --- |
| `make check` | format, vet, build, lint, unit, handler and BDD tests (the pre-push gate) |
| `make check-fast` | fmt-check, vet, arch-test |
| `make check-all` | check + coverage + arch-test + bdd |
| `make coverage` | 90% gate over `internal/domain` and `internal/application` |
| `make mutation` | gremlins on every `internal/domain/<pkg>` |
| `make arch-test` | hexagonal and harness fitness tests |
| `make integration` | testcontainers Postgres and Kafka: repositories, relay, middleware, real-wiring test (needs Docker) |
| `make vuln` | govulncheck |

Git hooks are managed by [lefthook](https://github.com/evilmartians/lefthook)
(`lefthook.yml`): pre-commit runs `fmt-check` + `vet` + `lint`; pre-push runs
`make check`. Activate once per clone: `lefthook install`.

## Tests

- **Unit** — `go test ./...` (domain, use cases, handlers; `make test`).
- **BDD acceptance** — godog/Gherkin over the real HTTP API in `features/` (`catalog`, `http_api`, `idempotency`, `inventory_feeds`, `listings`, `node_status`, `ownerref`, `packaging`); `make bdd`.
- **Integration** — testcontainers Postgres and Kafka: repositories, relay, middleware and a real-wiring test (`make integration`, build-tagged, needs only Docker).
- **Coverage gate** — `make coverage`: ≥ 90% over `internal/domain` + `internal/application`.
- **Mutation** — gremlins over every `internal/domain/<pkg>` (`make mutation`; the exhaustive `mutation-full`).
- **Architecture fitness** — `internal/architecture/` (`make arch-test`).
- **Web** — oxlint, `tsc -b` and vitest in `web/`.

CI (`.github/workflows/ci.yml`) runs `lint`, `guide-lint`, `complexity`, `test`
(with the 90% coverage gate), `bdd`, `integration`, `mutation-fast`, `api-lint`,
`vuln`, `chart-tests`, `arch-test` and `web` on every push and PR (`helm-lint`
with `ct lint` on PRs); the exhaustive `mutation` and the advisory `drift`
sensor run only on the weekly schedule or manual dispatch; `docker-publish`
pushes the image to GHCR on `main` once the gate jobs
(`lint`, `complexity`, `test`, `bdd`, `integration`, `mutation-fast`, `vuln`,
`api-lint`, `arch-test`) are green, and `release` then tags the next version
and publishes the chart.

## Architecture Decision Records

The ADR index lives at [docs/adr/index.md](docs/adr/index.md) — 10 decisions,
from the bounded context and aggregates to the CloudEvents catalogue, the MCP
server and the A2A adapter.

## License

MIT — see [LICENSE](LICENSE).
