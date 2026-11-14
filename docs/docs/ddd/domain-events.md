---
id: domain-events
title: Domain Events
sidebar_label: Domain Events
description: "Every event Offer and Availability publishes and consumes, with the full CloudEvents type, topic, key, data fields, producing use case and the consumer idempotency rules."
---

# Domain Events

Every event this context publishes and consumes. The contract is `apis/asyncapi.yaml`; the payloads are the structs in
`internal/adapters/outbound/kafka/encoder.go` (camelCase JSON, timestamps RFC 3339 UTC); the envelope is built only in
`internal/adapters/kafka/cloudevents`. The compact catalogue with the operations view is [Event catalogue](/docs/api-reference/events);
the standard is the aggregator's
[CloudEvents event standard](https://iqvo.github.io/warehouse-docs/network/strategic-design/event-standard-cloudevents) and the
decision here is [ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue).

## Envelope

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` (structured JSON, Kafka `content-type` header `application/cloudevents+json; charset=UTF-8`) |
| `id` | A UUID minted once when the event is encoded for `outbox_events`; a relay retry republishes the same `id` |
| `source` | `/retailnetwork/offer-availability` |
| `type` | `com.retailnetwork.commerce.offer-availability.<entity>.<EventName>`; `<entity>` is `catalogitem`, `listing` or `inventoryfeed` |
| `subject` | The aggregate root id: `catalogItemId`, `listingId` or `feedId` |
| `time` | The instant of the aggregate change, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:offer-availability:events:<EventName>:v1` (every payload is at `v1`) |
| Topic | `retailnetwork.offer-availability.events`; there is no analytics topic |
| Kafka key | The same aggregate root id as `subject`, so one aggregate's events share a partition |

Every event leaves through a transactional outbox written in the same transaction as the aggregate change; delivery is
at-least-once.

Source: `internal/adapters/kafka/cloudevents/cloudevents.go` (`Type`, `DataSchema`, `Source`), `internal/adapters/outbound/kafka/encoder.go`.

## Published

| Event | Full `type` | Subject / key | Raised by (use case) | Consumers (their inbound Kafka handlers) |
| --- | --- | --- | --- | --- |
| `CatalogItemCreated` | `com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated` | `catalogItemId` | `CreateCatalogItem` | `delivery-promise`, `pricing`, `product-content`, `product-discovery`, `shipping-and-tracking`, `shopping-cart` |
| `CatalogItemDiscontinued` | `...catalogitem.CatalogItemDiscontinued` | `catalogItemId` | `DiscontinueItem` | `delivery-promise`, `order-sourcing`, `pricing`, `product-content`, `product-discovery`, `shopping-cart` |
| `CatalogItemPackagingDeclared` | `...catalogitem.CatalogItemPackagingDeclared` | `catalogItemId` | `DeclarePackLevel` | `seller-inbound`, `vendor-connectivity` |
| `CatalogItemPackLevelRetired` | `...catalogitem.CatalogItemPackLevelRetired` | `catalogItemId` | `RetirePackLevel`, `DeclarePackLevel` (replacing a level) | `seller-inbound`, `vendor-connectivity` |
| `ListingCreated` | `...listing.ListingCreated` | `listingId` | `CreateListing`, `ApplyReceivedStock` | `delivery-promise`, `order-sourcing`, `product-discovery`, `vendor-connectivity` |
| `ListingAvailabilityChanged` | `...listing.ListingAvailabilityChanged` | `listingId` | `ApplyInventoryFeed` (per changed listing, zeroed ones included) | `delivery-promise`, `order-sourcing`, `product-discovery`, `seller-inbound` |
| `ListingSuppressed` | `...listing.ListingSuppressed` | `listingId` | `CreateListing` and `ApplyReceivedStock` (start suppressed), `DiscontinueItem`, `ApplyNodeEvent`, `ApplySellerEvent` | `delivery-promise`, `order-sourcing`, `product-discovery` |
| `ListingRestored` | `...listing.ListingRestored` | `listingId` | `ApplyNodeEvent`, `ApplySellerEvent` | `delivery-promise`, `order-sourcing`, `product-discovery` |
| `InventoryFeedApplied` | `...inventoryfeed.InventoryFeedApplied` | `feedId` | `ApplyInventoryFeed` (feed reaches `APPLIED`) | `node-performance` |

Source: `internal/domain/{catalog,listing,feed}/events.go` (types), `internal/application/usecases` (producers),
`apis/asyncapi.yaml`. This repository holds no consumer code for these events. The consumer column was recomputed on
2026-11-13 from each sibling's inbound Kafka handlers at `origin/develop` (`delivery-promise` `dispatch.go`, `order-sourcing`
`handlers.go`, `pricing`, `product-content` and `product-discovery` `offer_availability.go`, `shipping-and-tracking` and
`shopping-cart` `handlers.go`, `seller-inbound` `consumers.go`, `vendor-connectivity` `consumers.go` and
`inbound_consumers.go`, `node-performance` `handlers.go`); no contract test here pins it.

### Data fields

**`CatalogItemCreated`**

| Field | Type | Meaning |
| --- | --- | --- |
| `catalogItemId` | string | The item's id (also `subject`), `item-<uuid>`. |
| `title` | string | Display title. |
| `lengthMm`, `widthMm`, `heightMm` | integer | Dimensions in millimetres, at least 1. |
| `weightG` | integer | Weight in grams, at least 1. |
| `hazmat`, `fragile` | boolean | Handling flags. |
| `gtin` | string, omitted when absent | The each-level GTIN, 14 digits. |

**`CatalogItemDiscontinued`**: `catalogItemId`.

**`CatalogItemPackagingDeclared`**

| Field | Type | Meaning |
| --- | --- | --- |
| `catalogItemId` | string | The item. |
| `level` | string | `EACH`, `INNER`, `CASE` or `PALLET`: the one level this event declares. |
| `gtin` | string | The level's GTIN, 14 digits. |
| `childGtin` | string, omitted for `EACH` | The next lower level's GTIN. |
| `unitsPerPack` | integer | Direct factor to eaches: 1 for `EACH`, at least 2 above. |
| `declaredAt` | string (date-time) | When the level was declared. A re-declaration that only changes dimensions or weight raises this event again with the original GTIN and quantity. |

**`CatalogItemPackLevelRetired`**: `catalogItemId`, `level`, `gtin` and `retiredAt` (date-time). The level is retired (inactive),
never deleted, so shipments in flight still validate.

**`ListingCreated`**

| Field | Type | Meaning |
| --- | --- | --- |
| `listingId` | string | The listing's id (also `subject`). |
| `catalogItemId` | string | The item offered. |
| `nodeId` | string | The node it is offered from. |
| `ownerRef` | string, omitted when operator-owned | The seller of record of the stock. |

**`ListingAvailabilityChanged`**

| Field | Type | Meaning |
| --- | --- | --- |
| `listingId` | string | The listing (also `subject`). |
| `catalogItemId`, `nodeId` | string | The item and node, so consumers need no join. |
| `ownerRef` | string, omitted when operator-owned | The seller of record. |
| `availableQty` | integer | The node's claimed quantity, 0 to 2147483647, in eaches. |
| `basis` | string | `PHYSICAL`, `THROUGHPUT_CONSTRAINED` or `UNKNOWN`, as the vendor declared it. |
| `asOf` | string (date-time) | The instant the number is true for; strictly increasing per listing. |
| `feedId` | string | The feed that carried the number. |

**`ListingSuppressed`**: `listingId`, `nodeId` and `reason`, which is `NODE_NOT_SELLABLE`, `DISCONTINUED` or `OWNER_SUSPENDED`
(the highest active cause, ADR 0013).

**`ListingRestored`**: `listingId` and `nodeId`.

**`InventoryFeedApplied`**

| Field | Type | Meaning |
| --- | --- | --- |
| `feedId` | string | The feed (also `subject`). |
| `nodeId` | string | The node the feed is for. |
| `fullUpdate` | boolean | True for a full feed. |
| `lineCount` | integer | Lines in the feed as received. |
| `appliedAt` | string (date-time) | When the feed reached `APPLIED`. |

### Example envelope

A real message captured from a run ([Quickstart](/docs/overview/quickstart#6-see-the-published-events)):

```json
{
  "specversion": "1.0",
  "id": "0abbfe0e-6dbc-4e0c-a308-b57866ee7d78",
  "source": "/retailnetwork/offer-availability",
  "type": "com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated",
  "subject": "item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e",
  "datacontenttype": "application/json",
  "dataschema": "urn:retailnetwork:offer-availability:events:CatalogItemCreated:v1",
  "time": "2026-10-31T23:39:24.401377Z",
  "data": {
    "catalogItemId": "item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e",
    "title": "Cotton tote bag",
    "lengthMm": 400,
    "widthMm": 300,
    "heightMm": 50,
    "weightG": 180,
    "hazmat": false,
    "fragile": false,
    "gtin": "06141410000120"
  }
}
```

### Ordering inside one request

Events of one use case are written in one transaction in a fixed order: `DiscontinueItem` raises `CatalogItemDiscontinued`, then
`ListingSuppressed` by listing id; `DeclarePackLevel` raises `CatalogItemPackLevelRetired` (when replacing), then
`CatalogItemPackagingDeclared`; `CreateListing` raises `ListingCreated`, then `ListingSuppressed` if it starts suppressed;
`ApplyInventoryFeed` raises one `ListingAvailabilityChanged` per changed listing, then `InventoryFeedApplied`. A feed that ends
`SUPERSEDED` or `FAILED` publishes nothing.

## Consumed

All four come from `node-topology` on `retailnetwork.node-topology.events` (tier `logistics`, entity `node`), read by one
consumer under the group named by `NODE_TOPOLOGY_CONSUMER_GROUP`. Dead letters go to `retailnetwork.node-topology.events.dlq`.

| Event | Full `type` | Fields used | What this context does |
| --- | --- | --- | --- |
| `NodeRegistered` | `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | `nodeId`, CloudEvents `time` | Inserts the node into `node_status_view` as `PROPOSED` when unknown, otherwise nothing. |
| `NodeActivated` | `...node.NodeActivated` | `nodeId`, `activatedAt` | Sets the node `ACTIVE`; then `Restore` on every listing of the node suppressed for `NODE_NOT_SELLABLE`. |
| `NodeSuspended` | `...node.NodeSuspended` | `nodeId`, `suspendedAt` | Sets the node `SUSPENDED`; then `Suppress` on every `LIVE` listing of the node. |
| `NodeRetired` | `...node.NodeRetired` | `nodeId`, `retiredAt` | Sets the node `RETIRED` (final); then `Suppress` on every `LIVE` listing. |

The payload copy is tolerant: only the fields above are read, the rest of the producer's payload is ignored. Source:
`internal/adapters/inbound/kafka/node_consumer.go`, `internal/application/usecases/node_events.go`.

Three more come from `seller-inbound` on `retailnetwork.seller-inbound.events` (tier `partner`, entities `seller` and
`inboundshipment`), read by a second consumer under `SELLER_INBOUND_CONSUMER_GROUP` (ADR 0013, ADR 0014). Every other type
on that topic is counted in `availability.seller_events.ignored` (`reason=type_not_consumed`) and skipped; dead letters go
to `retailnetwork.seller-inbound.events.dlq`.

| Event | Full `type` | Fields used | What this context does |
| --- | --- | --- | --- |
| `SellerSuspended` | `com.retailnetwork.partner.seller-inbound.seller.SellerSuspended` | `sellerId`, `suspendedAt` | Sets the seller `SUSPENDED` in `seller_status_view`; then `Suppress(OWNER_SUSPENDED)` on every `LIVE` listing the seller owns (a listing already suppressed keeps its higher reason). |
| `SellerReinstated` | `com.retailnetwork.partner.seller-inbound.seller.SellerReinstated` | `sellerId`, `reinstatedAt` | Sets the seller `ACTIVE`; then `Reconcile` on every listing of the seller suppressed for `OWNER_SUSPENDED`: restored, or handed to its node's `NODE_NOT_SELLABLE`. |
| `InboundShipmentReceived` | `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived` | `sellerId`, `nodeId`, `received[].{catalogItemId?, gtin, qtyEaches}` | For each line with eaches > 0, resolves the item (`catalogItemId`, else the GTIN/packaging index) and creates the seller's listing on the node when none exists (`ListingCreated` at 0 / `UNKNOWN`, `ListingSuppressed` when a cause holds). Unresolvable lines and an absent `received` are counted, never errors (ADR 0014). |

A listing shows one reason, the highest active cause: `DISCONTINUED` (final) > `NODE_NOT_SELLABLE` > `OWNER_SUSPENDED`.
`NodeActivated` restores a listing only when its owner is not suspended; otherwise the reason becomes `OWNER_SUSPENDED`.
Source: `internal/adapters/inbound/kafka/seller_consumer.go`, `internal/adapters/inbound/kafka/received_consumer.go`,
`internal/application/usecases/seller_events.go`, `internal/application/usecases/received_stock.go`.

### Idempotency and ordering

- **Inbox.** Each consumed CloudEvents `id` is recorded in `processed_events` in the same transaction as the view update, the
  listing changes and their outbox rows. A redelivered `id` is skipped. Inbox rows are purged after 30 days.
- **Dispatch on the full type string**, never on a suffix. A message that is not a CloudEvents 1.0 document, or a type this
  service does not consume, is skipped with a warning; an event missing its `nodeId` or instant is dead-lettered at once.
- **Order per node.** `node-topology` keys by `nodeId`, so one node's events arrive in order on one partition; an observation older
  than the stored status is ignored anyway, and nothing but `RETIRED` is accepted after `RETIRED`.
- **Replays are safe.** Suppressing a `SUPPRESSED` listing or restoring a `LIVE` one is skipped without an event, and a
  `DISCONTINUED` suppression is never restored.
