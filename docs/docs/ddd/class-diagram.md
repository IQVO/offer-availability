---
id: class-diagram
title: Class Diagram
sidebar_label: Class Diagram
description: "UML class diagrams of the Offer and Availability domain model as built, then its hexagonal ports and the adapters that implement them."
---

# Class Diagram

UML class diagrams of the domain packages as built, then the hexagonal ports and adapters. Names are the Go identifiers on
`develop`. Fields are shown `-` (unexported), methods `+` (exported); `$` marks a constructor. Getters, `Snapshot` and
`Restore` (rebuild from persistence, validating every invariant) exist on every aggregate and are omitted.

## 1. CatalogItem aggregate

```mermaid
classDiagram
    direction TB
    class Item {
        <<AggregateRoot>>
        -id ID
        -spec Spec
        -state State
        -gtin gs1.GTIN
        -packaging PackLevelDef[]
        -version int64
        +Create(id, spec, now)$ Item
        +Discontinue(now) Event[]
        +DeclarePackLevel(level, spec, now) Event[]
        +RetirePackLevel(level, now) Event[]
        +Packaging() PackLevelDef[]
    }
    class Spec {
        <<ValueObject>>
        +Title string
        +LengthMm int
        +WidthMm int
        +HeightMm int
        +WeightG int
        +Hazmat bool
        +Fragile bool
        +GTIN gs1.GTIN
    }
    class PackLevelDef {
        <<Entity>>
        +Level PackLevel
        +GTIN gs1.GTIN
        +ChildGTIN gs1.GTIN
        +UnitsPerPack int
        +LengthMm int
        +WidthMm int
        +HeightMm int
        +GrossWeightG int
        +Active bool
        +DeclaredAt time.Time
        +RetiredAt time.Time
    }
    class State {
        <<Enumeration>>
        ACTIVE
        DISCONTINUED
    }
    class PackLevel {
        <<Enumeration>>
        EACH
        INNER
        CASE
        PALLET
    }
    Item *-- Spec
    Item *-- "0..*" PackLevelDef : packaging
    Item --> State
    PackLevelDef --> PackLevel
```

Source: `internal/domain/catalog/catalog.go`, `internal/domain/catalog/packaging.go`.

The invariants (`ErrInvalidItemDimensions`, `ErrInvalidGTIN`, `ErrGtinAlreadyAssigned`, `ErrPackQuantityImmutable`,
`ErrPackLevelOrder`, `ErrInvalidPackQuantity`, `ErrInvalidPackDimensions`, `ErrPackLevelNotFound`) are on the
[Aggregate design canvas](/docs/ddd/aggregate-design-canvas). `gs1.GTIN` (`internal/domain/shared/gs1`) is the 14-digit
value type with the mod-10 check; `catalog.ParseGTIN` normalises 8, 12 and 13 digits to 14.

## 2. Listing aggregate

```mermaid
classDiagram
    direction TB
    class Listing {
        <<AggregateRoot>>
        -id ID
        -item ItemID
        -node NodeID
        -owner OwnerRef
        -state State
        -reason Reason
        -avail Availability
        -version int64
        +Create(id, item, node, sellability, now)$ Listing
        +CreateOwned(id, item, node, owner, sellability, now)$ Listing
        +ApplyAvailability(qty, basis, asOf, feedID, now) Event[]
        +ZeroOut(asOf, feedID, now) Event[]
        +Suppress(reason, now) Event[]
        +Restore(nodeSellable, now) Event[]
    }
    class Availability {
        <<ValueObject>>
        +Qty int
        +Basis Basis
        +AsOf time.Time
        +FeedID string
    }
    class Sellability {
        <<ValueObject>>
        +NodeActive bool
        +ItemDiscontinued bool
    }
    class Basis {
        <<Enumeration>>
        PHYSICAL
        THROUGHPUT_CONSTRAINED
        UNKNOWN
    }
    class State {
        <<Enumeration>>
        LIVE
        SUPPRESSED
    }
    class Reason {
        <<Enumeration>>
        NODE_NOT_SELLABLE
        DISCONTINUED
        OWNER_SUSPENDED
    }
    Listing *-- Availability
    Availability --> Basis
    Listing --> State
    Listing --> Reason
    Listing ..> Sellability : created from
```

Source: `internal/domain/listing/listing.go`.

Errors: `ApplyAvailability` returns `ErrNegativeAvailability` and `ErrStaleAvailability`; `Restore` returns
`shared.ErrIllegalTransition` and `ErrNodeNotSellable`; `Suppress` returns `shared.ErrIllegalTransition`. The duplicate
check (`ErrDuplicateListing`) is made by `usecases.CreateListing` and backed by the unique key of the repository.

## 3. InventoryFeed aggregate

```mermaid
classDiagram
    direction TB
    class InventoryFeed {
        <<AggregateRoot>>
        -id ID
        -node NodeID
        -full bool
        -asOf time.Time
        -lineCount int
        -state State
        -applied int
        -stale int
        -zeroed int
        -failure string
        -receivedAt time.Time
        -appliedAt time.Time
        -version int64
        +Receive(submission, now)$ InventoryFeed
        +Resubmit(submission, now) error
        +Apply(submission, listings, now) Result
        +MarkApplied(result, now) Event[]
        +MarkFailed(cause) error
        +MarkSuperseded() error
    }
    class Submission {
        <<ValueObject>>
        +ID ID
        +NodeID NodeID
        +Full bool
        +AsOf time.Time
        +Lines Line[]
        +NewSubmission(...)$ Submission
    }
    class Line {
        <<ValueObject>>
        +ListingID listing.ID
        +Qty int
        +Basis listing.Basis
    }
    class Result {
        <<ValueObject>>
        +Events Event[]
        +Changed Listing[]
        +Applied int
        +Stale int
        +Zeroed int
    }
    class State {
        <<Enumeration>>
        RECEIVED
        APPLIED
        FAILED
        SUPERSEDED
    }
    InventoryFeed --> State
    InventoryFeed ..> Submission : validates against
    Submission *-- "0..10000" Line
    InventoryFeed ..> Result : produces
```

Source: `internal/domain/feed/feed.go`.

The feed does **not** keep its lines: `Submission` and `Line` are request values, and the persisted aggregate holds counters.
`Apply` takes the listings the use case loaded and does not touch the feed; `MarkApplied`, `MarkFailed` and `MarkSuperseded`
move the state. `feed.Superseded(asOf, lastApplied)` is the node-level staleness rule. `MarkFailed` accepts only
`ErrListingNotOnNode` and `listing.ErrNegativeAvailability` and stores the failure code.

## 3b. The node status read model

```mermaid
classDiagram
    direction LR
    class View {
        <<ReadModel>>
        -id ID
        -status Status
        -at time.Time
        -version int64
        +New(id, status, at)$ View
        +Observe(status, at) Outcome
        +Sellable() bool
    }
    class Status {
        <<Enumeration>>
        PROPOSED
        ACTIVE
        SUSPENDED
        RETIRED
    }
    class Outcome {
        <<Enumeration>>
        Ignored
        Repeated
        Changed
    }
    View --> Status
    View ..> Outcome : returns
```

Source: `internal/domain/nodestatus/nodestatus.go`.

`Observe` ignores an observation older than the current status and anything but `RETIRED` after `RETIRED`. `Sellable`
is true only for `ACTIVE`. The policy `SuppressOnNodeUnavailable` is not a class: it is the body of
`usecases.ApplyNodeEvent` (`internal/application/usecases/node_events.go`).

## 4. Domain events

```mermaid
classDiagram
    direction LR
    class CatalogItemCreated {
        <<DomainEvent>>
        +CatalogItemID ID
        +Title string
        +LengthMm int
        +WidthMm int
        +HeightMm int
        +WeightG int
        +Hazmat bool
        +Fragile bool
        +GTIN gs1.GTIN
    }
    class CatalogItemDiscontinued {
        <<DomainEvent>>
        +CatalogItemID ID
    }
    class CatalogItemPackagingDeclared {
        <<DomainEvent>>
        +CatalogItemID ID
        +Level PackLevel
        +GTIN gs1.GTIN
        +ChildGTIN gs1.GTIN
        +UnitsPerPack int
        +DeclaredAt time.Time
    }
    class CatalogItemPackLevelRetired {
        <<DomainEvent>>
        +CatalogItemID ID
        +Level PackLevel
        +GTIN gs1.GTIN
        +RetiredAt time.Time
    }
    class ListingCreated {
        <<DomainEvent>>
        +ListingID ID
        +CatalogItemID ItemID
        +NodeID NodeID
        +OwnerRef OwnerRef
    }
    class ListingAvailabilityChanged {
        <<DomainEvent>>
        +ListingID ID
        +CatalogItemID ItemID
        +NodeID NodeID
        +OwnerRef OwnerRef
        +AvailableQty int
        +Basis Basis
        +AsOf time.Time
        +FeedID string
    }
    class ListingSuppressed {
        <<DomainEvent>>
        +ListingID ID
        +NodeID NodeID
        +Reason Reason
    }
    class ListingRestored {
        <<DomainEvent>>
        +ListingID ID
        +NodeID NodeID
    }
    class InventoryFeedApplied {
        <<DomainEvent>>
        +FeedID ID
        +NodeID NodeID
        +FullUpdate bool
        +LineCount int
        +AppliedAt time.Time
    }
```

Source: `internal/domain/catalog/events.go`, `internal/domain/listing/events.go`, `internal/domain/feed/events.go`.

Every event embeds `shared.Header{Version, At}` and implements `shared.Event` (`EventName()`, `Entity()`, `SubjectID()`,
`AggregateVersion()`, `OccurredAt()`); the CloudEvents envelope is built only in `internal/adapters/kafka/cloudevents`. The
wire fields and JSON names are on [Domain events](/docs/ddd/domain-events).

## 5. Hexagonal ports and adapters

Driving side: inbound adapters, use cases, domain.

```mermaid
flowchart TB
    subgraph IN["internal/adapters/inbound"]
        direction LR
        HTTP["http<br/>REST routes"]
        MCP["mcp<br/>four read tools"]
        A2A["a2a<br/>two read skills"]
        KC["kafka<br/>node-topology consumer"]
    end
    subgraph APP["internal/application/usecases"]
        direction LR
        UC1["CreateCatalogItem, DiscontinueItem<br/>DeclarePackLevel, RetirePackLevel"]
        UC2["CreateListing"]
        UC3["ApplyInventoryFeed"]
        UC4["ApplyNodeEvent"]
        UC5["BrowseCatalog, GetCatalogItem<br/>ListListings, GetInventoryFeed"]
    end
    subgraph DOM["internal/domain"]
        direction LR
        D1["catalog.Item"]
        D2["listing.Listing"]
        D3["feed.InventoryFeed"]
        D4["nodestatus.View"]
    end
    HTTP --> UC1
    HTTP --> UC2
    HTTP --> UC3
    HTTP --> UC5
    MCP --> UC5
    A2A --> UC5
    KC --> UC4
    APP --> DOM
```

Source: `internal/adapters/inbound/{http,mcp,a2a,kafka}`, `internal/application/usecases`, `cmd/api/run.go`, `cmd/mcp/router.go`,
`cmd/a2a/router.go`.

MCP and A2A are separate binaries (`cmd/mcp`, `cmd/a2a`) that wire only the read use cases; REST, the consumer, the relay and the
workers run in `cmd/api`.

Driven side: ports and outbound adapters.

```mermaid
flowchart TB
    APP["internal/application/usecases"]
    subgraph PORTS["internal/application/ports"]
        direction LR
        P1["CatalogRepository<br/>ListingRepository<br/>FeedRepository"]
        P2["NodeStatusRepository<br/>NodeFeedMarks"]
        P3["EventOutbox<br/>EventEncoder<br/>ProcessedEvents"]
        P4["UnitOfWork<br/>Clock<br/>IDGenerator<br/>FeedMetrics"]
    end
    subgraph OUT["internal/adapters/outbound"]
        direction LR
        A1["postgres<br/>repositories, marks, outbox,<br/>inbox, unit of work, retention"]
        A2["outbox + kafka<br/>relay, encoder, writer"]
        A3["clock, ids, telemetry"]
        A4["memory<br/>test double only"]
    end
    APP --> PORTS
    P1 --- A1
    P2 --- A1
    P3 --- A1
    P4 --- A1
    P3 --- A2
    P4 --- A3
    PORTS -.-> A4
```

Source: `internal/application/ports/ports.go`, `internal/adapters/outbound/{postgres,outbox,kafka,clock,ids,telemetry,memory}`.

Notes: `postgres` implements the repositories, `NodeFeedMarks` (the per-node lock, `FOR UPDATE`), the outbox
(`outbox_repository.go`), the inbox (`ProcessedEvents`) and the unit of work; `outbox/relay.go` drains `outbox_events`
through the Kafka sink (`kafka/relay_sink.go`, `writer.go`) and `kafka/encoder.go` implements `EventEncoder`. The `memory`
adapter backs unit tests only: `cmd/**` never imports it, and a fitness test enforces that
(`TestRuntimeBinaryHasNoInMemoryAdapters`). `repocontract` is the shared contract suite both stores must pass. The Kafka consumer
writes the node view through its port inside the same unit of work as the listing changes. Omitted: `cmd/` composition roots and
configuration.
