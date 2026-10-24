# Offer & Availability runbook

The Offer & Availability bounded context (a supporting, commerce-tier service of the
retail-network DDD study; see `README.md` and `docs/adr/0001-offer-availability-bounded-context.md`)
tracks what the network can sell and from which node: catalogue items and their listings
(one per catalogue item and node), vendor inventory feeds, and a local copy of every node's
topology status. Every change publishes a CloudEvents 1.0 event through a transactional
outbox; the service calls no other service. This runbook covers the deployed `cmd/api`
process: its runtime shape, dependencies, health semantics, configuration, dashboards,
failure modes and the operator actions for them.

## Runtime shape

One binary, `cmd/api` (`cmd/api/run.go`), deployed by the Helm chart as a single `api`
Deployment (`charts/offer-availability/templates/deployment.yaml`, `replicaCount: 1`).
The process runs four things at once:

- **REST API** on `HTTP_ADDR` (default `:8080`): 8 routes from `apis/openapi.yaml` plus
  `/healthz` and `/readyz` (`internal/adapters/inbound/http/server.go`). No authentication.
  Every state-changing route requires an `Idempotency-Key` header (24 h replay window).
- **Outbox relay** (`cmd/api/workers.go` `startOutboxRelay` over
  `internal/adapters/outbound/outbox/relay.go`): drains unpublished `outbox_events` rows to
  Kafka every `OUTBOX_RELAY_INTERVAL` (default 1 s), batches of 100, in `id` order.
- **Node-topology consumer** (`internal/adapters/inbound/kafka/node_consumer.go`): reads
  `retailnetwork.node-topology.events` under `NODE_TOPOLOGY_CONSUMER_GROUP`, applies each
  event in one transaction with its `processed_events` inbox row, commits the offset after.
- **Retention janitor** (`cmd/api/workers.go` `startJanitor` over
  `internal/adapters/outbound/postgres/retention.go`): every `RETENTION_INTERVAL`
  (default 1 h) deletes `idempotency_keys` older than 24 h, `inventory_feeds` older than
  30 days and `processed_events` older than 30 days, in separate DELETEs; a failed pass is
  logged (`retention purge failed`) and retried at the next tick.

Boot order (`cmd/api/run.go` `buildAdapters`): validate env → run embedded migrations via
`MIGRATIONS_DATABASE_URL` with backoff (5 attempts, 1+2+4+8 s) → open the pool on
`DATABASE_URL` and ping it with the same backoff → start the three loops → serve. If any
loop returns on its own, the process exits non-zero so the orchestrator restarts it
(`serveUntilSignal`); this is deliberate, not a crash bug.

Shutdown on SIGINT/SIGTERM: `/readyz` flips to 503, wait `SHUTDOWN_DRAIN_DELAY`, drain HTTP
(10 s), stop and await the three loops (10 s each bound), then close the pool. The chart
refuses a `terminationGracePeriodSeconds` under `SHUTDOWN_DRAIN_DELAY` + 25 s (default 60 s).

## Dependencies

- **Postgres** — `DATABASE_URL` (the service's own database; no shared tables,
  `internal/adapters/outbound/postgres/migrations/0001_offer_availability_schema.up.sql`).
  In the cluster this is the pooled DSN behind PgBouncer. Migrations run at boot over
  `MIGRATIONS_DATABASE_URL`, a DIRECT (non-pooled) DSN, because golang-migrate holds a
  session advisory lock that transaction pooling cannot honour
  (`internal/adapters/outbound/postgres/config.go`). Both keys live in the Secret
  `offer-availability-db`; the chart mounts them as `secretKeyRef`s
  (`database.existingSecret`/`existingSecretKey` and `database.migrationsExistingSecretKey`).
- **Kafka** — `KAFKA_BROKERS`, comma-separated `host:port`. Publishes
  `retailnetwork.offer-availability.events` (`internal/adapters/outbound/kafka/encoder.go`),
  consumes `retailnetwork.node-topology.events` (`node_consumer.go` `NodeTopic`), dead
  letters on `retailnetwork.node-topology.events.dlq` (`deadletter.go` `DLQSuffix`).
  All writers set `AllowAutoTopicCreation: true` (`internal/adapters/outbound/kafka/writer.go`),
  so topics are auto-created by the first writer; `/readyz` deliberately asks for no topics.
- No other service is called; there are no sibling REST calls.

## Health

- `GET /healthz` — liveness. 200 `{"status":"ok"}` while the process serves, even while
  draining or while a dependency is down. Backs the chart's startup and liveness probes.
- `GET /readyz` — readiness (`internal/adapters/inbound/http/readiness.go`): 200
  `{"status":"ready"}` only if the drain gate is open, Postgres answers a ping and a Kafka
  broker answers a topic-less metadata request (fresh connection per check, verdict cached
  3 s, bounded 2 s). A down dependency gives 503 `application/problem+json` of type
  `dependency-unavailable` naming `postgres` or `kafka`; draining gives 503
  `{"status":"not_ready"}`.
- **Writes with a dependency down**: Kafka down → `/readyz` 503 naming `kafka`, `/healthz`
  stays 200 and a REST write is still accepted (201) and queued in the outbox — asserted by
  `cmd/api/e2e_integration_test.go` `assertKafkaDown`. Postgres down → `/readyz` 503 naming
  `postgres` and writes fail (every write is one transaction; nothing is accepted).

## Configuration

Env vars `cmd/api` reads (`cmd/api/config.go`, `internal/adapters/outbound/postgres/config.go`);
chart values from `charts/offer-availability/values.yaml`. An invalid optional value logs a
warning and falls back to its default; a missing mandatory value is a boot error naming it.

| Env var | Default | Chart value | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | required | `database.url` or `database.existingSecret` + `existingSecretKey` | Pooled Postgres DSN. |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | `database.migrationsExistingSecretKey` | Direct DSN for boot migrations (PgBouncer-safe). |
| `KAFKA_BROKERS` | required | `kafka.brokers` | Comma-separated broker list (relay, consumer, `/readyz`). |
| `NODE_TOPOLOGY_CONSUMER_GROUP` | required, no default | `config.nodeTopologyConsumerGroup` (set to `offer-availability-node-topology`) | Stable, service-prefixed consumer group id. |
| `HTTP_ADDR` | `:8080` | `config.httpAddr` | Listen address. |
| `LOG_LEVEL` | `info` | `config.logLevel` | `debug`/`info`/`warn`/`error`. |
| `OUTBOX_RELAY_INTERVAL` | `1s` | `config.outboxRelayInterval` | Relay poll interval (Go duration or plain seconds). |
| `OUTBOX_MAX_ATTEMPTS` | `20` | `config.outboxMaxAttempts` | Failures before the relay excludes a row; `0` = unlimited. |
| `RETENTION_INTERVAL` | `1h` | `config.retentionInterval` | Janitor cadence (the windows themselves are fixed: 24 h / 30 d / 30 d). |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | `config.shutdownDrainDelay` | Wait after `/readyz` flips before the listener closes; `0` disables. |

## Alerts and dashboards

- Grafana dashboard uid **`warehouse-offer-availability`**.
- Alerting: pod-restart alerts exist for this service.
- Honest state: the service does not yet instrument business metrics, so
  business-metric panels stay empty until the service publishes them. Do not read empty
  panels as "no activity"; use logs and the SQL below for ground truth.

## Failure modes

- **Pod crash-loop** — *Symptom*: restarts, pod-restart alert fires. *Diagnosis*: a worker
  that returned on its own exits the process non-zero by design (`cmd/api/run.go`), so first
  check logs for `stopped unexpectedly` / `stopped` ERROR lines naming the loop; also check
  boot errors (a missing mandatory env names it) and the migration/ping backoff giving up.
  *Action*: fix the named cause; `kubectl -n warehouse-systems rollout restart
  deploy/offer-availability` to recycle.
- **Postgres unreachable** — *Symptom*: `/readyz` 503 `dependency-unavailable` naming
  `postgres`; writes fail; boot retries migrations/ping for ~15 s then refuses to start.
  *Diagnosis*: check PgBouncer and the Secret `offer-availability-db`; confirm the direct
  migrations DSN differs from the pooled one. *Action*: restore Postgres/PgBouncer; the pod
  recovers on its own once pings answer (restart if it exited during boot).
- **Kafka unreachable** — *Symptom*: `/readyz` 503 naming `kafka`; `/healthz` 200; relay
  passes fail with `outbox relay pass failed`. REST writes are still accepted and queued:
  count them with `SELECT count(*) FROM outbox_events WHERE published_at IS NULL;`. When the
  broker returns, the relay drains in `id` order — no action needed beyond restoring Kafka.
- **Outbox relay stuck / row parked after max attempts** — table `outbox_events`
  (`migrations/0001_offer_availability_schema.up.sql`): `published_at TIMESTAMPTZ` (NULL =
  unpublished), `attempts INTEGER NOT NULL DEFAULT 0`, `last_error TEXT`. A row whose
  `attempts` reached `OUTBOX_MAX_ATTEMPTS` (20) is excluded from claims, logged once at
  ERROR (`outbox relay: row reached the attempt cap and is excluded from future claims;
  re-arm it with attempts = 0 after fixing the cause`, with id/topic/type/key), and later
  rows overtake it. *Diagnosis*: `SELECT id, topic, event_type, attempts, last_error,
  created_at FROM outbox_events WHERE published_at IS NULL ORDER BY id;`. *Action*: fix the
  cause in `last_error`, then re-arm:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;` — the relay sets `published_at`
  and clears `last_error` on success. Delivery is at-least-once with a stable event id, so
  consumers deduplicate republishes.
- **Consumer lag or retry logs** — the consumer retries a transient failure of the SAME
  message 5 times total (`domainMaxHandlerAttempts` in `deadletter.go`) with capped backoff
  (200 ms doubling, cap 5 s), then publishes it to `retailnetwork.node-topology.events.dlq`
  with `x-dlq-source-topic`/`-partition`/`-offset`/`-error`/`-failed-at` headers. Nothing
  drains the DLQ topic — inspect and handle it manually. A message that can never be applied
  (missing `nodeId`, missing instant, refused value) is dead-lettered at once; a
  non-CloudEvent or an unconsumed type is skipped. Note: a consumed topic whose producer has
  not written yet is normal quiet — the reader polls, and topics are auto-created by the
  first writer — so "no `event processed` logs" with a healthy pod means no producer traffic,
  not a fault. Persistent lag with retry ERRORs means the broker or Postgres is the problem;
  check `/readyz` and the Postgres failure mode above.

## Operational actions

```sh
# restart the single api Deployment (REST + relay + consumer + janitor)
kubectl -n warehouse-systems rollout restart deploy/offer-availability

# follow logs in Grafana Loki with the label app=offer-availability
# (JSON slog lines on stdout; raise LOG_LEVEL via config.logLevel for debug)

# unpublished outbox backlog
#   SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL;

# parked (capped) outbox rows
#   SELECT id, topic, event_type, attempts, last_error FROM outbox_events
#    WHERE published_at IS NULL AND attempts >= 20 ORDER BY id;
```

Kong exposes the service at `/api/offer-availability` (prefix stripped before the routes
above), so an external probe is `GET /api/offer-availability/healthz` through the gateway.
