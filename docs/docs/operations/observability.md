---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

All three binaries follow the fleet observability standard ([ADR 0009](/docs/adr/0009-fleet-observability-standard)):
JSON logs on stdout, OpenTelemetry traces and metrics exported over OTLP/gRPC, W3C trace context across Kafka. Export
never blocks: the gRPC exporters connect lazily, so a Collector that is down or absent costs telemetry and nothing
else. The wiring is `internal/adapters/outbound/telemetry/telemetry.go`; each binary calls it before it builds its
router (`cmd/*/telemetry.go`) and flushes it last on shutdown (5 s bound).

## Exporter and resource configuration

| Variable | Default | Effect |
| --- | --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OTLP/gRPC Collector, bare `host:port` or URL (a bare value becomes `http://host:port`). Traces and metrics share it. |
| `OTEL_SERVICE_NAME` | `offer-availability` (`cmd/api`), `offer-availability-mcp`, `offer-availability-a2a` | `service.name`; also the Loki `app` label source and the `service_name` metric label. |
| `SERVICE_VERSION` | `dev` | `service.version`. |
| `ENVIRONMENT` | `local` | `deployment.environment.name`. |
| `OTEL_RESOURCE_ATTRIBUTES` | none | Read by the SDK's default resource (`resource.Default()`), not by the service. |

The metrics reader pushes every 15 s. Propagators are W3C `traceparent` / `tracestate` and baggage. The SDK's own
diagnostics are bridged onto the JSON logger and an exporter error is logged at DEBUG only (`slog.Debug("opentelemetry
error")`), so `LOG_LEVEL=debug` is how you see a broken pipeline. The Helm chart renders these variables from `otel.*`
and `environment` (the mcp and a2a components get `<name>-mcp` and `<name>-a2a` as `otel.serviceName`).

## Metrics

| Instrument | Type | Unit | Attributes | Emitted by |
| --- | --- | --- | --- | --- |
| `availability.inventory_feeds.applied` (Prometheus `availability_inventory_feeds_applied_total`) | Int64 counter | `{feed}` | `outcome` = `applied` \| `superseded` \| `failed` \| `rejected`; `reason` on `failed` and `rejected` only | `cmd/api` (`internal/adapters/outbound/telemetry/feed_metrics.go`, meter `github.com/claudioed/offer-availability`) |
| `http.server.request.duration` (Prometheus `http_server_request_duration_seconds`) | histogram | `s` | HTTP method, `http.route`, status code (set by `otelchimetric`) | all three binaries (`otelchimetric.NewServerRequestDuration`) |
| Go runtime metrics (goroutines, memory used, GC) | gauges / counters | per OTel runtime contrib | runtime defaults | all three binaries (`runtime.Start`) |

`reason` is a problem slug from `internal/application/usecases/feeds_metrics.go`: `listing-not-on-node`,
`negative-availability`, `feed-already-applied`, `concurrent-update`, `validation-failed`, or `internal-error` for
anything else (a database error, a bug). The four outcomes mean: `applied` (listings changed), `superseded` (a newer feed
of the node was already applied, stored as `SUPERSEDED`), `failed` (stored as `FAILED`, the REST answer is 422) and
`rejected` (refused with nothing recorded, for example an already-applied feed id). There is no instrument for the outbox
backlog, relay attempts or consumer lag: read those from SQL and the logs ([Runbook](/docs/operations/runbook)).

## Traces

| Span | Kind | Created by | Attributes |
| --- | --- | --- | --- |
| Server span named after the chi route pattern (one name per route, never per id) | server | `otelchi.Middleware` in `cmd/api`, `cmd/mcp`, `cmd/a2a` | HTTP semantic conventions |
| `<topic> process` (`retailnetwork.node-topology.events process`) | consumer | node-topology consumer, one per handling attempt (`internal/adapters/inbound/kafka/tracing.go`) | `messaging.system=kafka`, `messaging.destination.name`, `messaging.operation.name=process`; errors recorded on the span |
| `a2a.skill <skill>` | internal | A2A skill dispatcher (`internal/adapters/inbound/a2a/skills.go`) | `a2a.skill`; status Error with the problem detail when the skill answers a problem |

Trace context crosses Kafka in both directions. When a request or a consumer span raises events, the active span context
is stamped as `traceparent`/`tracestate` into the outbox row's headers in the same transaction
(`internal/adapters/outbound/postgres/outbox_trace.go`) and the relay publishes them as Kafka headers; the consumer
extracts `traceparent` from the headers of a node-topology message, so an event published by node-topology with
those headers parents `<topic> process`. The relay itself creates no span.

## Logs

JSON via `slog` on stdout, level from `LOG_LEVEL`. A handler wrapper adds `trace_id` and `span_id` to every record written
with a span-carrying context (the `*Context` slog variants), so a log line links to its trace.

| Field | Where | Meaning |
| --- | --- | --- |
| `time`, `level`, `msg` | every record | RFC 3339 time, `DEBUG`/`INFO`/`WARN`/`ERROR`, message. |
| `method`, `route`, `status`, `duration_ms`, `bytes`, `request_id` | `msg="http request"` (REST and MCP/A2A routers) | One line per request; `route` is the matched pattern, never the raw path; 5xx is ERROR, 4xx WARN, the rest INFO. `/healthz` and `/readyz` are not logged. |
| `trace_id`, `span_id` | records written inside a span | Correlate with Tempo/Jaeger. |
| `type`, `event_id`, `subject` | `msg="event processed"` | A node-topology event was applied. |
| `error`, `topic`, `type`, `key`, `id` | outbox relay records | `outbox relay pass failed`; once per row at the attempt cap `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause`. |
| `idempotency_keys`, `feeds`, `processed_events` | `msg="retention purge"` | Rows the janitor deleted in a pass. |
| `worker` | `msg="worker did not stop before the shutdown drain deadline"` | A loop did not stop within 10 s on shutdown. |

Startup lines worth knowing: `telemetry configured` (`service_name`, `otlp_endpoint`), `postgres adapters configured`,
`outbox relay running`, `node-topology consumer running` (`topic`, `group`), `retention janitor running`,
`http server listening`. Shutdown lines: `shutdown: readiness flipped to not-ready; waiting for traffic to drain`.

## Dashboards and alerts that exist

Only what is in `warehouse-infra` on `develop`:

- **Dashboard** `warehouse-offer-availability` (`terraform/dashboards/contexts/offer-availability.json`): Kong request
  rate, 5xx ratio and p95 request/upstream latency for the context's routes; service HTTP RED (request rate by route,
  5xx by Kong route, p95 by process, from `http_server_request_duration_seconds` with `service_name=~"offer-availability.*"`);
  the business panel `availability_inventory_feeds_applied_total` by `outcome`; Go runtime goroutines and memory; Loki
  live log stream, log volume by level and errors/warnings for `{app="offer-availability"}`.
- **Alerts** (`terraform/alerting/warehouse-alert-rules.json`, groups `warehouse-offer-availability` and
  `warehouse-offer-availability-slo`): `WarehouseKong5xxRatioHigh` (more than 5 % 5xx on a route over 5 minutes),
  `WarehousePodRestarting` (two or more container restarts in 15 minutes for pods `offer-availability-.+`), and the
  multi-window burn-rate alerts `WarehouseApiAvailabilityBurnFast` / `BurnSlow` (99 % availability SLO) and
  `WarehouseApiLatencyBurnFast` / `BurnSlow` (95 % under 500 ms, evaluated at the 400 ms histogram bucket).
- There is no alert on the outbox backlog, the dead-letter topic or the feed outcomes. The alert annotations' `runbook_url`
  points at `docs/operations/runbook.md` on `develop` in this repository, a path that this site's [Runbook](/docs/operations/runbook)
  replaces (see the report; `warehouse-infra` is not changed by this documentation).

## Quick checks

```sh
# is telemetry configured and where does it go?
kubectl -n warehouse-systems logs deploy/offer-availability | grep 'telemetry configured'
# feed outcomes in Prometheus
sum by (outcome, reason) (rate(availability_inventory_feeds_applied_total[5m]))
# errors and warnings in Loki
{app="offer-availability", level=~"ERROR|WARN"}
```
