---
id: domain-message-flow
title: Domain Message Flow
sidebar_label: Domain Message Flow
description: "Key Offer and Availability scenarios as ddd-crew domain message flows: a feed applied, a feed refused or superseded, a node suspended and restored, a catalogue item listed and discontinued."
---

# Domain Message Flow

Following ddd-crew [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling). Five scenarios.
Participants are contexts and actors, abbreviated by context code: NF = `network-fulfillment` (the fleet, vendor V-WS), VC =
`vendor-connectivity`, OA = `offer-availability`, NT = `node-topology`, DP = `delivery-promise`, OS = `order-sourcing`, ST =
`shipping-and-tracking`, NP = `node-performance`. Every arrow is a message prefixed `cmd:` (command), `qry:` (query) or
`evt:` (event). Event arrows are written `<entity>.<EventName>`; the full type is
`com.retailnetwork.<tier>.<producer>.<entity>.<EventName>` (see [Domain events](/docs/ddd/domain-events)). Path parameters are
written without braces. The OA side of every flow is checked against the code; the VC, NT, DP, OS, ST and NP sides are the
aggregator's description of those contexts, not code in this repository. The domain-wide flows are on the aggregator's
[Domain Message Flows](https://iqvo.github.io/warehouse-docs/network/strategic-design/domain-message-flows).

## 1. A full inventory feed is applied (happy path)

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant NF as NF vendor V-WS
    participant VC
    participant OA
    participant NP
    NF->>VC: cmd: POST /vendor/inventory<br/>SubmitInventory
    VC-->>NF: cmd: reply 202 transactionId
    Note over VC: ProcessSubmission<br/>after notBefore
    VC->>OA: cmd: POST /inventory-feeds<br/>ApplyInventoryFeed<br/>fullUpdate true
    Note over OA: one transaction:<br/>feed RECEIVED, lines applied,<br/>omitted listings zeroed,<br/>feed APPLIED
    OA-->>VC: cmd: reply 201 APPLIED
    OA->>NP: evt: inventoryfeed.<br/>InventoryFeedApplied
    VC->>NP: evt: submission.<br/>SubmissionProcessed SUCCESS
    NF->>VC: qry: GET /vendor/transactions/<br/>transactionId GetTransaction
    VC-->>NF: qry: reply SUCCESS
```

Source: `internal/application/usecases/feeds.go` (`ApplyInventoryFeed`), `internal/domain/feed/feed.go` (`Apply`,
`MarkApplied`), `internal/adapters/inbound/http/handlers.go`; the vendor-facing messages are `vendor-connectivity`'s.

The same apply, seen from the availability consumers:

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant VC
    participant OA
    participant DP
    participant OS
    VC->>OA: cmd: POST /inventory-feeds<br/>ApplyInventoryFeed
    Note over OA: per changed listing,<br/>including zeroed ones
    OA->>DP: evt: listing.<br/>ListingAvailabilityChanged
    OA->>OS: evt: listing.<br/>ListingAvailabilityChanged
```

Source: `internal/domain/feed/feed.go` (`Apply`, `zeroOmitted`), `internal/domain/listing/events.go`.

Omitted: `vendor-connectivity`'s ownership check and rate limiting; the outbox relay between commit and Kafka, so the event
arrows can be seen after the reply; the lines that were stale, which raise no event. One `ListingAvailabilityChanged` goes out
per changed listing; the diagram shows one arrow per consumer. A partial feed is the same flow without zeroing.

## 2. A feed is refused, superseded or changes nothing (failure path)

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant NF as NF vendor V-WS
    participant VC
    participant OA
    participant NP
    NF->>VC: cmd: POST /vendor/inventory<br/>SubmitInventory
    VC-->>NF: cmd: reply 202 transactionId
    VC->>OA: cmd: POST /inventory-feeds<br/>ApplyInventoryFeed
    alt the request is malformed
        Note over OA: shared.ErrValidation<br/>nothing recorded
        OA-->>VC: cmd: reply 422<br/>validation-failed
    else a line names a listing of another node
        Note over OA: ErrListingNotOnNode<br/>feed FAILED, row kept
        OA-->>VC: cmd: reply 422<br/>listing-not-on-node
        VC->>NP: evt: submission.<br/>SubmissionProcessed FAILURE
    else a line has a negative quantity
        Note over OA: ErrNegativeAvailability<br/>feed FAILED, row kept
        OA-->>VC: cmd: reply 422<br/>negative-availability
        VC->>NP: evt: submission.<br/>SubmissionProcessed FAILURE
    else the feed id was already applied
        Note over OA: ErrFeedAlreadyApplied<br/>nothing changes
        OA-->>VC: cmd: reply 409<br/>feed-already-applied
    else the feed is older than the node's last applied feed
        Note over OA: feed SUPERSEDED<br/>nothing else changes
        OA-->>VC: cmd: reply 201 SUPERSEDED
    else every line is stale for its listing
        Note over OA: ErrStaleAvailability<br/>per line, lines skipped,<br/>feed APPLIED
        OA-->>VC: cmd: reply 201 APPLIED
        OA->>NP: evt: inventoryfeed.<br/>InventoryFeedApplied
    end
    NF->>VC: qry: GET /vendor/transactions/<br/>transactionId GetTransaction
    VC-->>NF: qry: reply SUCCESS<br/>or FAILURE with errors
```

Source: `internal/application/usecases/feeds.go`, `internal/domain/feed/feed.go` (`NewSubmission`, `Superseded`, `MarkFailed`,
`MarkSuperseded`), `internal/adapters/inbound/http/problems.go` (problem slugs and statuses).

Omitted: what `vendor-connectivity` does with a `409` on a feed it already applied, and with a `SUPERSEDED` answer. A `FAILED`
feed id may be sent again; an `APPLIED` or `SUPERSEDED` one may not. A superseded or failed feed publishes no
`InventoryFeedApplied`; the counter `availability.inventory_feeds.applied` records every outcome.

## 3. A node is suspended, then activated again

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor OPS as ops planner
    participant NT
    participant OA
    participant DP
    participant OS
    OPS->>NT: cmd: POST /nodes/nodeId:suspend<br/>SuspendNode
    NT->>OA: evt: node.NodeSuspended
    Note over OA: node_status_view SUSPENDED<br/>Suppress every LIVE listing<br/>of the node, NODE_NOT_SELLABLE
    OA->>DP: evt: listing.<br/>ListingSuppressed
    OA->>OS: evt: listing.<br/>ListingSuppressed
    OPS->>NT: cmd: POST /nodes/nodeId:activate<br/>ActivateNode
    NT->>OA: evt: node.NodeActivated
    Note over OA: node_status_view ACTIVE<br/>Restore the listings suppressed<br/>for NODE_NOT_SELLABLE
    OA->>DP: evt: listing.<br/>ListingRestored
    OA->>OS: evt: listing.<br/>ListingRestored
```

Source: `internal/adapters/inbound/kafka/node_consumer.go`, `internal/application/usecases/node_events.go` (`ApplyNodeEvent`),
`internal/domain/nodestatus/nodestatus.go`; the node commands are `node-topology`'s.

Omitted: one `ListingSuppressed` or `ListingRestored` goes out per listing; the diagram shows one arrow per consumer. A listing
suppressed for `DISCONTINUED` is never restored. `NodeRetired` follows the first half of this flow and no later event
changes a retired node. A redelivered event (same CloudEvents `id`) is skipped through `processed_events`.

## 4. A catalogue item is created, listed and browsed

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor MER as merchandising
    participant OA
    participant DP
    participant ST
    actor SHO as storefront
    MER->>OA: cmd: POST /catalog-items<br/>CreateCatalogItem
    alt dimensions or weight not positive
        OA-->>MER: cmd: reply 422<br/>invalid-item-dimensions
    else the GTIN is malformed
        OA-->>MER: cmd: reply 422<br/>invalid-gtin
    else the GTIN is held by another item
        OA-->>MER: cmd: reply 409<br/>gtin-already-assigned
    else valid
        OA-->>MER: cmd: reply 201<br/>catalogItemId
        OA->>DP: evt: catalogitem.<br/>CatalogItemCreated
        OA->>ST: evt: catalogitem.<br/>CatalogItemCreated
    end
    SHO->>OA: qry: GET /catalog<br/>BrowseCatalog
    OA-->>SHO: qry: reply<br/>catalogue items
```

Source: `internal/application/usecases/catalog.go` (`CreateCatalogItem`), `internal/application/usecases/queries.go`
(`BrowseCatalog`), `internal/adapters/inbound/http/handlers.go`.

Then the item is listed on a node:

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor MER as merchandising
    participant OA
    participant DP
    participant OS
    participant VC
    MER->>OA: cmd: POST /listings<br/>CreateListing
    alt the item does not exist
        OA-->>MER: cmd: reply 404<br/>not-found
    else a listing for this item, node and owner exists
        OA-->>MER: cmd: reply 409<br/>duplicate-listing
    else new listing
        OA-->>MER: cmd: reply 201<br/>listingId
        OA->>DP: evt: listing.<br/>ListingCreated
        OA->>OS: evt: listing.<br/>ListingCreated
        OA->>VC: evt: listing.<br/>ListingCreated
        Note over OA: node not ACTIVE or item discontinued:<br/>also ListingSuppressed
    end
```

Source: `internal/application/usecases/listings.go` (`CreateListing`), `internal/domain/listing/listing.go` (`CreateOwned`).

A new listing has quantity 0 and basis `UNKNOWN` until the node's first feed names it. An unknown node is not sellable, so a
listing on a node `node-topology` has not yet announced starts `SUPPRESSED`.

## 5. A catalogue item is discontinued

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    actor MER as merchandising
    participant OA
    participant DP
    participant OS
    MER->>OA: cmd: POST /catalog-items/id:discontinue<br/>DiscontinueItem
    alt the item does not exist
        OA-->>MER: cmd: reply 404<br/>not-found
    else the item is already DISCONTINUED
        OA-->>MER: cmd: reply 200<br/>unchanged, no event
    else the item is ACTIVE
        Note over OA: one transaction:<br/>item DISCONTINUED,<br/>its listings suppressed<br/>with reason DISCONTINUED
        OA-->>MER: cmd: reply 200
        OA->>DP: evt: catalogitem.<br/>CatalogItemDiscontinued
        OA->>OS: evt: catalogitem.<br/>CatalogItemDiscontinued
        OA->>DP: evt: listing.<br/>ListingSuppressed per listing
        OA->>OS: evt: listing.<br/>ListingSuppressed per listing
    end
```

Source: `internal/application/usecases/catalog.go` (`DiscontinueItem`), `internal/domain/catalog/catalog.go` (`Discontinue`),
`internal/domain/listing/listing.go` (`Suppress`).

The events leave in the order `CatalogItemDiscontinued`, then `ListingSuppressed` by listing id. A later `NodeActivated` never
restores these listings.
