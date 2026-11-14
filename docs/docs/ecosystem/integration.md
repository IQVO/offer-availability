---
id: integration
title: Integration
sidebar_label: Integration
sidebar_position: 1
---

# Integration

How `offer-availability` connects to the rest of the retail network. The service **calls no other service**: there is no
outbound REST, MCP or A2A client (`TestNoOutboundA2AClient` and the dependency rules keep it so), and `CLAUDE.md` states
"calls no sibling". Everything it knows about the outside world arrives as a Kafka event or as a REST call into it, and
everything it tells the outside world leaves as a Kafka event or a read response. The relationships come from the
aggregator's context map; this repository holds the contract of its own side only.

## At a glance

| Neighbour | Direction | Protocol | Contract | Pattern |
| --- | --- | --- | --- | --- |
| `node-topology` | upstream, consumed | Kafka `retailnetwork.node-topology.events` | `NodeRegistered`, `NodeActivated`, `NodeSuspended`, `NodeRetired` | Conformist |
| `vendor-connectivity` | caller into this service | REST `POST /inventory-feeds` (in-organization) | `apis/openapi.yaml` | Customer/Supplier (this context is the Supplier) |
| `vendor-connectivity` | downstream | Kafka `retailnetwork.offer-availability.events` | `ListingCreated`, pack-level events | Conformist |
| `delivery-promise` | downstream | Kafka (same topic) | catalogue and listing events | Conformist |
| `order-sourcing` | downstream | Kafka (same topic) | `CatalogItemDiscontinued` and listing events | Conformist |
| `shipping-and-tracking` | downstream | Kafka (same topic) | `CatalogItemCreated` | Conformist |
| `pricing`, `product-content`, `shopping-cart` | downstream | Kafka (same topic) | `CatalogItemCreated`, `CatalogItemDiscontinued` | Conformist |
| `product-discovery` | downstream | Kafka (same topic) | catalogue and listing events | Conformist |
| `node-performance` | downstream | Kafka (same topic) | `InventoryFeedApplied` | Conformist |
| `seller-inbound` | downstream | Kafka (same topic) | `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired`, `ListingAvailabilityChanged` | Conformist |
| `seller-inbound` | upstream, consumed | Kafka `retailnetwork.seller-inbound.events` | `SellerSuspended`, `SellerReinstated`, `InboundShipmentReceived` | Conformist |
| Network merchandising, storefronts | callers | REST | catalogue and listing routes | UI / Open Host Service |
| Agents | callers | MCP, A2A | [MCP tools](/docs/mcp/tools), [API overview](/docs/api-reference#a2a) | Open Host Service |

## Upstream: `node-topology` (Kafka, consumed)

- **Topic and group.** `retailnetwork.node-topology.events`, read by one consumer under `NODE_TOPOLOGY_CONSUMER_GROUP`
  (mandatory, never defaulted). The code sets no start offset, so kafka-go's default applies to a new group: it starts at
  the first offset of the topic.
- **Contract.** CloudEvents types `com.retailnetwork.logistics.node-topology.node.NodeRegistered`, `...NodeActivated`,
  `...NodeSuspended`, `...NodeRetired`. The service restates the contract in `apis/asyncapi.yaml` and never imports it; the
  payload copy is tolerant (it reads `nodeId` and `activatedAt`, `suspendedAt` or `retiredAt`, plus the CloudEvents
  `time` for `NodeRegistered`).
- **Effect.** The events fold into the local `node_status_view`, and listings of the node are suppressed or restored
  ([Use cases](/docs/ddd/use-cases#applynodeevent)). Nothing queries node-topology: a node this service has never heard
  of is not sellable.
- **Failure behaviour.** Delivery is at-least-once and the offset is committed after the handler succeeded; each event and
  its `processed_events` row commit together, so a redelivery is skipped. A transient failure is retried five times with
  200 ms to 5 s backoff and then the message goes to `retailnetwork.node-topology.events.dlq`; an event that can never be
  applied goes there at once; a message that is not a CloudEvent is skipped with a WARN. While the consumer is behind,
  listings keep their last state (no event means no change) and `/readyz` stays green: readiness checks Postgres and a
  broker, not consumer lag.
- **Ordering assumption.** node-topology keys its events by `nodeId`, so one node's events arrive in order; an observation
  older than the stored status is ignored anyway, and `RETIRED` is final.

## Upstream: `seller-inbound` (Kafka, consumed)

- **Topic and group.** `retailnetwork.seller-inbound.events`, read by a second consumer under
  `SELLER_INBOUND_CONSUMER_GROUP` (mandatory, never defaulted; ADR 0013).
- **Contract.** CloudEvents types `com.retailnetwork.partner.seller-inbound.seller.SellerSuspended` (reads `sellerId`,
  `suspendedAt`), `...seller.SellerReinstated` (reads `sellerId`, `reinstatedAt`) and
  `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived` (reads `sellerId`, `nodeId`,
  `received[].{catalogItemId?, gtin, qtyEaches}`; ADR 0014), restated in `apis/asyncapi.yaml`. Every other type on the
  topic is acknowledged and counted in `availability.seller_events.ignored` (`reason=type_not_consumed`).
- **Effect.** The events fold into the local `seller_status_view` (an unknown seller is `ACTIVE`, an older event is
  ignored); the seller's listings are suppressed with `OWNER_SUSPENDED` or restored. Nothing queries seller-inbound.
- **Received stock (G51, ADR 0014).** Each received item without a listing of (item, node, seller) gets one at 0 /
  `UNKNOWN` (suppressed when the node is not sellable, the item discontinued or the seller suspended); an existing listing
  is untouched and availability still comes only from the vendor feed. A line resolves by `catalogItemId`, else by `gtin`
  through the local GTIN/packaging index; an unresolvable line is a WARN counted `gtin_unresolved` (or `item_unknown`), and
  an event published before the producer carried `received` creates nothing (`received_unknown`).
- **Failure behaviour.** As for node-topology, with dead letters on `retailnetwork.seller-inbound.events.dlq`.

## Caller: `vendor-connectivity` (REST `POST /inventory-feeds`)

`vendor-connectivity` owns the vendor-facing protocol (vendor authentication, asynchronous transactions, rate limits, the
`202` answer) and calls `POST /inventory-feeds` in-organization to apply each feed, then `GET /inventory-feeds/{feedId}` if
it needs the outcome. This service does not know vendors and does not check that a vendor acts on its own nodes; that is
`vendor-connectivity`'s duty. Each call must carry an `Idempotency-Key`; a retry with the same key replays the first
answer. A feed that ends `FAILED` is answered `422` with a problem and the same `feedId` may be re-sent with a new key.
There is no call in the other direction.

## Downstream consumers (Kafka)

All consumers read `retailnetwork.offer-availability.events` ([Event catalogue](/docs/api-reference/events)). They keep
their own read models (`delivery-promise` and `order-sourcing` an `AvailabilityView`, `vendor-connectivity` a `ListingView`
and `CatalogPackagingView`, `shipping-and-tracking` a `CatalogDimensionsView`, `seller-inbound` a `CatalogPackagingView` and
a `DemandHeatView`, `pricing` a `CatalogItemView`, `shopping-cart` a `PurchasabilityView`, `node-performance` its feed-freshness
measure); none calls this service on a hot path. This repository contains **no consumer code for its own events and no
consumer-driven contract test**: the consumer list (which type each sibling handles, see
[Domain events](/docs/ddd/domain-events)) was recomputed on 2026-11-13 from each sibling's inbound Kafka handlers at
`origin/develop`, and the only check here is `TestEventCatalogueMatchesContract` (the types in `apis/asyncapi.yaml` appear in
ADR 0004). If a consumer is absent from a deployment, nothing in this service notices: the relay publishes regardless.

## Callers over REST, MCP and A2A

Network merchandising (a human operator) uses `POST /catalog-items`, `POST /catalog-items/{catalogItemId}/discontinuations`,
the packaging routes and `POST /listings`. A storefront browses `GET /catalog` and `GET /catalog/{catalogItemId}` (Open Host
Service). Agents read through MCP and A2A. All are unauthenticated and go through Kong at `/api/offer-availability`
(`/api/mcp/offer-availability`, `/api/a2a/offer-availability` for the optional components). The console remote in `web/`
calls only this service's REST reads ([ADR 0008](/docs/adr/0008-console-remote)).

## Absent edges

| Edge | Status |
| --- | --- |
| `network-fulfillment` (the fleet) | **Separate Ways.** No channel. Its inventory reaches this context only as a feed applied by `vendor-connectivity` |
| `customer-ordering`, `linehaul-transportation`, `last-mile-delivery` | No relationship |
| Any outbound HTTP, MCP or A2A call | None. No circuit breaker, retry or timeout configuration exists for a sibling |
| An analytics topic or read side | None; the model declares no analytics-only event |
| A Schema Registry or Avro | None; payloads are JSON CloudEvents with a `dataschema` URN |
| Authentication on any edge | None, fleet-wide |
