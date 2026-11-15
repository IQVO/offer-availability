---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
description: "Process-level EventStorming boards for Offer and Availability: catalogue and listing set-up, feed application and node-driven and owner-driven suppression, with the hotspots the model left open and how the code settled them."
---

# EventStorming

Process-level boards (`flowchart LR`, or `TB` where LR would be too wide) in the ddd-crew
[EventStorming glossary & cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet) notation. Every sticky is
a command, aggregate, event, policy, read model or neighbour that exists in the code on `develop`. The hotspots are the gaps
the design left open: most are now settled by the code and the table at the end says how, the rest are still risks. The
domain-wide board is the aggregator's
[EventStorming Big Picture](https://iqvo.github.io/warehouse-docs/network/strategic-design/eventstorming-big-picture).

## Legend

| Sticky | Colour | Meaning |
| --- | --- | --- |
| Command | blue `#a5d8ff` | An intention, named as the use case or aggregate method |
| Aggregate | yellow `#ffec99` | The aggregate root that decides |
| Domain Event | orange `#ffc078` | A fact this context publishes (or a consumed one) |
| Policy | lilac `#d0bfff` | "Whenever X, then Y" |
| Read Model | green `#b2f2bb` | A local, event-fed view |
| External System | pink `#fcc2d7` | A neighbouring context or the fleet |
| Hotspot | red `#ff8787` | A remaining risk or an open question |
| Actor | pale orange `#fff4e6` | A person (used only for humans) |

## 1. Catalogue and listing set-up

```mermaid
flowchart LR
    MER["Network<br/>merchandising"]:::actor
    C1["CreateCatalogItem"]:::command
    C2["DiscontinueItem"]:::command
    C3["CreateListing"]:::command
    C4["DeclarePackLevel<br/>RetirePackLevel"]:::command
    A1["CatalogItem"]:::aggregate
    A2["Listing"]:::aggregate
    E1["CatalogItemCreated"]:::event
    E2["CatalogItemDiscontinued"]:::event
    E3["ListingCreated"]:::event
    E4["CatalogItemPackagingDeclared<br/>CatalogItemPackLevelRetired"]:::event
    E5["ListingSuppressed<br/>reason DISCONTINUED"]:::event
    X1["delivery-promise, pricing<br/>product-content, product-discovery<br/>shipping-and-tracking, shopping-cart"]:::external
    X2["delivery-promise, order-sourcing<br/>pricing, product-content<br/>product-discovery, shopping-cart"]:::external
    X3["delivery-promise, order-sourcing<br/>product-discovery<br/>vendor-connectivity"]:::external
    X4["seller-inbound<br/>vendor-connectivity"]:::external
    X5["delivery-promise<br/>order-sourcing<br/>product-discovery"]:::external
    H7["H7: no contract test<br/>against these consumers"]:::hotspot

    MER --> C1 --> A1 --> E1 --> X1
    MER --> C2 --> A1
    A1 --> E2 --> X2
    C2 --> A2 --> E5 --> X5
    MER --> C4 --> A1 --> E4 --> X4
    MER --> C3 --> A2 --> E3 --> X3
    E1 -.- H7

    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
    classDef actor fill:#fff4e6,stroke:#e8590c,color:#000
```

Source: `internal/application/usecases/catalog.go`, `internal/application/usecases/listings.go`,
`internal/domain/catalog/events.go`, `internal/domain/listing/events.go`.

Omitted: the refusals (`invalid-item-dimensions`, `invalid-gtin`, `gtin-already-assigned`, `duplicate-listing`; see
[Use cases](/docs/ddd/use-cases)) and the read queries, which change nothing. `CreateListing` also raises `ListingSuppressed`
when the node is not `ACTIVE`, the item is discontinued or the owner is suspended.

## 2. Inventory feed application

```mermaid
flowchart TB
    VC["vendor-connectivity<br/>POST /inventory-feeds"]:::external
    C1["ApplyInventoryFeed"]:::command
    A1["InventoryFeed"]:::aggregate
    R1["node_feed_marks<br/>last applied asOf, node lock"]:::readmodel
    P1["Full feed zeroing:<br/>omitted listings of the<br/>node get availableQty 0"]:::policy
    C2["ApplyAvailability<br/>per line, ZeroOut<br/>per omitted listing"]:::command
    A2["Listing"]:::aggregate
    E1["ListingAvailabilityChanged"]:::event
    E2["InventoryFeedApplied"]:::event
    X1["delivery-promise, order-sourcing<br/>product-discovery, seller-inbound"]:::external
    X2["node-performance"]:::external
    H2["H2: one atomic apply<br/>across many listings"]:::hotspot

    VC --> C1 --> A1
    R1 --> A1
    A1 --> P1 --> C2
    A1 --> C2 --> A2 --> E1 --> X1
    A1 --> E2 --> X2
    A1 -.- H2

    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/feeds.go`, `internal/domain/feed/feed.go`, `internal/domain/listing/listing.go`,
`internal/adapters/outbound/postgres/feed_repository.go` (`node_feed_marks`).

Omitted: the `FAILED` outcome (`listing-not-on-node`, `negative-availability`) and the `SUPERSEDED` outcome, which raise no event
but increment the feed counter; the stale-line skip, which raises no `ListingAvailabilityChanged`; the vendor's own Vendor API
submission upstream of `vendor-connectivity`.

## 3. Node-driven and owner-driven suppression

```mermaid
flowchart TB
    NT["node-topology"]:::external
    SI["seller-inbound"]:::external
    E0["NodeRegistered<br/>NodeActivated<br/>NodeSuspended<br/>NodeRetired"]:::event
    S0["SellerSuspended<br/>SellerReinstated"]:::event
    S1["InboundShipmentReceived"]:::event
    R1["node_status_view"]:::readmodel
    R2["seller_status_view"]:::readmodel
    P1["Policy SuppressOnNodeUnavailable:<br/>suspended or retired: suppress all LIVE,<br/>activated: restore those<br/>suppressed for NODE_NOT_SELLABLE"]:::policy
    P2["Policy on the seller's status:<br/>suspended: suppress the seller's LIVE<br/>listings for OWNER_SUSPENDED,<br/>reinstated: reconcile the ones<br/>suppressed for OWNER_SUSPENDED"]:::policy
    P3["Policy on received stock:<br/>create the seller's listing for each<br/>received item that has none on the node"]:::policy
    C1["Suppress"]:::command
    C2["Restore<br/>via Reconcile: the highest<br/>remaining cause wins"]:::command
    C4["Create owned listing<br/>availability 0, UNKNOWN"]:::command
    A1["Listing"]:::aggregate
    E1["ListingSuppressed"]:::event
    E2["ListingRestored"]:::event
    E3["ListingCreated"]:::event
    X1["delivery-promise<br/>order-sourcing<br/>product-discovery"]:::external
    X2["delivery-promise, order-sourcing<br/>product-discovery, vendor-connectivity"]:::external
    H8["H8: lag of the consumers is<br/>invisible to readiness"]:::hotspot

    NT --> E0 --> R1
    E0 --> P1
    P1 --> C1 --> A1
    P1 --> C2 --> A1
    R1 --> C2
    SI --> S0 --> R2
    S0 --> P2
    P2 --> C1
    P2 --> C2
    R2 --> C2
    SI --> S1 --> P3 --> C4 --> A1
    R1 --> C4
    R2 --> C4
    A1 --> E1 --> X1
    A1 --> E2 --> X1
    A1 --> E3 --> X2
    E0 -.- H8
    S0 -.- H8

    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/node_events.go`, `internal/application/usecases/seller_events.go`,
`internal/application/usecases/received_stock.go`, `internal/domain/nodestatus/nodestatus.go`,
`internal/domain/sellerstatus`, `internal/adapters/inbound/kafka/node_consumer.go`,
`internal/adapters/inbound/kafka/seller_consumer.go`, `internal/adapters/inbound/kafka/received_consumer.go`.

Omitted: `NodeRegistered` only inserts the node into the view as `PROPOSED` (a no-op for a known node) and triggers no command.
The restore steps run through `Listing.Reconcile`, which reads the node, the item and the owner from the local views and moves
the listing to the highest remaining cause; `Listing.Restore` itself is refused with `ErrNodeNotSellable` on a node that is not
`ACTIVE`, and a `DISCONTINUED` listing is never restored. A listing shows one reason, the highest active cause (`DISCONTINUED` >
`NODE_NOT_SELLABLE` > `OWNER_SUSPENDED`, ADR 0013), so `NodeActivated` on a node whose owner is suspended moves the listing to
`OWNER_SUSPENDED` instead of restoring it, and `SellerReinstated` hands a listing to `NODE_NOT_SELLABLE` when the node is still
not `ACTIVE`. An `InboundShipmentReceived` without a `received` list, or a line whose item cannot be resolved, creates nothing
and is counted (ADR 0014); a received line whose listing exists changes nothing.

## Hotspots

H1 to H6 were open questions of the design that the code has since settled; H2, H7 and H8 remain. The other inputs of board 3
(`SellerSuspended`, `SellerReinstated`, `InboundShipmentReceived`) were added by ADR 0013 and ADR 0014 and are not hotspots.

| Hotspot | Question | Status in the code |
| --- | --- | --- |
| H1 | A feed older than the node's last applied feed has no rule or state | Settled: state `SUPERSEDED`, `feed.Superseded`, the `node_feed_marks` table |
| H2 | A full feed writes every listing of a busy node in one atomic apply (up to 10000 lines) | **Open.** One transaction under the node's lock; no measured limit on transaction size or lock time |
| H3 | Starting state and availability of a new listing | Settled: `LIVE` only when the node is `ACTIVE` and the item is not discontinued, else `SUPPRESSED`; quantity 0, basis `UNKNOWN` |
| H4 | Effect of discontinuing on the item's listings | Settled: every listing is suppressed with reason `DISCONTINUED`, finally |
| H5 | What a zeroed listing carries | Settled: quantity 0, the basis kept, the feed's `asOf` and id stamped |
| H6 | Vocabulary of the suppression reason | Settled: `NODE_NOT_SELLABLE`, `DISCONTINUED` and, since ADR 0013, `OWNER_SUSPENDED`, one reason shown at a time by precedence (`listing.Reason`, `listing.Sellability.Cause`) |
| H7 | No contract test here pins the downstream consumers; their list was recomputed on 2026-11-13 from each sibling's inbound Kafka handlers | **Open.** [Integration](/docs/ecosystem/integration) |
| H8 | While the node-topology or seller-inbound consumer lags, listings keep their last state and `/readyz` stays green | **Open.** Readiness checks Postgres and a broker, not consumer lag ([Troubleshooting](/docs/operations/troubleshooting#kafka-the-outbox-and-the-consumer)) |
