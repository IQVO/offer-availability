# ADR 0003: consumed contracts, the local node status read model and the relationships of offer-availability

## Status

Accepted (2026-10-23). Design source: retail-network-docs `model/model.yaml`
(`inbound`, `consumes`, `read_models`, `policies`, `sync_calls`) and
`contracts-delta.md`.

## Context

offer-availability consumes events from one context, node-topology, calls no
sibling synchronously (`sync_calls` is empty) and is called by two in-organization
clients. This ADR records the consumed contract, the local read model, the
relationships and the delivery guarantees that follow.

## Decision

### Relationships

| Counterpart | Direction | Channel | Relationship |
| --- | --- | --- | --- |
| node-topology | inbound events | Kafka `retailnetwork.node-topology.events` | Conformist: offer-availability adopts the producer's model of node status |
| vendor-connectivity | inbound command | REST `POST /inventory-feeds` | Customer/Supplier: vendor-connectivity supplies feeds, owns the `Submission`, the `202` to the vendor, rate limits and the vendor-owns-node check |
| network merchandising | inbound commands | REST `/catalog-items`, `/listings` | UI |
| shopper / storefront | inbound queries | REST `GET /catalog`, `GET /catalog/{catalogItemId}` | Open Host Service |
| delivery-promise, order-sourcing, vendor-connectivity, shipping-and-tracking, node-performance | outbound events | Kafka `retailnetwork.offer-availability.events` | published language of ADR 0004; they are the Conformists |

`GET /listings` and `GET /inventory-feeds/{feedId}` have no caller named by the
model; they are plain reads for merchandising and for vendor-connectivity's
diagnostics.

### Consumed events

All four come from node-topology on `retailnetwork.node-topology.events` (type
prefix `com.retailnetwork.logistics.node-topology.node.`), keyed by `nodeId`.
node-topology's own `apis/asyncapi.yaml` (`origin/feature/service`) is the
producer contract; offer-availability vendors a tolerant copy of what it reads in
its own `apis/asyncapi.yaml` (no cross-repo import):

| Type | Fields read | Effect |
| --- | --- | --- |
| `...node.NodeRegistered` | `nodeId` (+ CloudEvents `time`) | NodeStatusView row `PROPOSED` if unknown, else no-op |
| `...node.NodeActivated` | `nodeId`, `activatedAt` | status `ACTIVE`, then RestoreListing for listings suppressed for `NODE_NOT_SELLABLE` |
| `...node.NodeSuspended` | `nodeId`, `suspendedAt` (`reason` is not stored) | status `SUSPENDED`, then SuppressListing for every `LIVE` listing |
| `...node.NodeRetired` | `nodeId`, `retiredAt` | status `RETIRED`, then SuppressListing for every `LIVE` listing |

Every other field and every other type on the topic is ignored (decoding is
tolerant, unknown types are acknowledged). The consumer dispatches on the full
`type` string, never on a suffix. A record that cannot be decoded, or whose
`nodeId` is not a valid id, is sent to the dead-letter topic
`retailnetwork.offer-availability.events.dlq` with the failure reason in headers
and its offset is committed; it does not block the partition.

### Local read model: NodeStatusView

Table `node_status_view(node_id, status, status_at, version)`; written only by the
consumer; read by `CreateListing` (is the node sellable) and by nothing else. It is
an event-fed copy: the service never calls node-topology (hard rule 4). Ordering
and unknown-node rules are A13 of ADR 0002.

### Delivery guarantees

- **Inbox.** The CloudEvents `id` of each consumed message is inserted into
  `processed_events(event_id, event_type, processed_at)` in the same transaction as
  the view update, the listing changes and their outbox rows. A redelivered id is
  skipped (the transaction is a no-op that commits only if nothing else is new).
- **Consumer group.** Mandatory: `NODE_STATUS_CONSUMER_GROUP` (chart value; proposed
  `offer-availability-node-status`). A missing value is a boot error naming the
  variable. Offsets are committed after the transaction commits.
- **Outbox.** Every published event is appended to `outbox_events` in the
  transaction of the aggregate change; an always-on relay drains it to Kafka with
  an attempt cap (ADR 0004 for the envelope).
- **No synchronous calls.** `sync_calls` is empty. The shopper path reads the local
  database only.

### Analytics

None. The model has no analytics-only event for this context.

## Consequences

- offer-availability can answer every request while node-topology is down; a
  node's status can lag its true status by the consumer lag, which A5 and A13
  tolerate.
- A new consumed type needs a message in `apis/asyncapi.yaml`, a row here and a
  dispatch entry in `internal/adapters/inbound/kafka`.
- The per-node policy runs in one transaction, so a node with a very large number
  of listings produces a correspondingly large transaction (bulk saves keep it to a
  few statements).
