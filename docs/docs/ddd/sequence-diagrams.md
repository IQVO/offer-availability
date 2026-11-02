---
id: sequence-diagrams
title: Sequence Diagrams
sidebar_label: Sequence Diagrams
description: "UML sequence diagrams for the Offer and Availability write use cases, from the inbound adapter through the unit of work and the transactional outbox to Kafka, with the error branches."
---

# Sequence Diagrams

One sequence diagram per write use case of [Use cases](/docs/ddd/use-cases), plus the outbox relay. Participants, in order: the
inbound adapter (`HTTP`, or the Kafka consumer `KC`), the use case (`UC`), the aggregate, the database (`DB`) and, where it
matters, Kafka. The grey `rect` is one database transaction: the repositories, the outbox rows and, for the consumer, the
`processed_events` row are written in it, so they commit or roll back together. Messages to `DB` that start with "outbox" are
outbox writes, the others are repository calls. A hyphen before a line break splits a long name to keep the diagram narrow
(`Catalog-ItemCreated` is `CatalogItemCreated`).

Common to every REST write: the `Idempotency-Key` middleware (`internal/adapters/inbound/http/idempotency.go`) runs first. A
key already stored for the route replays the first answer with `Idempotency-Replayed: true`, a new key is recorded in
`idempotency_keys` in the same transaction as the use case. Common to every use case: `transact` runs the whole attempt a
second time when it meets `ports.ErrConcurrentUpdate`, and reports `concurrent-update` (409) only if it happens again. Event
arrows to Kafka name `<entity>.<EventName>` on `retailnetwork.offer-availability.events`; the full type is
`com.retailnetwork.commerce.offer-availability.<entity>.<EventName>`.

## 1. CreateCatalogItem

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant HTTP
    participant UC as UC
    participant AG as catalog.Item
    participant DB as Repo + Outbox
    HTTP->>UC: POST /catalog-items<br/>Idempotency-Key
    opt gtin supplied
        UC->>UC: ParseGTIN
        Note over UC: ErrInvalidGTIN gives 422<br/>invalid-gtin
    end
    rect rgb(241, 243, 245)
        UC->>DB: FindByGTIN
        alt GTIN already held
            DB-->>UC: owner item
            UC-->>HTTP: 409<br/>gtin-already-assigned
        else free
            UC->>AG: Create(id, spec, now)
            alt dimension or weight not positive
                AG-->>UC: ErrInvalid-<br/>ItemDimensions
                UC-->>HTTP: 422<br/>invalid-item-dimensions
            else valid
                AG-->>UC: item ACTIVE, version 1
                UC->>DB: Save(item)
                UC->>DB: outbox Append<br/>Catalog-<br/>ItemCreated
                UC-->>HTTP: 201 item
            end
        end
    end
```

Source: `internal/application/usecases/catalog.go` (`CreateCatalogItem`), `internal/domain/catalog/catalog.go` (`Create`),
`internal/adapters/inbound/http/problems.go`.

Omitted: the idempotency-key write (same transaction), logging and tracing. A failure rolls the transaction back, so no outbox
row exists for it.

## 2. DiscontinueItem

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant HTTP
    participant UC as UC
    participant IT as catalog.Item
    participant LI as listing.Listing
    participant DB as Repo + Outbox
    HTTP->>UC: POST /catalog-items/<br/>id/discontinuations
    rect rgb(241, 243, 245)
        UC->>DB: Catalog.Get(id)
        alt not found
            DB-->>UC: ErrNotFound
            UC-->>HTTP: 404 not-found
        else found
            DB-->>UC: item, version n
            UC->>IT: Discontinue(now)
            alt already DISCONTINUED
                IT-->>UC: no events
                UC-->>HTTP: 200 item, no event
            else ACTIVE
                IT-->>UC: CatalogItem-<br/>Discontinued
                UC->>DB: Save(item) expects version n
                UC->>DB: Listings.ListByItem(id)
                loop each listing
                    UC->>LI: Suppress(DISCONTINUED)
                    Note over LI: already DISCONTINUED:<br/>illegal, skipped
                end
                UC->>DB: SaveMany(changed listings)
                UC->>DB: outbox Append<br/>Discontinued then<br/>Suppressed per listing
                UC-->>HTTP: 200 item
            end
        end
    end
```

Source: `internal/application/usecases/catalog.go` (`DiscontinueItem`, `suppressListings`), `internal/domain/listing/listing.go`
(`Suppress`).

Omitted: the idempotency replay. A version conflict on `Save` makes `transact` retry once, then answers `409 concurrent-update`.

## 3. DeclarePackLevel

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant HTTP
    participant UC as UC
    participant IT as catalog.Item
    participant DB as Repo + Outbox
    HTTP->>UC: PUT /catalog-items/id/<br/>packaging/level
    UC->>UC: ParseGTIN, ParsePackLevel
    rect rgb(241, 243, 245)
        UC->>DB: Catalog.Get(id)
        UC->>DB: FindByGTIN
        alt GTIN held by another item
            UC-->>HTTP: 409<br/>gtin-already-assigned
        else not held elsewhere
            UC->>IT: DeclarePackLevel(level, spec, now)
            alt shape, chain or weight rule broken
                IT-->>UC: ErrPackLevelOrder or<br/>ErrInvalidPackQuantity or<br/>ErrInvalidPackDimensions
                UC-->>HTTP: 422 problem
            else same GTIN, changed unitsPerPack
                IT-->>UC: ErrPackQuantity-<br/>Immutable
                UC-->>HTTP: 409<br/>pack-quantity-immutable
            else identical declaration
                IT-->>UC: no events
                UC-->>HTTP: 200 item unchanged
            else new or re-dimensioned
                IT-->>UC: PackLevelRetired if replacing,<br/>then PackagingDeclared
                UC->>DB: Save(item with pack levels)
                UC->>DB: outbox Append events
                UC-->>HTTP: 200 item
            end
        end
    end
```

Source: `internal/application/usecases/catalog.go` (`DeclarePackLevel`), `internal/domain/catalog/packaging.go`
(`DeclarePackLevel`, `redeclare`, `checkChain`).

`RetirePackLevel` is the same shape: it retires the active definition at the level and raises `CatalogItemPackLevelRetired`,
returns the item unchanged for a level with only retired history, and answers `404 pack-level-not-found` for a level never declared.

## 4. CreateListing

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant HTTP
    participant UC as UC
    participant AG as listing.Listing
    participant DB as Repo + Outbox
    HTTP->>UC: POST /listings<br/>Idempotency-Key
    rect rgb(241, 243, 245)
        UC->>DB: Catalog.Get(catalogItemId)
        alt item unknown
            UC-->>HTTP: 404 not-found
        else known
            UC->>DB: Listings.FindByItemNodeOwner
            alt pair taken
                UC-->>HTTP: 409<br/>duplicate-listing
            else free
                UC->>DB: Nodes.Get(nodeId)
                DB-->>UC: status or not found
                UC->>AG: CreateOwned(.., Sellability, now)
                AG-->>UC: LIVE or SUPPRESSED,<br/>ListingCreated<br/>and maybe ListingSuppressed
                UC->>DB: Listings.Save
                alt unique key violated by a race
                    DB-->>UC: ErrDuplicateListing
                    UC-->>HTTP: 409<br/>duplicate-listing
                else saved
                    UC->>DB: outbox Append events
                    UC-->>HTTP: 201 listing
                end
            end
        end
    end
```

Source: `internal/application/usecases/listings.go` (`CreateListing`), `internal/domain/listing/listing.go` (`CreateOwned`).

Omitted: the idempotency replay. An unknown node counts as not sellable, so the listing starts `SUPPRESSED` with reason
`NODE_NOT_SELLABLE`; a discontinued item wins with `DISCONTINUED`.

## 5. ApplyInventoryFeed

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant HTTP
    participant UC as UC
    participant AG as feed.InventoryFeed
    participant DB as Repo + Outbox
    HTTP->>UC: POST /inventory-feeds<br/>Idempotency-Key
    UC->>AG: NewSubmission(...)
    alt malformed request
        AG-->>UC: ErrValidation
        UC-->>HTTP: 422 validation-failed<br/>nothing recorded
    else valid
        rect rgb(241, 243, 245)
            UC->>DB: Marks.Lock(nodeId) FOR UPDATE
            DB-->>UC: last applied asOf
            UC->>DB: Feeds.Get(feedId)
            alt APPLIED or SUPERSEDED
                UC-->>HTTP: 409<br/>feed-already-applied
            else new, or FAILED sent again
                UC->>DB: Feeds.Save(RECEIVED)
                alt asOf before last applied
                    UC->>AG: MarkSuperseded
                    UC->>DB: Feeds.Save(SUPERSEDED)
                    UC-->>HTTP: 201 SUPERSEDED
                else current
                    UC->>DB: Listings.GetMany, ListByNode if full
                    UC->>AG: Apply(submission, listings, now)
                    alt line off the node or negative
                        AG-->>UC: ErrListingNotOnNode or<br/>ErrNegativeAvailability
                        UC->>AG: MarkFailed(cause)
                        UC->>DB: Feeds.Save(FAILED)
                        Note over UC,DB: the row is kept,<br/>the transaction commits
                        UC-->>HTTP: 422 listing-not-on-node<br/>or negative-availability
                    else applied
                        AG-->>UC: Result applied, stale, zeroed
                        UC->>AG: MarkApplied(result, now)
                        UC->>DB: Listings.SaveMany(changed)
                        UC->>DB: Marks.Advance(nodeId, asOf)
                        UC->>DB: Feeds.Save(APPLIED)
                        UC->>DB: outbox Append<br/>Availability-<br/>Changed x n,<br/>then FeedApplied
                        UC-->>HTTP: 201 feed APPLIED
                    end
                end
            end
        end
    end
```

Source: `internal/application/usecases/feeds.go` (`ApplyInventoryFeed.attempt`, `receive`, `commit`),
`internal/domain/feed/feed.go` (`NewSubmission`, `Apply`, `MarkApplied`, `MarkFailed`, `MarkSuperseded`).

Omitted: the idempotency replay and the metric increment (`availability.inventory_feeds.applied`, one per outcome). For a
`FAILED` feed the middleware commits the writes of the 422, so the feed row is kept; the failed feed's listings are discarded,
never saved. Per line, `ApplyAvailability` skips a line whose `asOf` is not after the listing's (`ErrStaleAvailability`, counted
as stale) and the outbox gets one `ListingAvailabilityChanged` per changed listing.

## 6. ApplyNodeEvent

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant KC as Kafka consumer
    participant UC as UC
    participant VW as nodestatus.View
    participant LI as listing.Listing
    participant DB as Repo + Outbox
    KC->>UC: node.NodeSuspended,<br/>NodeRetired, NodeActivated<br/>or NodeRegistered
    rect rgb(241, 243, 245)
        UC->>DB: processed_events MarkProcessed(id)
        alt id already processed
            DB-->>UC: not new
            UC-->>KC: nil, offset committed
        else new id
            UC->>DB: Nodes.Get(nodeId)
            UC->>VW: New or Observe(status, at)
            alt Ignored
                Note over UC: older, node RETIRED,<br/>or NodeRegistered:<br/>nothing more
            else Changed or Repeated
                UC->>DB: Nodes.Save(view)
                UC->>DB: Listings.ListByNode(nodeId)
                alt node ACTIVE
                    UC->>LI: Restore(true) on each
                    Note over LI: only NODE_NOT_SELLABLE ones,<br/>others skipped
                else SUSPENDED or RETIRED
                    UC->>LI: Suppress(NODE_NOT_SELLABLE) on each
                    Note over LI: LIVE ones only,<br/>others skipped
                end
                UC->>DB: Listings.SaveMany(changed)
                UC->>DB: outbox Append<br/>Suppressed or Restored x n
            end
            UC-->>KC: nil, offset committed
        end
    end
```

Source: `internal/application/usecases/node_events.go` (`ApplyNodeEvent`), `internal/adapters/inbound/kafka/node_consumer.go`,
`internal/domain/nodestatus/nodestatus.go` (`Observe`).

The consumer dispatches on the full type (`com.retailnetwork.logistics.node-topology.node.NodeSuspended` and the other three).
A transient error rolls the unit back, is retried five times with backoff and then dead-lettered
([Runbook](/docs/operations/runbook#the-consumer-and-the-dead-letter-topic)); a malformed event is dead-lettered at once.

## 7. The outbox relay

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant Relay as Relay loop (cmd/api)
    participant DB as outbox_events
    participant Kafka
    loop every poll interval
        Relay->>DB: claim unpublished rows<br/>ORDER BY id FOR UPDATE SKIP LOCKED
        alt rows claimed
            Relay->>Kafka: publish value, key, headers
            alt published
                Relay->>DB: set published_at
            else failed
                Relay->>DB: attempts + 1, last_error
                Note over Relay,DB: after OUTBOX_MAX_ATTEMPTS the row<br/>is parked and later rows go on
            end
        end
    end
```

Source: `internal/adapters/outbound/outbox/relay.go`, `internal/adapters/outbound/postgres/outbox_repository.go` (`FOR UPDATE SKIP
LOCKED`), `internal/adapters/outbound/kafka/relay_sink.go`. The settings are on [Configuration](/docs/operations/configuration)
and the operation on the [Runbook](/docs/operations/runbook#the-outbox-in-operation).
