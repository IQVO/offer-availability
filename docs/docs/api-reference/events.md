---
id: events
title: Event catalogue
sidebar_label: Events
sidebar_position: 3
---

# Event catalogue

`offer-availability` publishes **nine** CloudEvents 1.0 types on one topic and consumes four. The contract is
`apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0); the encoder is `internal/adapters/outbound/kafka/encoder.go`; the envelope is built
only in `internal/adapters/kafka/cloudevents`. Every field of every payload is on [Domain events](/docs/ddd/domain-events).
The envelope and type catalogue are decided in [ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue).

## Topic and envelope

| Property | Value |
| --- | --- |
| Produced topic | `retailnetwork.offer-availability.events` (the only one; there is no analytics topic) |
| Consumed topic | `retailnetwork.node-topology.events`, dead letters to `retailnetwork.node-topology.events.dlq` |
| Broker | The fleet's single Kafka broker (`localhost:9092` from the host in the kind cluster) |
| Kafka key | The aggregate root id (the CloudEvents `subject`): `catalogItemId`, `listingId` or `feedId` |
| Content type header | `application/cloudevents+json; charset=UTF-8` (structured mode) |
| `specversion` | `1.0` |
| `source` | `/retailnetwork/offer-availability` |
| `type` | `com.retailnetwork.commerce.offer-availability.<entity>.<EventName>` with `<entity>` one of `catalogitem`, `listing`, `inventoryfeed` |
| `id` | A random UUID minted once and persisted with the outbox row, so a relay retry republishes the same id |
| `subject` | The aggregate root id |
| `time` | RFC 3339 UTC instant of the event |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:offer-availability:events:<EventName>:v1` |
| Delivery | At-least-once through the transactional outbox ([Runbook](/docs/operations/runbook#the-outbox-in-operation)); consumers deduplicate on `id` |
| Tracing | The active span context is stamped as `traceparent`/`tracestate` Kafka headers by the relay |

## Published events

| Event (full type suffix) | Raised by | Payload (`data`) | Known consumers |
| --- | --- | --- | --- |
| `catalogitem.CatalogItemCreated` | `CreateCatalogItem` | `catalogItemId`, `title`, `lengthMm`, `widthMm`, `heightMm`, `weightG`, `hazmat`, `fragile`, optional `gtin` | `delivery-promise`, `shipping-and-tracking` |
| `catalogitem.CatalogItemPackagingDeclared` | `DeclarePackLevel` | `catalogItemId`, `level`, `gtin`, optional `childGtin`, `unitsPerPack`, `declaredAt` | `seller-inbound`, `vendor-connectivity` |
| `catalogitem.CatalogItemPackLevelRetired` | `DeclarePackLevel` (replacement), `RetirePackLevel` | `catalogItemId`, `level`, `gtin`, `retiredAt` | `seller-inbound`, `vendor-connectivity` |
| `catalogitem.CatalogItemDiscontinued` | `DiscontinueItem` | `catalogItemId` | `delivery-promise`, `order-sourcing` |
| `listing.ListingCreated` | `CreateListing` | `listingId`, `catalogItemId`, `nodeId`, optional `ownerRef` | `delivery-promise`, `order-sourcing`, `vendor-connectivity` |
| `listing.ListingAvailabilityChanged` | `ApplyInventoryFeed` | `listingId`, `catalogItemId`, `nodeId`, optional `ownerRef`, `availableQty`, `basis`, `asOf`, `feedId` | `delivery-promise`, `order-sourcing` |
| `listing.ListingSuppressed` | `CreateListing`, `DiscontinueItem`, `ApplyNodeEvent` | `listingId`, `nodeId`, `reason` (`NODE_NOT_SELLABLE` or `DISCONTINUED`) | `delivery-promise`, `order-sourcing` |
| `listing.ListingRestored` | `ApplyNodeEvent` | `listingId`, `nodeId` | `delivery-promise`, `order-sourcing` |
| `inventoryfeed.InventoryFeedApplied` | `ApplyInventoryFeed` | `feedId`, `nodeId`, `fullUpdate`, `lineCount`, `appliedAt` | `node-performance` |

The consumer column is the downstream edge recorded in the aggregator's context map; this repository contains no consumer
code and no contract test against one ([Integration](/docs/ecosystem/integration)).

### Ordering inside one request

One use case publishes its events in one transaction, in this order: `DiscontinueItem` -> `CatalogItemDiscontinued`, then
`ListingSuppressed` per listing by id; `DeclarePackLevel` -> `CatalogItemPackLevelRetired` (when replacing), then
`CatalogItemPackagingDeclared`; `CreateListing` -> `ListingCreated`, then `ListingSuppressed` if it starts suppressed;
`ApplyInventoryFeed` -> one `ListingAvailabilityChanged` per changed listing, then `InventoryFeedApplied`. A feed that ends
`SUPERSEDED` or `FAILED` publishes nothing. Rows reach Kafka in `id` order, but a row parked after `OUTBOX_MAX_ATTEMPTS`
failures lets later events of the same aggregate overtake it.

## Consumed events

`internal/adapters/inbound/kafka/node_consumer.go` dispatches on the full `type`; the payload copy is tolerant (only the
fields below are read).

| Type (`com.retailnetwork.logistics.node-topology.node.`) | Fields read | Effect |
| --- | --- | --- |
| `NodeRegistered` | `nodeId`, CloudEvents `time` | Creates the node view as `PROPOSED` if unknown; otherwise no-op |
| `NodeActivated` | `nodeId`, `activatedAt` | Node `ACTIVE`; restores listings suppressed for `NODE_NOT_SELLABLE` |
| `NodeSuspended` | `nodeId`, `suspendedAt` | Node `SUSPENDED`; suppresses its `LIVE` listings |
| `NodeRetired` | `nodeId`, `retiredAt` | Node `RETIRED` (final); suppresses its `LIVE` listings |

An event without a `nodeId` or its instant is dead-lettered at once; a message that is not a CloudEvents document, or a
type this service does not consume, is skipped. See [Troubleshooting](/docs/operations/troubleshooting#kafka-the-outbox-and-the-consumer).

## Example

A real message captured from a run ([Quickstart](/docs/overview/quickstart#6-see-the-published-events)):

```json
{"specversion":"1.0","id":"0abbfe0e-6dbc-4e0c-a308-b57866ee7d78","source":"/retailnetwork/offer-availability","type":"com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated","subject":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","datacontenttype":"application/json","dataschema":"urn:retailnetwork:offer-availability:events:CatalogItemCreated:v1","time":"2026-10-31T23:39:24.401377Z","data":{"catalogItemId":"item-fe9bdd3a-66fe-41b7-a7c5-c9663873ee1e","title":"Cotton tote bag","lengthMm":400,"widthMm":300,"heightMm":50,"weightG":180,"hazmat":false,"fragile":false,"gtin":"06141410000120"}}
```
