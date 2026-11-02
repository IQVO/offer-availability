---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

`offer-availability` ships **three binaries**, `cmd/api`, `cmd/mcp` and `cmd/a2a`, and all three are configured only
through environment variables. There is no config file, no flag and no secret-manager integration in the repository.
REST, MCP and A2A are unauthenticated fleet-wide, so there is no token or key to configure.

The service runs on real Postgres and real Kafka only. `cmd/api` has three mandatory variables (`DATABASE_URL`,
`KAFKA_BROKERS`, `NODE_TOPOLOGY_CONSUMER_GROUP`); `cmd/mcp` needs `DATABASE_URL`; `cmd/a2a` needs `DATABASE_URL` and
`A2A_AGENT_URL`. A missing mandatory variable is a boot error that names it, raised before the listener binds. Only
`cmd/api` dials Kafka: `cmd/mcp` and `cmd/a2a` have no `KAFKA_BROKERS`, no consumer group and no outbox.

Every variable below is read in the binary's `main` package or in the `internal/adapters/outbound/postgres` and
`internal/adapters/outbound/telemetry` helpers; no other package reads the environment. The test-only variables of the
integration harness (testcontainers) are excluded. An invalid *optional* value logs a warning and falls back to its
default; the exceptions are noted per row.

## `cmd/api`

| Variable | Default | Required? | Meaning |
| --- | --- | --- | --- |
| `DATABASE_URL` | none | **yes** | Postgres DSN of the service pool (10 connections, `statement_timeout = 10s`). Missing = boot error `missing required environment variable: DATABASE_URL`. The embedded migrations run at boot; the outbox, the `Idempotency-Key` store and every repository live here. Source: `cmd/api/config.go` (`loadConfig`), `internal/adapters/outbound/postgres/config.go`. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (non-pooled) DSN used only for the boot migrations, because golang-migrate holds a session advisory lock that breaks behind a transaction-pooling proxy. Empty falls back to `DATABASE_URL`. |
| `KAFKA_BROKERS` | none | **yes** | Comma-separated `host:port` list (entries trimmed, blanks dropped). Missing or blank = boot error `missing required environment variable: KAFKA_BROKERS (comma-separated host:port list)`. Used by the outbox relay, the node-topology consumer and the `/readyz` metadata check. From the host, the fleet's single broker is `localhost:9092`. |
| `NODE_TOPOLOGY_CONSUMER_GROUP` | none | **yes** | Consumer group id of the node-topology consumer. Never defaulted, so a local process cannot join a live cluster's group by accident. Missing or blank = boot error `missing required environment variable: NODE_TOPOLOGY_CONSUMER_GROUP`. |
| `HTTP_ADDR` | `:8080` | no | Listen address of the REST server (also serves `/healthz` and `/readyz`). |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Pause between relay passes that did not fill a batch (batch = 100 rows). A Go duration (`500ms`, `2s`) or plain seconds (`2`, `0.5`). Unparsable or non-positive = warning + default. |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failures after which the relay stops claiming an outbox row. `0` = unlimited. Negative or unparsable = warning + `20`, so a typo never removes the cap ([ADR 0005](/docs/adr/0005-runtime-relay-consumer-and-idempotency)). |
| `FEED_APPLY_CHUNK_SIZE` | `500` | no | Batch cap while an inventory feed settles: at most this many changed listings per bulk save and this many events per outbox append. A full feed can touch every listing of a node, so bounded batches keep each lock-held write small; the feed itself still settles in one transaction. Negative or unparsable = warning + `500`. |
| `RETENTION_INTERVAL` | `1h` | no | How often the retention janitor purges idempotency keys older than 24 h and feeds and processed events older than 30 days. Go duration; unparsable or non-positive = warning + `1h`. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | After SIGINT/SIGTERM `/readyz` flips to 503 and the process waits this long before closing the listener. `0` disables the wait. Negative or unparsable = warning + `5s`. |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`, case-insensitive; anything else is `info`. Logs are JSON on stdout. |
| `OTEL_SERVICE_NAME` | `offer-availability` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint, bare `host:port` (becomes `http://host:port`) or a URL. Export never blocks: an unreachable Collector costs telemetry, never availability. |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. |

## `cmd/mcp`

Read-only Streamable-HTTP MCP server over the service database ([ADR 0007](/docs/adr/0007-mcp-server-adoption)). Starts no relay,
consumer or janitor and never dials Kafka. Source: `cmd/mcp/main.go`, `cmd/mcp/telemetry.go`.

| Variable | Default | Required? | Meaning |
| --- | --- | --- | --- |
| `MCP_ADDR` | `:8090` | no | Listen address of the MCP server. |
| `DATABASE_URL` | none | **yes** | Postgres DSN. Missing = boot error `DATABASE_URL is required: this context runs on real Postgres`. The binary also runs the idempotent embedded migrations at start; golang-migrate's advisory lock makes concurrent starts with `cmd/api` safe. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations only. |
| `LOG_LEVEL` | `info` | no | As in `cmd/api`. |
| `OTEL_SERVICE_NAME` | `offer-availability-mcp` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint. |
| `SERVICE_VERSION` | `dev` | no | `service.version`. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name`. |

## `cmd/a2a`

Read-only A2A (Agent2Agent) agent, JSON-RPC 2.0 over HTTP, Agent Card at `/.well-known/agent-card.json`
([ADR 0010](/docs/adr/0010-a2a-inbound-adapter)). Starts no relay, consumer or janitor and never dials Kafka. Source:
`cmd/a2a/main.go`, `cmd/a2a/telemetry.go`.

| Variable | Default | Required? | Meaning |
| --- | --- | --- | --- |
| `A2A_ADDR` | `:8093` | no | Listen address of the A2A server. |
| `A2A_AGENT_URL` | none | **yes** | Absolute `http(s)` URL of this agent's JSON-RPC endpoint, advertised on the Agent Card (for example `http://<release>-a2a.<ns>.svc.cluster.local:8093/`). Missing, relative or schemeless = boot error before telemetry, any connection and the listener. |
| `DATABASE_URL` | none | **yes** | Postgres DSN. Missing = boot error `DATABASE_URL is required: this context runs on real Postgres`. All boot problems are reported together. Runs the embedded migrations at start, like `cmd/mcp`. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations only. |
| `LOG_LEVEL` | `info` | no | As in `cmd/api`. |
| `OTEL_SERVICE_NAME` | `offer-availability-a2a` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint. |
| `SERVICE_VERSION` | `dev` | no | `service.version`; also the version printed on the Agent Card. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name`. |

## Compiled-in settings (not configurable)

These are constants in the source; changing them needs a code change.

| Setting | Value | Source |
| --- | --- | --- |
| HTTP `ReadHeaderTimeout` | 5 s (all three binaries) | `cmd/*/main.go`, `cmd/api/config.go` |
| HTTP graceful shutdown timeout / per-worker drain timeout | 10 s / 10 s (`cmd/api`); 10 s (`cmd/mcp`, `cmd/a2a`) | `cmd/api/config.go` (`shutdownTimeout`, `workerDrainTimeout`) |
| Postgres pool | 10 connections, `statement_timeout = 10s` | `internal/adapters/outbound/postgres/pool.go` |
| Boot retry (migrations, first ping) | 5 attempts, backoff 1 s, 2 s, 4 s, 8 s | `internal/bootretry/bootretry.go` |
| Outbox relay batch size | 100 rows per pass | `internal/adapters/outbound/outbox/relay.go` (`DefaultBatchSize`) |
| Kafka producer | acks=all, hash balancer on the message key, 10 ms batch timeout, `AllowAutoTopicCreation` | `internal/adapters/outbound/kafka/writer.go`, `relay_sink.go` |
| Consumer retry backoff | 200 ms doubling to a 5 s cap, then the dead-letter topic `<topic>.dlq` | `internal/adapters/inbound/kafka/kafka.go`, `deadletter.go` |
| `/readyz` verdict cache and check timeout | 3 s cache, 2 s per round of checks | `internal/adapters/inbound/http/readiness.go` |
| Request body limit | 64 KiB | `internal/adapters/inbound/http/response.go` (`maxBodyBytes`) |
| `Idempotency-Key` | length at most 200, stored 24 hours | `internal/adapters/inbound/http/idempotency.go` |
| Inventory feed size | at most 10000 lines | `internal/domain/feed/feed.go` (`MaxLines`) |
| Pack quantity ceiling | 1000000 | `internal/domain/catalog/packaging.go` (`MaxPackQuantity`) |
| Topic produced | `retailnetwork.offer-availability.events` | `internal/adapters/outbound/kafka/encoder.go` |
| Topic consumed | `retailnetwork.node-topology.events` (dead letters to `retailnetwork.node-topology.events.dlq`) | `internal/adapters/inbound/kafka/node_consumer.go` |

## What is deliberately absent

- **No auth variables.** REST, MCP and A2A are unauthenticated; access control is the in-cluster ClusterIP boundary.
- **No `*_MODE` variables.** There is no in-memory runtime; the chart refuses a `*_MODE` entry in `extraEnv`.
- **No CORS, rate-limit or feature-flag variables.**
- **Chart values.** `charts/offer-availability` maps these variables onto the pods (`database.*`, `kafka.brokers`,
  `config.*`, `otel.*`); see [Runtime](/docs/overview/runtime) and the [Runbook](/docs/operations/runbook).
