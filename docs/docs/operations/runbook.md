---
id: runbook
title: Runbook
sidebar_label: Runbook
sidebar_position: 2
---

# Offer & Availability runbook

The Offer & Availability bounded context (a supporting, commerce-tier service of the retail-network DDD study; see the
[Introduction](/docs/overview/introduction) and [ADR 0001](/docs/adr/0001-offer-availability-bounded-context)) tracks
what the network can sell and from which node: catalogue items and their listings (one per catalogue item, node and
optional owner), vendor inventory feeds, and a local copy of every node's topology status. Every change publishes a
CloudEvents 1.0 event through a transactional outbox; the service calls no other service. This runbook covers the
deployed processes: their shape, dependencies, health semantics, chart, migrations, topics, dashboards, failure modes and
the operator actions for them. Settings are in [Configuration](/docs/operations/configuration); the boot and shutdown
sequence is in [Runtime](/docs/overview/runtime); symptoms are indexed in [Troubleshooting](/docs/operations/troubleshooting).

## Runtime shape

Three binaries, three independent deployables (`charts/offer-availability`):

| Component | Binary | Port | Default in the chart | What it does |
| --- | --- | --- | --- | --- |
| `api` | `cmd/api` | `:8080` (`HTTP_ADDR`) | always rendered, `replicaCount: 1` | REST, outbox relay, node-topology consumer, retention janitor. The only process that writes and the only one that dials Kafka. |
| `mcp` | `cmd/mcp` | `:8090` (`MCP_ADDR`) | `mcp.enabled: false` | Read-only MCP over Streamable HTTP at `/mcp` and `/`. Reads Postgres only. |
| `a2a` | `cmd/a2a` | `:8093` (`A2A_ADDR`) | `a2a.enabled: false` | Read-only A2A agent, JSON-RPC at `/` and `/a2a`, Agent Card at `/.well-known/agent-card.json`. Reads Postgres only. |

The `api` process (`cmd/api/run.go`) runs four things at once:

- **REST API** on `HTTP_ADDR`: the 10 operations of `apis/openapi.yaml` (nine resource paths) plus `/healthz` and `/readyz`
  (`internal/adapters/inbound/http/server.go`). No authentication. Every state-changing route requires an
  `Idempotency-Key` header (24 h replay window).
- **Outbox relay** (`cmd/api/workers.go` `startOutboxRelay` over `internal/adapters/outbound/outbox/relay.go`): drains
  unpublished `outbox_events` rows to Kafka every `OUTBOX_RELAY_INTERVAL` (default 1 s), batches of 100, in `id` order.
- **Node-topology consumer** (`internal/adapters/inbound/kafka/node_consumer.go`): reads
  `retailnetwork.node-topology.events` under `NODE_TOPOLOGY_CONSUMER_GROUP`, applies each event in one transaction with
  its `processed_events` inbox row, commits the offset after.
- **Seller-inbound consumer** (`internal/adapters/inbound/kafka/seller_consumer.go`, ADR 0013): reads
  `retailnetwork.seller-inbound.events` under `SELLER_INBOUND_CONSUMER_GROUP` the same way; applies `SellerSuspended`,
  `SellerReinstated` and `InboundShipmentReceived` (`received_consumer.go`, ADR 0014: the seller's listings of received
  stock), counts every other type in `availability.seller_events.ignored` (`reason=type_not_consumed`). A received line
  whose item does not resolve logs `a received line resolves to no catalogue item; skipped` (WARN, with `gtin`) and counts
  `reason=gtin_unresolved` or `item_unknown`; an event without `received` (published before the producer carried it)
  creates nothing and counts `reason=received_unknown`. A steady `gtin_unresolved` means the catalogue lacks the
  packaging the seller ships: declare the pack level (`PUT /catalog-items/{id}/packaging/{level}`), then create the
  listing over `POST /listings`.
- **Retention janitor** (`cmd/api/workers.go` `startJanitor` over `internal/adapters/outbound/postgres/retention.go`):
  every `RETENTION_INTERVAL` (default 1 h) it deletes `idempotency_keys` older than 24 h and `inventory_feeds` and
  `processed_events` older than 30 days, in separate `DELETE`s. A failed pass is logged (`retention purge failed`) and
  retried at the next tick; a pass that deleted rows logs `retention purge` with the counts.

If any of the three loops returns on its own the process exits non-zero so the orchestrator restarts it
(`serveUntilSignal`): deliberate, not a crash bug. `cmd/mcp` and `cmd/a2a` serve until SIGINT/SIGTERM and then drain
HTTP for up to 10 s; they have no worker loops.

## Deployment (Helm chart)

`charts/offer-availability` renders the `api` Deployment, Service (`ClusterIP`, port 80 to 8080), ConfigMap,
ServiceAccount and database Secret reference; the HPA, Ingress and Gateway API HTTPRoute exist but default off. The
optional components are `mcp` (Service `:8090`, optional Kong route `mcp.route`, default path
`/api/mcp/offer-availability` rewritten to `/mcp`), `a2a` (Service `:8093`, optional route `a2a.route`, default path
`/api/a2a/offer-availability`, `a2a.agentUrl` empty = the in-cluster Service DNS
`http://<release>-a2a.<ns>.svc.cluster.local:8093/`) and `frontend` (the console remote, ClusterIP only). The image is
`ghcr.io/iqvo/offer-availability`; an empty `image.tag` means `v` + the chart `appVersion`, the tag the `release` job
pushes. Pods run as uid 1000 with a read-only root filesystem, all capabilities dropped and the `RuntimeDefault` seccomp
profile.

```sh
helm install offer-availability charts/offer-availability \
  --set database.existingSecret=offer-availability-db \
  --set kafka.brokers=kafka:9092 \
  --set config.nodeTopologyConsumerGroup=offer-availability-node-topology
```

The chart fails at render for: no database source, no or blank `kafka.brokers`, an empty
`config.nodeTopologyConsumerGroup`, any `*_MODE` variable in `extraEnv`, and a `terminationGracePeriodSeconds` below
`SHUTDOWN_DRAIN_DELAY` + 25 s (the default is 60 s). `config.shutdownDrainDelay` must be `0` or a whole number of `ms`,
`s` or `m`. `otel.enabled=false` renders no `OTEL_*` variable; `extraEnv` may not set the four telemetry variables.

| Chart value | Env var | Notes |
| --- | --- | --- |
| `database.url` or `database.existingSecret` (+ `existingSecretKey`, default `DATABASE_URL`) | `DATABASE_URL` | Required; always a `secretKeyRef`. |
| `database.migrationsExistingSecretKey` | `MIGRATIONS_DATABASE_URL` | Optional `secretKeyRef`; the binary falls back to `DATABASE_URL`. |
| `kafka.brokers` | `KAFKA_BROKERS` | Required (api only). |
| `config.nodeTopologyConsumerGroup` | `NODE_TOPOLOGY_CONSUMER_GROUP` | Required, no default; stable and service-prefixed (the kind release uses `offer-availability-node-topology`). |
| `config.sellerInboundConsumerGroup` | `SELLER_INBOUND_CONSUMER_GROUP` | Required, no default; stable and service-prefixed (proposed `offer-availability-seller-inbound`). |
| `config.httpAddr`, `logLevel`, `outboxRelayInterval`, `outboxMaxAttempts`, `retentionInterval`, `shutdownDrainDelay` | `HTTP_ADDR`, `LOG_LEVEL`, `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, `RETENTION_INTERVAL`, `SHUTDOWN_DRAIN_DELAY` | Defaults repeat the binary's; the render test checks they agree. |
| `otel.endpoint`, `otel.serviceName`, `environment` | `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `ENVIRONMENT` | Chart default endpoint `otel-collector.observability.svc.cluster.local:4317`. |
| `mcp.httpAddr`, `a2a.httpAddr`, `a2a.agentUrl` | `MCP_ADDR`, `A2A_ADDR`, `A2A_AGENT_URL` | Keep `service.targetPort` in step with the address. |

```sh
helm lint charts/offer-availability -f charts/offer-availability/ci/default-values.yaml
bash charts/offer-availability/tests/render_test.sh               # env contract, secrets, hardening, refusals
python3 charts/offer-availability/tests/test_service_selectors.py # every Service selects exactly one Deployment
```

CI runs `helm-lint` (`ct lint` plus the selector test) on pull requests and `chart-tests` on every push and PR; the
`release` job packages `offer-availability-<version>.tgz` and pushes it to `oci://ghcr.io/iqvo`. Kong exposes the api at
`/api/offer-availability` (prefix stripped before the routes), so an external probe is
`GET /api/offer-availability/healthz` through the gateway. The packaging checklist for a first release is
[Before the first release](/docs/operations/before-first-release).

## Probes and what readiness waits for

- `GET /healthz` is liveness: 200 `{"status":"ok"}` while the process serves, even while draining or while a dependency
  is down. It backs the chart's startup (2 s period, 30 failures), liveness and, for `mcp` and `a2a`, every probe: those
  two binaries expose only `/healthz` and have no `/readyz`.
- `GET /readyz` (api only, `internal/adapters/inbound/http/readiness.go`) is 200 `{"status":"ready"}` only if the drain
  gate is open, Postgres answers a ping and a Kafka broker answers a metadata request. The request names no topic (an
  empty, non-nil list), so it never creates a topic and does not need the output or the consumed topic to exist; a fresh
  connection is dialled on every check; verdicts are cached 3 s and a round is bounded to 2 s. A down dependency gives
  503 `application/problem+json` of type `dependency-unavailable` naming `postgres` or `kafka`; draining gives 503
  `{"status":"not_ready"}`.
- **Writes with a dependency down**: Kafka down gives `/readyz` 503 naming `kafka`, `/healthz` stays 200 and a REST write
  is still accepted (201) and queued in the outbox (asserted by `cmd/api/e2e_integration_test.go`). Postgres down gives
  `/readyz` 503 naming `postgres` and every write fails (one transaction; nothing is accepted).
- Readiness does **not** check that the consumer is attached to its partitions or that the topics exist.

## Migrations

The SQL migrations are embedded in `internal/adapters/outbound/postgres/migrations` (`0001_offer_availability_schema`,
`0002_catalog_packaging_and_listing_owner`, each with an `.up.sql` and `.down.sql`) and are run by **all three
binaries** at boot with golang-migrate (`postgres.RunMigrations`), retried with backoff (5 attempts, 1 + 2 + 4 + 8 s)
because the first outbound dial of an injected pod can be reset. golang-migrate holds a session advisory lock, so
concurrent starts are safe, but the lock does not survive transaction-pooling PgBouncer: in the cluster
`DATABASE_URL` is the pooled DSN and `MIGRATIONS_DATABASE_URL` a direct one. Both keys live in the Secret
`offer-availability-db`. There is no separate migration job and no command-line migrate step; a failed migration keeps
the pod from booting (`ping postgres` / `run migrations` errors in the log, then exit 1). Tables: `catalog_items`,
`catalog_item_pack_levels`, `listings`, `inventory_feeds`, `node_feed_marks`, `node_status_view`, `processed_events`,
`outbox_events`, `idempotency_keys`.

## Dependencies

- **Postgres**: `DATABASE_URL` (the service's own database; no shared tables). `cmd/mcp` and `cmd/a2a` read the same
  database.
- **Kafka**: `KAFKA_BROKERS`, comma-separated `host:port`, one broker platform-wide (from the host
  `localhost:9092`). Used by `cmd/api` only. All writers set `AllowAutoTopicCreation: true`
  (`internal/adapters/outbound/kafka/writer.go`), so topics are created by the first write.
- No other service is called; there are no sibling REST, MCP or A2A calls.

### Kafka topics and consumer groups

| Topic | Direction | Key | Group / writer | Notes |
| --- | --- | --- | --- | --- |
| `retailnetwork.offer-availability.events` | produced by the outbox relay | aggregate id | the relay's writer: acks=all, hash balancer | All nine event types; content type `application/cloudevents+json; charset=UTF-8`. |
| `retailnetwork.node-topology.events` | consumed | n/a | group = `NODE_TOPOLOGY_CONSUMER_GROUP` (chart: `offer-availability-node-topology`) | `NodeRegistered`, `NodeActivated`, `NodeSuspended`, `NodeRetired`; offset committed after the handler succeeded. |
| `retailnetwork.seller-inbound.events` | consumed | n/a | group = `SELLER_INBOUND_CONSUMER_GROUP` (chart: `offer-availability-seller-inbound`) | `SellerSuspended`, `SellerReinstated` (ADR 0013), `InboundShipmentReceived` (ADR 0014); every other type is counted and skipped; dead letters to `retailnetwork.seller-inbound.events.dlq`. |
| `retailnetwork.node-topology.events.dlq` | produced by the consumer | source key | the dead-letter writer | Headers `x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`, `x-dlq-error`, `x-dlq-failed-at`. Nothing reads it. |

## The outbox in operation

- Events are written in the same transaction as the aggregates and published later, so a write is accepted while Kafka is
  down; `outbox_events.published_at IS NULL` rows wait and are published, in id order, when the broker is back.
- Delivery is at-least-once with a stable CloudEvents `id`; consumers deduplicate.
- Nothing deletes `outbox_events`: published rows stay (`published_at` set). The retention janitor purges only
  `idempotency_keys`, `inventory_feeds` and `processed_events`, so the outbox table grows with every event; size and
  archive it as an operator task.
- A row that failed `OUTBOX_MAX_ATTEMPTS` times (default 20) is excluded from claims, logged once at ERROR with id, topic,
  type and key, and stays in the table with `attempts` and `last_error`. While a row is excluded, later events of the same
  aggregate can overtake it. Fix the cause, then re-arm it (see "Operational actions").

## The consumer and the dead-letter topic

- The offset is committed after the event was handled, and each event is applied in one transaction together with its
  `processed_events` row, so a redelivery changes nothing.
- A transient failure of the same message is retried 5 times in total with capped exponential backoff (200 ms doubling to
  5 s) and then the message is published to `retailnetwork.node-topology.events.dlq`. An event that can never be applied
  (no `nodeId`, no instant, a value the use case refuses) is dead-lettered at once. A message that is not a CloudEvent, or
  a type this service does not consume, is skipped with a WARN. A lost broker connection is retried with the same backoff.
- **Nothing here drains the DLQ topic: that is an operator task.** Inspect it, fix the cause (usually a producer bug in
  the node-topology context), and republish the original value to `retailnetwork.node-topology.events` if it must be
  applied; the inbox makes a re-send harmless.

```sh
kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic retailnetwork.node-topology.events.dlq \
  --from-beginning --property print.headers=true
```

## Alerts and dashboards

- Grafana dashboard uid `warehouse-offer-availability`: Kong rate/errors/latency, service RED, the
  `availability_inventory_feeds_applied_total` business panel, Go runtime and Loki logs. Alert rules: 5xx ratio, pod
  restarts and the availability/latency burn-rate SLOs. Details and the instrument list: [Observability](/docs/operations/observability).
- Business panels are fed by `availability.inventory_feeds.applied` only; there is no panel or alert for the outbox
  backlog, dead letters or consumer lag. Use the SQL below and the logs for ground truth.

## Failure modes

- **Pod crash-loop**: restarts, pod-restart alert fires. A worker that returned on its own exits the process non-zero by
  design (`cmd/api/run.go`), so check the logs for `stopped unexpectedly` / `... stopped` ERROR lines naming the loop;
  also check boot errors (a missing mandatory env names it) and the migration/ping backoff giving up. Fix the named cause;
  `kubectl -n warehouse-systems rollout restart deploy/offer-availability` recycles.
- **Postgres unreachable**: `/readyz` 503 `dependency-unavailable` naming `postgres`; writes fail; boot retries
  migrations and the ping for ~15 s and then refuses to start. Check PgBouncer and the Secret `offer-availability-db`;
  confirm the direct migrations DSN differs from the pooled one. The pod recovers once pings answer (restart if it exited
  during boot).
- **Kafka unreachable**: `/readyz` 503 naming `kafka`; `/healthz` 200; relay passes fail with `outbox relay pass failed`.
  REST writes are still accepted and queued; count them with
  `SELECT count(*) FROM outbox_events WHERE published_at IS NULL;`. When the broker returns the relay drains in `id`
  order; nothing to do beyond restoring Kafka.
- **Outbox row parked after max attempts**: see below.
- **Consumer not applying events**: see the next section and [Troubleshooting](/docs/operations/troubleshooting).

## Operational actions

```sh
# restart the single api Deployment (REST + relay + consumer + janitor)
kubectl -n warehouse-systems rollout restart deploy/offer-availability
```

```sql
-- unpublished outbox backlog and its age
SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL;

-- parked (capped) outbox rows and why
SELECT id, topic, event_type, attempts, last_error, created_at
  FROM outbox_events WHERE published_at IS NULL AND attempts >= 20 ORDER BY id;

-- re-arm a parked row after fixing the cause (the relay then publishes it and clears last_error)
UPDATE outbox_events SET attempts = 0 WHERE id = <id>;

-- the local node status read model and the inbox
SELECT * FROM node_status_view ORDER BY node_id;
SELECT count(*), max(processed_at) FROM processed_events;

-- a feed and its outcome
SELECT feed_id, node_id, state, line_count, applied_count, stale_count, zeroed_count, failure_code
  FROM inventory_feeds ORDER BY received_at DESC LIMIT 20;
```

- **Logs**: follow Grafana Loki with the label `app=offer-availability` (JSON `slog` lines on stdout); raise
  `config.logLevel` to `debug` to see exporter errors.
- **Consumer group state** (the kind broker): `kafka-consumer-groups.sh --bootstrap-server localhost:9092 --describe
  --group offer-availability-node-topology`. An empty answer for a running pod means the consumer has no assignment.
- **A feed is stuck FAILED**: it is recorded on purpose (ADR 0005); the vendor corrects the lines and re-sends the same
  `feedId`, which is allowed for a FAILED feed.
- **Retention**: expired rows are removed hourly; to purge sooner, restart the api (the janitor runs a pass at start).
