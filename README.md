# offer-availability

Retail network, bounded context **Offer & Availability** (Supporting, commerce tier): what the network can sell and from which node.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

The design is proposed in [retail-network docs](https://iqvo.github.io/warehouse-docs/network/contexts/offer-availability); `model/model.yaml` there is the single source of truth.
This repository is a service of the retail network, not of the warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

## What it does

- **Catalogue**: create a catalogue item (title, dimensions, weight, hazmat, fragile) and discontinue it; discontinuing suppresses every listing of the item.
- **Identifiers and packaging**: an item may carry a GS1 `identifiers.gtin` and a packaging hierarchy `EACH < INNER < CASE < PALLET` (each level a GTIN with `unitsPerPack` in eaches, a child GTIN chain, optional dimensions and weight). The `unitsPerPack` of a GTIN never changes: a new count is a new GTIN and the old level is retired ([ADR 0007](docs/adr/0006-catalog-gtin-packaging-and-listing-owner.md)).
- **Listings**: one listing per (catalogue item, node, optional `ownerRef`), `LIVE` while its node is `ACTIVE`, `SUPPRESSED` (reason `NODE_NOT_SELLABLE` or `DISCONTINUED`) otherwise. `ownerRef` is the optional seller that owns the stock; absent means operator-owned.
- **Inventory feeds**: a vendor sends the quantity a node can ship for its listings, with the basis it gave (`PHYSICAL`, `THROUGHPUT_CONSTRAINED`).
  A feed is applied in one transaction; a stale line is skipped and counted, an older feed is `SUPERSEDED`, a broken line makes the feed `FAILED`
  (recorded, and it may be sent again), and a full feed zeroes the listings it omits.
- **Node status**: the four node-topology lifecycle events keep a local copy of every node's status, and suspending or retiring a node suppresses its
  listings, activating it restores them.
- Every change publishes a CloudEvents 1.0 event through a transactional outbox. The service calls no other service.

Aggregates, invariants and every assumption the model left open: [docs/adr/0002](docs/adr/0002-aggregates-and-invariants.md).

## REST API

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
| `GET /healthz`, `GET /readyz` | liveness, readiness |

## Events

Contract: [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md).
Published to `retailnetwork.offer-availability.events`, CloudEvents 1.0 structured JSON, type
`com.retailnetwork.commerce.offer-availability.<entity>.<Event>` (`catalogitem`, `listing`, `inventoryfeed`), `subject` and Kafka key = aggregate id:
`CatalogItemCreated`, `CatalogItemDiscontinued`, `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired`, `ListingCreated`, `ListingAvailabilityChanged`, `ListingSuppressed`, `ListingRestored`,
`InventoryFeedApplied`. Delivery is at-least-once with a stable event id ([ADR 0005](docs/adr/0005-runtime-relay-consumer-and-idempotency.md)).

Consumed from `retailnetwork.node-topology.events` ([ADR 0003](docs/adr/0003-consumed-contracts-and-local-node-status.md)): `NodeRegistered`,
`NodeActivated`, `NodeSuspended`, `NodeRetired`.

## Run it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS` and `NODE_TOPOLOGY_CONSUMER_GROUP` are mandatory (a boot error names the missing
variable); the embedded migrations run at boot and the outbox relay, the consumer and the retention janitor are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/offer_availability?sslmode=disable
export KAFKA_BROKERS=localhost:29092                   # whatever your broker advertises
export NODE_TOPOLOGY_CONSUMER_GROUP=offer-availability-local
go run ./cmd/api                                       # listens on :8080 (HTTP_ADDR)
```

All settings, the probes, the shutdown order and the outbox runbook: [docs/runtime.md](docs/runtime.md).

## Deploy it

The Helm chart is [`charts/offer-availability`](charts/offer-availability) (one `api` component). It refuses to render without a database
source, `kafka.brokers` and `config.nodeTopologyConsumerGroup` (a stable, service-prefixed id, never the pod name). `DATABASE_URL` always comes
from a Secret.

```sh
helm install offer-availability charts/offer-availability \
  --set database.existingSecret=offer-availability-db \
  --set kafka.brokers=kafka:9092 \
  --set config.nodeTopologyConsumerGroup=offer-availability-node-topology
```

Values, grace period and the chart tests: [docs/runtime.md](docs/runtime.md#deployment-helm-chart).

## MCP server

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

## Web console remote

`web/` is the `offer_availability_mfe` Module Federation remote for the warehouse-console
shell ([ADR 0008](docs/adr/0008-console-remote.md)): read-only screens over the REST reads above — the catalogue (keyset pages), one item
with its listings, the listings of a node and/or item, and an inventory-feed lookup with its outcome. The API base is resolved at runtime from
the shell's `/config.json`; the remote is served by its own nginx pod behind the web gateway at `/mfes/offer-availability/` (the chart's optional
`frontend` component, ClusterIP only).

```sh
cd web && npm install && npm run dev        # standalone on :4018 (strictPort)
docker build --build-context uikit=../../warehouse-ui-kit -t warehouse/offer-availability-frontend:local .
```

## Develop

| Command | What it runs |
| --- | --- |
| `make check` | format, vet, build, lint, unit, handler and BDD tests (the pre-push gate) |
| `make coverage` | 90% gate over `internal/domain` and `internal/application` |
| `make mutation` | gremlins on every `internal/domain/<pkg>` |
| `make arch-test` | hexagonal and harness fitness tests |
| `make integration` | testcontainers Postgres and Kafka: repositories, relay, middleware, real-wiring test (needs Docker) |
| `make vuln` | govulncheck |

Layout: `internal/domain/{shared,catalog,listing,feed,nodestatus}` (pure Go), `internal/application/{ports,views,usecases,repository,outbox}`,
`internal/adapters/inbound/{http,kafka}` (chi, the node-topology consumer), `internal/adapters/outbound/{postgres,kafka,outbox,clock,ids,memory,repocontract}`,
`cmd/api`, `features/` (godog BDD). The `memory` and `repocontract` packages are test doubles; `cmd/**` never imports them.

Decisions: [docs/adr/](docs/adr/index.md). Packaging checklist: [docs/before-first-release.md](docs/before-first-release.md).
