# ADR 0010: A2A inbound adapter

## Status

Accepted (2026-10-29).

## Context

offer-availability has two inbound surfaces over its use cases besides the
Kafka consumer: REST (`cmd/api`, the only writer) and a read-only MCP server
(`cmd/mcp`, [ADR 0007](./0007-mcp-server-adoption.md)). The fleet now also
exposes each context as an Agent2Agent (A2A) agent, so that another agent can
*delegate a goal* to this context instead of stitching tool calls together.

The three transports do not overlap:

- **Kafka + CloudEvents** carry *facts* between contexts
  ([ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)).
- **MCP** carries an agent's *tools*.
- **A2A** carries the *delegation of a goal*, never data sync.

## Decision

- **A third driving adapter, a separate binary.**
  `internal/adapters/inbound/a2a` is built on the official A2A Go SDK
  (`github.com/a2aproject/a2a-go/v2`, pinned at v2.7.0) and served by
  `cmd/a2a` over JSON-RPC 2.0 on HTTP: the endpoint answers at `/` and at
  `/a2a`, the Agent Card at `/.well-known/agent-card.json` and an open
  `/healthz`, on `A2A_ADDR` (default `:8093`; 8080 is the api and 8090 the mcp
  server). It is a message-only agent: both skills are transactional
  request/response reads, so there is no Task lifecycle and the card does not
  advertise streaming. `cmd/a2a` is cloned from `cmd/mcp` (same composition,
  same boot migrations through `internal/bootretry`, same observability
  middleware).
- **The agent is READ-ONLY, because the MCP surface is.** A skill may change
  state only if the context's MCP surface already offers that write; ADR 0007
  made MCP read-only (creating an item, discontinuing it, listing an item on a
  node and applying a feed stay REST-only, with `Idempotency-Key` and the
  transactional outbox). Delegation must not widen what the context exposes,
  so the A2A agent has no write skill. Read-only is structural: `Deps` holds
  three read use cases and nothing that can write, and `cmd/a2a` neither dials
  Kafka nor starts the relay, the consumer or the janitor (no `KAFKA_BROKERS`,
  no `EVENT_PUBLISHER`, no outbox). The fitness tests of this ADR pin it (below).
- **Two curated, goal-shaped skills, derived from the MCP tools** (not one
  skill per endpoint):
  - `check_availability` (read): *where can this item be sold, and how much is
    there?* Arguments `catalogItemId?`, `nodeId?` (at least one is required),
    `limit?`, `after?`. With `catalogItemId` the answer composes the catalogue
    item (`get_catalog_item`) with one keyset page of its listings
    (`list_listings`); an unknown item is a 404, not an empty list. Each
    listing says LIVE or SUPPRESSED (with the reason) and the availability the
    last feed gave it.
  - `feed_outcome` (read): *what became of this vendor feed?* Argument
    `feedId` (`get_inventory_feed`): state APPLIED, SUPERSEDED or FAILED, the
    counters and the failure code.

  `browse_catalog` stays MCP-only: paging the whole catalogue is a tool, not
  a goal worth delegating.
- **Request contract.** The first message part is a Data part (or a Text part
  holding the same JSON) `{"skill":"<id>","arguments":{...}}`. Decoding is
  strict: `DisallowUnknownFields` plus a trailing-data check at both levels,
  so a misspelled filter cannot silently widen a read. The optional page size
  is a pointer, so an explicit `limit: 0` reaches the use case and is refused
  (`validation-failed`) instead of silently meaning "default".
- **Answers.** One agent Message carrying the caller's `contextId`/`taskId`:
  a Data part (the envelope `{skill, data | problem}`) and a Text part (a
  one-line summary).
- **Problems are answered in-band, never raised as JSON-RPC errors**, as
  RFC 7807/9457-shaped objects `{type, title, detail, status}` with the SAME
  `type` slugs and statuses as the REST adapter's problem catalogue
  (`internal/adapters/inbound/http/problems.go`), restricted to what a read can
  raise: `not-found` 404, `validation-failed` 422 (a bad page size; REST
  answers 422 there, so does A2A), `internal-error` 500 with a fixed detail
  (the cause is logged with the trace id, infrastructure text never reaches the
  caller). Adapter-owned request-shape problems are 400: `malformed-skill-request`,
  `unknown-skill` (every write name lands here), `invalid-argument`. The write-side
  slugs (`duplicate-listing`, `feed-already-applied`, ...) are unreachable from
  these skills. No shared domain error of a read names a deprecated alias, so no
  alias-hiding mapping is needed here.
- **Observability.** One OTel span per skill (`a2a.skill <id>`, attribute
  `a2a.skill`), on top of the server span and request log the router shares
  with `cmd/api` ([ADR 0009](./0009-fleet-observability-standard.md)); the
  service name defaults to `offer-availability-a2a`.
- **No sibling calls, no auth.** The agent answers from this context's own
  tables, calls no other context over REST, MCP or A2A, and carries **no
  outbound A2A client** (`TestNoOutboundA2AClient`). Like every fleet endpoint
  it is unauthenticated (the 2026-09-11 revert, `TestNoAuthMiddlewareReintroduced`
  scans this tree too); the Agent Card declares no security schemes on purpose,
  and access control is the in-cluster ClusterIP boundary.
- **Boot.** `DATABASE_URL` and `A2A_AGENT_URL` are mandatory, validated before
  telemetry, any connection and the listener, and every problem is reported at
  once, each naming its variable. `A2A_AGENT_URL` must be an absolute http(s)
  URL: the Agent Card advertises it, and a relative or schemeless URL would
  mislead every client. There is no in-memory mode.
- **Deployment.** The Helm chart gets `a2a-deployment.yaml`, `a2a-service.yaml`
  and `a2a-route.yaml` (component `a2a`, command `/app/a2a`, port 8093) and an
  `a2a:` values block, **off by default**. `A2A_AGENT_URL` defaults to
  `http://<fullname>-a2a.<namespace>.svc.cluster.local:8093/` and is overridden
  by `a2a.agentUrl`; the optional Kong HTTPRoute maps `/api/a2a/offer-availability`
  to `/` (so the card stays reachable under the same prefix). The Dockerfile
  already builds every `cmd/*` binary into `/app`.

## Consequences

- A delegating agent can answer "who can ship item X, how much, and did the
  vendor's feed land" with one message per goal, over the same use cases and
  the same error vocabulary as REST and MCP: one action surface, three
  protocols.
- Adding a skill is a reviewed act: the card test, the read-use-case allowlist
  of `internal/architecture/a2a_fitness_test.go` and this ADR change together.
  A write skill needs the MCP charter to change first (an amendment of ADR 0007).
- Enforced by `internal/architecture`: `TestA2AAdapterDependencyRule` (only
  application + domain; nothing depends on it), `TestA2AAdapterIsReadOnly` (the
  `Deps` fields and every `usecases.X` reference are on the read allowlist),
  `TestA2ABinaryIsReadOnlyAndKafkaFree` (no Kafka, outbox or CloudEvents import
  in `cmd/a2a` or the adapter), `TestNoOutboundA2AClient` and
  `TestA2ASDKOnlyInTheA2AAdapterAndBinary`. The integration test of `cmd/a2a`
  proves, on a real Postgres, that the agent reads what `cmd/api`'s use cases
  wrote and adds no outbox row.
- The write side stays exactly where it was: REST with `Idempotency-Key`, the
  transactional outbox and the consumers of
  [ADR 0005](./0005-runtime-relay-consumer-and-idempotency.md).
