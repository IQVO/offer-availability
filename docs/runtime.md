# Runtime: configuration, probes, shutdown and the outbox

`cmd/api` is the only binary. It runs on real PostgreSQL and real Kafka; there is
no alternative runtime and no log sink.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres DSN of the service's pool. A boot error names it when unset. |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations (golang-migrate holds a session advisory lock, which breaks behind a transaction-pooling proxy). |
| `KAFKA_BROKERS` | required | Comma-separated `host:port` list (relay, consumer, `/readyz`). A boot error names it when unset or blank. |
| `NODE_TOPOLOGY_CONSUMER_GROUP` | required | Consumer group id of the node-topology consumer. A boot error names it when unset or blank; there is no default, so a local process cannot join a live cluster's group by accident. |
| `HTTP_ADDR` | `:8080` | Listen address. |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Poll interval of the relay (Go duration or seconds). |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row; `0` = unlimited (ADR 0005). |
| `RETENTION_INTERVAL` | `1h` | How often the janitor deletes idempotency keys older than 24 h and feeds and processed events older than 30 days. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait between flipping `/readyz` to 503 and closing the listener; `0` disables. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `OTEL_SERVICE_NAME` | `offer-availability` | `service.name` of spans and metrics (ADR 0009). |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Collector OTLP/gRPC, `host:port` or URL; export never blocks. |
| `SERVICE_VERSION` / `ENVIRONMENT` | `dev` / `local` | `service.version` / `deployment.environment.name`. |

Configuration errors are raised before anything is opened or bound. An invalid
optional value logs a warning and falls back to its default.

## Boot and shutdown

1. Validate the environment. 2. Run the embedded migrations and ping Postgres, both
with exponential backoff (5 attempts, 1 + 2 + 4 + 8 s). 3. Start the outbox relay,
the node-topology consumer and the janitor (none dials at construction; a broker
that is down only makes passes fail until it is back). 4. Serve.

On SIGINT/SIGTERM: `/readyz` flips to 503 `not_ready`, wait `SHUTDOWN_DRAIN_DELAY`,
drain HTTP (10 s), cancel and await the three loops (10 s each), close the pool. If a
loop returns on its own the process exits non-zero so the orchestrator restarts it.
Room for the whole sequence: 5 + 10 + 10 s, so a pod needs a termination grace of
30 s or more.

## Probes

- `GET /healthz` is liveness: 200 `{"status":"ok"}` while the process serves, even
  while draining or while a dependency is down.
- `GET /readyz` is 200 `{"status":"ready"}` only if the drain gate is open, Postgres
  answers a ping and a Kafka broker answers a metadata request. The request names
  no topics (an empty, non-nil list), so it never creates a topic and does not need
  the output topic to exist; a fresh connection is dialled on every check. While
  draining it answers 503 `{"status":"not_ready"}`; while a dependency is down, 503
  `application/problem+json` of type `dependency-unavailable` naming it. Verdicts
  are cached 3 s.

## The outbox in operation

- Events are written in the same transaction as the aggregates and published later,
  so a write is accepted while Kafka is down; `outbox_events.published_at IS NULL`
  rows wait and are published, in id order, when the broker is back.
- Delivery is at-least-once with a stable CloudEvents `id`; consumers deduplicate.
- A row that failed `OUTBOX_MAX_ATTEMPTS` times is skipped (logged once at ERROR
  with id, topic, type and key) and stays in the table with `attempts` and
  `last_error`. Fix the cause, then re-arm it:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`. While a row is skipped,
  later events of the same aggregate can overtake it.

## The consumer in operation

- `retailnetwork.node-topology.events` is read under `NODE_TOPOLOGY_CONSUMER_GROUP`;
  the offset is committed after the event was handled, and each event is applied in
  one transaction together with its `processed_events` row, so a redelivery changes
  nothing.
- A transient failure is retried five times with a capped backoff and then the
  message is published to `retailnetwork.node-topology.events.dlq` with the headers
  `x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`,
  `x-dlq-error` and `x-dlq-failed-at`. An event that can never be applied (no
  `nodeId`, no instant, a value the use case refuses) is dead-lettered at once. A
  message that is not a CloudEvent, or a type this service does not consume, is
  skipped. Nothing here drains the DLQ topic: that is an operator task.

## Deployment (Helm chart)

`charts/offer-availability` deploys the one binary as a single `api` Deployment with its Service, ConfigMap, ServiceAccount and database
Secret; the HPA, Ingress and Gateway API HTTPRoute exist but default off. The image is `ghcr.io/iqvo/offer-availability`; an empty `image.tag`
means `v` + the chart `appVersion`, the tag the `release` job pushes. The pod runs as uid 1000 with a read-only root filesystem, all
capabilities dropped and the `RuntimeDefault` seccomp profile; `/healthz` backs the startup and liveness probes, `/readyz` the readiness probe.

| Value | Env var | Notes |
| --- | --- | --- |
| `database.url` or `database.existingSecret` (+ `existingSecretKey`, default `DATABASE_URL`) | `DATABASE_URL` | Required; always a `secretKeyRef`. |
| `database.migrationsExistingSecretKey` | `MIGRATIONS_DATABASE_URL` | Optional `secretKeyRef` (`optional: true`); the binary falls back to `DATABASE_URL`. |
| `kafka.brokers` | `KAFKA_BROKERS` | Required. |
| `config.nodeTopologyConsumerGroup` | `NODE_TOPOLOGY_CONSUMER_GROUP` | Required, no default; stable and service-prefixed. |
| `otel.endpoint`, `otel.serviceName` (`<name>-mcp` for the mcp component), `image.tag` or appVersion, `environment` | `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME`, `SERVICE_VERSION`, `ENVIRONMENT` | `otel.enabled=false` renders no `OTEL_*` env; `extraEnv` may not set these four. |
| `config.httpAddr`, `logLevel`, `outboxRelayInterval`, `outboxMaxAttempts`, `retentionInterval`, `shutdownDrainDelay` | `HTTP_ADDR`, `LOG_LEVEL`, `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS`, `RETENTION_INTERVAL`, `SHUTDOWN_DRAIN_DELAY` | Defaults repeat the binary's; the render test checks they agree. |

The chart fails at render for: no database source, no or blank `kafka.brokers`, an empty `config.nodeTopologyConsumerGroup`, any `*_MODE`
variable in `extraEnv`, and a `terminationGracePeriodSeconds` below `SHUTDOWN_DRAIN_DELAY` + 25 s (the shutdown budget above is drain delay +
10 s HTTP + 10 s loops; the default is 60 s, 30 s is the floor at the default 5 s drain). `config.shutdownDrainDelay` must be `0` or a whole
number of `ms`, `s` or `m` so the chart can add it up.

```sh
helm lint charts/offer-availability -f charts/offer-availability/ci/default-values.yaml
bash charts/offer-availability/tests/render_test.sh               # env contract, secrets, hardening, refusals
python3 charts/offer-availability/tests/test_service_selectors.py # the Service selects exactly one Deployment
```

CI runs `helm-lint` (`ct lint` plus the selector test) on pull requests and `chart-tests` on every push and PR; the `release` job packages
`offer-availability-<version>.tgz` and pushes it to `oci://ghcr.io/iqvo`.

## Tests

`make check` (format, vet, build, lint, unit + handler + BDD tests), `make coverage`
(90% over `internal/domain` and `internal/application`), `make mutation` (gremlins
per domain package), `make arch-test` (fitness), `make integration` (testcontainers
Postgres + Kafka: repositories, Drain, the relay, the middleware, the consumer flow
and the real-wiring test `cmd/api/e2e_integration_test.go`; needs Docker).
