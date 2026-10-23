---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/outbox/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

offer-availability PUBLISHES seven integration events to
`retailnetwork.offer-availability.events` and CONSUMES four node-topology events
from `retailnetwork.node-topology.events` (ADR 0003) under the consumer group
`NODE_TOPOLOGY_CONSUMER_GROUP`. The topic `retailnetwork.offer-availability.analytics`
is unused: no event is analytics-only. Never produce or consume a `warehouse.*` topic.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces is a CloudEvents 1.0 event in
structured content mode. This is a hard rule, not a preference:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write, no
  envelope toggle env var. `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build and validate the envelope with `github.com/cloudevents/sdk-go/v2/event`
  through ONE helper package, `internal/adapters/kafka/cloudevents` (`New`,
  `Decode`, `ContentTypeHeader`). Transport stays `segmentio/kafka-go`; no
  hand-rolled CloudEvent structs. The only producer-side caller is the encoder
  `internal/adapters/outbound/kafka/encoder.go`; the only consumer-side caller is
  `internal/adapters/inbound/kafka/node_consumer.go`.
- Every message carries the Kafka header
  `content-type: application/cloudevents+json; charset=UTF-8`. Message key =
  aggregate id, `kafkago.Hash{}` balancer (`internal/adapters/outbound/kafka/writer.go`).
- Attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per event at outbox
  append and stored in `outbox_events.event_id`, so a redelivery carries the same
  id); `source=/retailnetwork/offer-availability`; `type`; `subject` (the aggregate
  id: catalogItemId, listingId or feedId; never empty); `time` (the domain
  occurred-at, UTC); `datacontenttype=application/json`;
  `dataschema=urn:retailnetwork:offer-availability:events:<EventName>:v1`. No custom
  extension attributes without an ADR.
- `type` = `com.retailnetwork.commerce.offer-availability.<entity>.<EventName>` with
  entity `catalogitem`, `listing` or `inventoryfeed` (lowercase, no separators). A
  breaking payload change is a new `:v2` dataschema, never a mutation of v1;
  evolution is additive only.
- Payloads follow the wire conventions of contracts-delta: ids are strings,
  instants RFC 3339 UTC, quantities and dimensions integers, enums UPPER_SNAKE,
  optional fields omitted.
- Tests: a golden exact-JSON test per published type
  (`internal/adapters/outbound/kafka/encoder_test.go`, files in `testdata/`), the
  catalogue-vs-contract sensor (`TestEventCatalogueMatchesContract`), and the Kafka
  proofs against a real broker (`internal/adapters/outbound/outbox/relay_integration_test.go`
  and `cmd/api/e2e_integration_test.go`), all via testcontainers.

### Published types

All on `retailnetwork.offer-availability.events`; `subject` and Kafka key is the
aggregate id; `dataschema` is `urn:retailnetwork:offer-availability:events:<Event>:v1`.

| `type` | `subject` |
| --- | --- |
| `com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated` | catalogItemId |
| `com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemDiscontinued` | catalogItemId |
| `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | listingId |
| `com.retailnetwork.commerce.offer-availability.listing.ListingAvailabilityChanged` | listingId |
| `com.retailnetwork.commerce.offer-availability.listing.ListingSuppressed` | listingId |
| `com.retailnetwork.commerce.offer-availability.listing.ListingRestored` | listingId |
| `com.retailnetwork.commerce.offer-availability.inventoryfeed.InventoryFeedApplied` | feedId |

### Consumed types

From node-topology, topic `retailnetwork.node-topology.events`, dispatched on the
FULL `type` (`node_consumer.go`); every other type on the topic is ignored:

| `type` | read fields |
| --- | --- |
| `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | `nodeId` (+ CloudEvents `time`) |
| `com.retailnetwork.logistics.node-topology.node.NodeActivated` | `nodeId`, `activatedAt` |
| `com.retailnetwork.logistics.node-topology.node.NodeSuspended` | `nodeId`, `suspendedAt` |
| `com.retailnetwork.logistics.node-topology.node.NodeRetired` | `nodeId`, `retiredAt` |

Decoding is tolerant: only these fields are read. Each event is handled by
`usecases.ApplyNodeEvent` in ONE unit of work together with its inbox row
(`processed_events`, keyed by the CloudEvents id), so a redelivery changes nothing.

## Transactional outbox

A use case saves the aggregates and appends the encoded events in ONE Postgres
transaction (`ports.EventOutbox`, `internal/adapters/outbound/postgres/outbox_repository.go`).
The relay (`internal/adapters/outbound/outbox/relay.go`, always on, started by
`cmd/api/workers.go`) drains `outbox_events` in id order, at-least-once. The cap
`OUTBOX_MAX_ATTEMPTS` (default 20, 0 = unlimited) excludes a poison row; re-arm it
with `UPDATE outbox_events SET attempts = 0 WHERE id = ...` (ADR 0005). The first
write to a topic that does not exist yet can fail once with an unknown-topic
error: the relay retries it with the same id. The caller of `NewOutboxRelay` must
pass `WithMaxAttempts(...)` inline (`TestOutboxRelaysPassAttemptCap`).

## Consumer group id and dead letters

The group id comes from the mandatory env var `NODE_TOPOLOGY_CONSUMER_GROUP` (a boot
error naming it), never a string literal (`TestKafkaConsumerGroupNeverHardcodedInline`).
Offsets are committed after the event was handled. A transient failure is retried
five times, then the message goes to `retailnetwork.node-topology.events.dlq`; an
event that can never be applied goes there at once; a cancelled context is never
dead-lettered (`TestDeadLetterCallsGuardCancellation`). See
`.claude/rules/fleet/kafka-testing-and-consumers.md` and ADR 0005.
