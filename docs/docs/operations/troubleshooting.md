---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

Symptom, cause, check, fix, built from the failure modes visible in the code. Log lines and REST bodies are quoted from
`cmd/api` and checked against a real run ([Quickstart](/docs/overview/quickstart)). SQL probes use the tables listed in
the [Runbook](/docs/operations/runbook#migrations). This service calls no other service over HTTP, MCP or A2A, so there
is **no circuit breaker** to trip; the dependencies that can fail are Postgres and Kafka.

## Boot

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| `cmd/api` exits 1 at once: `missing required environment variable: DATABASE_URL` (same wording for `KAFKA_BROKERS (comma-separated host:port list)`, `NODE_TOPOLOGY_CONSUMER_GROUP` and `SELLER_INBOUND_CONSUMER_GROUP`) | A mandatory variable is unset or blank; there is no in-memory or consumer-less mode | The error returned by `run` (`cmd/api/config.go`, `loadConfig`); the pod's env | Set it ([Configuration](/docs/operations/configuration)); the group id is a stable service-prefixed name you choose, never defaulted |
| `cmd/mcp` or `cmd/a2a` exits 1: `DATABASE_URL is required: this context runs on real Postgres` | No database DSN | The binary's stderr | Set `DATABASE_URL` |
| `cmd/a2a` exits 1 before it binds, naming `A2A_AGENT_URL` | Unset, relative or schemeless agent URL; all boot problems are reported together | `cmd/a2a/main.go` | An absolute `http(s)` URL, for example `http://localhost:8093/` |
| Warning `invalid OUTBOX_RELAY_INTERVAL; using the default` (also `OUTBOX_MAX_ATTEMPTS`, `RETENTION_INTERVAL`) | Optional value does not parse or is non-positive | The WARN line has `value` and `default` | Fix the value, or unset it; the service runs on the default meanwhile |
| Exits 1 after about 15 s: `run migrations` or `ping postgres` errors after WARN `retrying` lines (`op`, `attempt`, `in`, `err`) | Postgres unreachable, wrong DSN, or a migration fails; 5 attempts with 1, 2, 4, 8 s backoff, then the process refuses to boot | The WARN lines and the final error; `psql "$DATABASE_URL"` from the same network | Fix DSN or network. The retry exists because the first dial of an injected pod can be reset, it is not a license to boot without a database |
| Migration fails on every later boot | A partial migration left `schema_migrations.dirty = true` and golang-migrate refuses to continue | `SELECT version, dirty FROM schema_migrations;` | Repair the schema by hand and force the version with the golang-migrate tooling (take a backup first) |
| Migrations hang or fail behind PgBouncer | `DATABASE_URL` is a transaction-pooling address, which breaks golang-migrate's session advisory lock | Is the DSN a pooler? | Set `MIGRATIONS_DATABASE_URL` to a direct connection |
| `helm template` / `helm install` fails | The chart refuses: no database source, no or blank `kafka.brokers`, empty `config.nodeTopologyConsumerGroup`, a `*_MODE` entry in `extraEnv`, `terminationGracePeriodSeconds` below `SHUTDOWN_DRAIN_DELAY` + 25 s, or a `config.shutdownDrainDelay` that is not `0` or a whole number of `ms`, `s`, `m` | The `fail` message names the value | Fix the values ([Runbook](/docs/operations/runbook#deployment-helm-chart)) |

## Probes and runtime

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| `/readyz` 503 `{"status":"not_ready"}` | The process is draining after SIGINT/SIGTERM | Is the pod terminating? | Nothing to fix; a new pod takes over |
| `/readyz` 503 `application/problem+json`, type `dependency-unavailable`, detail naming `postgres` or `kafka` | Postgres does not answer a ping, or no Kafka broker answers a metadata request. A missing topic never causes it: the request names no topic | WARN `readiness: dependency is not ready` has `dependency` and `error` | Restore the dependency; the verdict is re-checked after at most 3 s. `/healthz` stays 200 throughout |
| `/readyz` 200 but requests fail with 500 | One query failing for another reason (statement timeout 10 s, constraint, bug); readiness only proves a ping | `http request` ERROR lines with `status` 500; `panic in handler`; `psql` from the pod | Fix the cause shown in the log |
| A write succeeds (201) but nothing appears on Kafka | Expected while the broker is down: the event waits in `outbox_events` | `SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL;` and `/readyz` | Restore Kafka; the relay drains in `id` order |
| `cmd/mcp` or `cmd/a2a` has no `/readyz` | They expose `/healthz` only | | Probe `/healthz` |
| Process exits non-zero with ERROR `outbox relay stopped`, `node-topology consumer stopped` or `retention janitor stopped` | A worker loop returned on its own; by design the process then exits so the orchestrator restarts it | The `error` field of that ERROR line, then `... stopped unexpectedly: exiting so the orchestrator restarts the service` | Fix the named cause; `kubectl rollout restart deploy/offer-availability` |

## Kafka, the outbox and the consumer

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| ERROR `outbox relay pass failed` repeating, `published_at` stays NULL | Broker unreachable, or auto-creation of `retailnetwork.offer-availability.events` is disabled and the topic is missing (the first pass in a fresh local broker can fail once with `Unknown Topic Or Partition` and the next pass succeeds) | The `error` field; `last_error` and `attempts` on the head row | Restore the broker or create the topic. The pass retries every `OUTBOX_RELAY_INTERVAL` |
| ERROR `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause` | A poison row, or an outage longer than `OUTBOX_MAX_ATTEMPTS` passes (default 20, about 20 s at 1 s) | `SELECT id, topic, event_type, attempts, last_error FROM outbox_events WHERE published_at IS NULL AND attempts >= 20;` | Fix the cause, then `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`. While a row is excluded, later events of the same aggregate can overtake it |
| Node events are produced but listings never change; no `event processed` line | The consumer is not attached: the consumed topic did not exist when `cmd/api` started (observed in the capture: events produced later were never received until a restart), or another process holds the consumer group | `node-topology consumer running` (`topic`, `group`) at boot; `kafka-consumer-groups.sh --describe --group <group>` (no assignment = not attached) | Create `retailnetwork.node-topology.events` before starting, then restart the api. Never share `NODE_TOPOLOGY_CONSUMER_GROUP` with another environment |
| Seller events are produced but a suspended seller's listings stay `LIVE`; no `event processed` line for `SellerSuspended` | The seller-inbound consumer is not attached: `retailnetwork.seller-inbound.events` did not exist when `cmd/api` started, or another process holds `SELLER_INBOUND_CONSUMER_GROUP` | `seller-inbound consumer running` (`topic`, `group`) at boot; `kafka-consumer-groups.sh --describe --group <group>` | Create `retailnetwork.seller-inbound.events` before starting, then restart the api (ADR 0013) |
| WARN `node-topology consumer broker connection stalled or lost; fetching again after the backoff` | Transient timeout, reset or EOF on the fetch; up to 10 consecutive failures are slept off with capped backoff | `attempt` and `backoff` fields | None while it recovers. After 10 failures, or on a non-transient error such as connection refused, the loop returns and the process exits for a restart |
| ERROR `node-topology consumer handling failed; retrying the same message` (`partition`, `offset`, `attempt`, `max_attempts` 5, `retry_in`) | A transient failure, usually Postgres: the unit of work rolled back | The `error` field; database health | Fix the dependency. After 5 attempts the message goes to the DLQ and the partition moves on |
| ERROR `node-topology consumer message can never be applied; dead-lettering it without retry` or WARN `an event can never be applied; it goes to the dead-letter topic` | Payload without `nodeId` or without the instant (`activatedAt`, `suspendedAt`, `retiredAt`, or the CloudEvents `time` for `NodeRegistered`), undecodable `data`, or a value the use case refuses | The `type` and `event_id` fields; the `x-dlq-error` header of the DLQ record | Fix the producer (node-topology), then re-publish the original value to the source topic; the inbox makes a re-send harmless |
| Messages on `retailnetwork.node-topology.events.dlq` | See the two rows above | `kafka-console-consumer.sh --topic retailnetwork.node-topology.events.dlq --from-beginning --property print.headers=true`; headers `x-dlq-source-topic`, `x-dlq-source-partition`, `x-dlq-source-offset`, `x-dlq-error`, `x-dlq-failed-at` | Nothing drains the DLQ; it is an operator task ([Runbook](/docs/operations/runbook#the-consumer-and-the-dead-letter-topic)) |
| WARN `skipping a message that is not a valid CloudEvent` | A non-CloudEvents message on the topic | The `error` field | None; it is skipped and the offset moves on. A valid CloudEvent of a type this service does not consume is skipped silently |
| A listing stays `SUPPRESSED` with `NODE_NOT_SELLABLE` | The local node status is not `ACTIVE`: no `NodeActivated` was consumed (an unknown node is not sellable), or the node was suspended or retired | `SELECT * FROM node_status_view WHERE node_id = '<node>';` | Wait for or re-send `NodeActivated`. A `RETIRED` node never comes back, and an older event never overrides a newer status |
| A listing stays `SUPPRESSED` with `DISCONTINUED` | Its catalogue item was discontinued; discontinuation is final | `GET /catalog/{catalogItemId}` shows `state: DISCONTINUED` | Create a new catalogue item; a node event never restores this reason |

## Inventory feeds

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| `POST /inventory-feeds` 422 `listing-not-on-node` | A line names a listing that does not exist or belongs to another node. The feed is **recorded as `FAILED`** and nothing else changes | `GET /inventory-feeds/{feedId}` shows `state: FAILED`, `failureCode` | Correct the lines and re-send the **same `feedId`**: a `FAILED` feed may be re-sent, with a new `Idempotency-Key` |
| 422 `negative-availability` | A line has `availableQty` below 0; recorded as `FAILED` | As above | Correct the quantity |
| 409 `feed-already-applied` | The `feedId` is already `APPLIED` or `SUPERSEDED` | `GET /inventory-feeds/{feedId}` | Use a new `feedId` for new data |
| A feed is `SUPERSEDED`, no listing changed | Its `asOf` is older than the last `APPLIED` feed of the node (kept per node in `node_feed_marks`) | `SELECT * FROM node_feed_marks WHERE node_id = '<node>';` | Expected ordering behaviour; send a feed with a later `asOf` |
| `appliedCount` is lower than `lineCount`, `staleCount` above 0 | A line is older than the availability the listing already holds | `staleCount` on the feed | Expected; that line is skipped |
| A full feed (`fullUpdate: true`) zeroed listings | By design: listings of the node omitted from a full feed are set to 0 | `zeroedCount` | Send the missing lines, or use a partial feed |
| 422 `validation-failed` on a feed | More than 10000 lines, a partial feed with no line, the same listing twice, a bad id, a missing `asOf`, an unknown `basis`, a quantity above 2147483647 | The `detail` text; nothing is recorded for a malformed request | Fix the request |

## REST problem types

Every error is `application/problem+json` with `type` (a slug), `title`, `status`, `detail` and `instance`. The slugs are
the catalogue in `internal/adapters/inbound/http/problems.go`.

| Status | Slug | When | What to do |
| --- | --- | --- | --- |
| 400 | `malformed-request-body` | Invalid JSON, unknown field, wrong type, trailing data, or a NUL character | Fix the body against `apis/openapi.yaml` |
| 400 | `malformed-request-query` | A bad query string (a NUL character, an unparsable value) | Fix the query |
| 400 | `idempotency-key-required` | A state-changing route without an `Idempotency-Key` header | Send a unique key per logical request |
| 400 | `idempotency-key-invalid` | Key longer than 200 characters or with control characters | Use a shorter printable key |
| 404 | `not-found` | Unknown catalogue item or feed id, or no route matches | Normal for an unknown id |
| 404 | `pack-level-not-found` | The item never declared that pack level | Declare it first |
| 405 | `method-not-allowed` | Wrong verb for a path | |
| 409 | `illegal-transition` | A state change the aggregate refuses | Read the `detail` |
| 409 | `concurrent-update` | Two writers raced on one aggregate; the version guard failed | Re-fetch and retry |
| 409 | `gtin-already-assigned` | The GTIN already identifies an item or a pack level (retired ones included) | A GTIN identifies one thing forever; use another |
| 409 | `pack-quantity-immutable` | `unitsPerPack` of an active pack level cannot change | Retire the level and declare a new GTIN |
| 409 | `duplicate-listing` | A listing of this item on this node (and owner) exists | Read it with `GET /listings?nodeId=&catalogItemId=` |
| 409 | `stale-availability`, `node-not-sellable` | Domain refusals of the listing aggregate | Read the `detail` |
| 413 | `request-body-too-large` | Body over 64 KiB | Split the request (a feed carries at most 10000 lines) |
| 422 | `invalid-gtin` | Not 8, 12, 13 or 14 digits, a wrong mod-10 check digit, or a variable-measure GTIN | Fix the GTIN |
| 422 | `invalid-item-dimensions`, `invalid-pack-dimensions`, `invalid-pack-quantity`, `invalid-pack-level`, `pack-level-order` | Packaging rules (ADR 0006): positive dimensions, `unitsPerPack` up to 1000000 and consistent with the child chain, level one of `EACH`, `INNER`, `CASE`, `PALLET`, an active lower child level | Read the `detail` |
| 422 | `negative-availability`, `listing-not-on-node`, `validation-failed` | Feed and value-object rules | See the feed table |
| 422 | `idempotency-key-reused` | The same key with another method, path or body, or the key expired while the request was in flight | New key, or resend the original request to get the stored answer |
| 500 | `internal-error` | Unmapped error or handler panic; `detail` is always `an unexpected error occurred` | Read the `http request` ERROR line and `panic in handler` |
| 503 | `dependency-unavailable` | `GET /readyz` only | See the `/readyz` row above |

### Idempotency in practice

- Every `POST` and `PUT` is keyed by (route, `Idempotency-Key`). The same key with the same method, path and body replays
  the first answer with `Idempotency-Replayed: true`, for 24 hours.
- A stored answer is replayed whatever its status below 500: 2xx and 4xx are both stored. A 5xx is never stored, so the
  same key can retry the real work.
- A 4xx rolls the handler's writes back, except a `FAILED` feed, which is kept on purpose.
- Two concurrent requests with one new key serialize on the `idempotency_keys` primary key; the second replays the first.
- Expired keys are purged by every keyed write and by the hourly janitor.

## MCP and A2A

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| MCP `tools/list` refused: `method "tools/list" is invalid during session initialization` | No `initialize` and `notifications/initialized` on this session | [Quickstart](/docs/overview/quickstart#7-first-calls-on-mcp) | Initialise first and send `Mcp-Session-Id` |
| An MCP tool answers `isError: true` with `not-found: not found` | Unknown id | | Normal |
| A tool fails with `validation-failed` | `limit` outside 1 to 500 | | Use a valid page size |
| Writes over MCP or A2A | Not available: both are read-only (ADR 0007, ADR 0010) | | Use REST |
| A2A answer carries a problem instead of data | `malformed-skill-request` (no parts, no `skill`, unknown field), `unknown-skill`, `invalid-argument` (all status 400 in the data part), or `not-found` / `validation-failed` from the read; answered as a business reply, not a transport error | The `problem` in the reply's data part | Fix the request ([API overview](/docs/api-reference)) |
| Agent Card advertises the wrong URL | `A2A_AGENT_URL` | `GET /.well-known/agent-card.json` | Set the externally reachable URL |
