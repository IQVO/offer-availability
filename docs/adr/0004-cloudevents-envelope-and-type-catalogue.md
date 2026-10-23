# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-23).

## Context

Every Kafka message in the retail network is a CloudEvents 1.0 event in structured
mode (retail-network-docs `model/model.yaml` `conventions.cloudevents`; the repo
rule `.claude/rules/fleet/cloudevents.md`). This ADR is the catalogue of every
type offer-availability publishes. `TestEventCatalogueMatchesContract` checks it
against `apis/asyncapi.yaml`. The four node-topology types the service consumes
are listed in ADR 0003, not here.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable across
  relay retries; consumers dedupe on it); `source`
  `/retailnetwork/offer-availability`; `subject` the aggregate root id
  (`catalogItemId`, `listingId` or `feedId`); `time` the instant the change was
  committed, in UTC; `datacontenttype` `application/json`; `dataschema`
  `urn:retailnetwork:offer-availability:events:<EventName>:v1`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; Kafka key = the
  subject, so all events of one aggregate stay ordered on one partition.
- `data` is camelCase; optional fields are omitted when unset (no published field
  is nullable); timestamps are RFC 3339 UTC.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.
- `<entity>` is the raising aggregate, lowercase, no separators: `catalogitem`,
  `listing`, `inventoryfeed`. Tier is `commerce`.

### Published types

Every type below goes to the topic `retailnetwork.offer-availability.events`. The
model declares no analytics-only event for this context, so nothing is written to
`retailnetwork.offer-availability.analytics`.

```text
type: com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated stream: events
type: com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemDiscontinued stream: events
type: com.retailnetwork.commerce.offer-availability.listing.ListingCreated stream: events
type: com.retailnetwork.commerce.offer-availability.listing.ListingAvailabilityChanged stream: events
type: com.retailnetwork.commerce.offer-availability.listing.ListingSuppressed stream: events
type: com.retailnetwork.commerce.offer-availability.listing.ListingRestored stream: events
type: com.retailnetwork.commerce.offer-availability.inventoryfeed.InventoryFeedApplied stream: events
```

| Event | Subject / key | Raised when | `data` |
|---|---|---|---|
| `CatalogItemCreated` | `catalogItemId` | CreateCatalogItem is saved (state ACTIVE) | `{catalogItemId, title, lengthMm, widthMm, heightMm, weightG, hazmat, fragile}` |
| `CatalogItemDiscontinued` | `catalogItemId` | DiscontinueItem is saved (once; repeating it raises nothing) | `{catalogItemId}` |
| `ListingCreated` | `listingId` | CreateListing is saved | `{listingId, catalogItemId, nodeId}` |
| `ListingAvailabilityChanged` | `listingId` | a feed line is applied, or FullFeedZeroing zeroes a listing | `{listingId, catalogItemId, nodeId, availableQty, basis, asOf, feedId}` |
| `ListingSuppressed` | `listingId` | SuppressListing is saved (node unavailable, creation on a non-sellable node or discontinued item, DiscontinueItem) | `{listingId, nodeId, reason}` |
| `ListingRestored` | `listingId` | RestoreListing is saved (node activated) | `{listingId, nodeId}` |
| `InventoryFeedApplied` | `feedId` | ApplyInventoryFeed reaches APPLIED | `{feedId, nodeId, fullUpdate, lineCount, appliedAt}` |

Payload rules:

- The `data` field lists are exactly the model's `publishes[].data` lists
  (contracts-delta adds nothing for this context). Wire types are an
  implementation decision recorded in `apis/asyncapi.yaml` (ADR 0002, A17).
- `reason` is `NODE_NOT_SELLABLE` or `DISCONTINUED`; `basis` is `PHYSICAL`,
  `THROUGHPUT_CONSTRAINED` or `UNKNOWN`.
- A listing created while its node is not sellable (or its item discontinued)
  raises `ListingCreated` and, in the same transaction, `ListingSuppressed`
  (ADR 0002, A6).
- A `FAILED` or `SUPERSEDED` feed raises no event.
- A breaking payload change is a new `.v2` type and dataschema, never a mutation
  of v1.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is
  no shared Go code.
- Adding a published type requires a line here in the same commit as its
  asyncapi message, or the architecture fitness test fails.
