# ADR 0014: Received stock creates owner listings

## Status

Accepted (2026-11-09). Closes network gap G51 (received seller stock never
becomes a listing). Supersedes in part
[ADR 0013](./0013-seller-status-and-received-stock-drive-owner-listings.md): its
section "Rule 8 is blocked on the producer payload" and the consequence "G51 stays
open". Rule 8 of ADR 0013 itself stands and is built as written there; this ADR
records the payload it reads and how the build reads it. Extends
[ADR 0003](./0003-consumed-contracts-and-local-node-status.md) and
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) with a third
consumed seller-inbound type.

## Context

ADR 0013 decided rule 8 (an `InboundShipmentReceived` creates the owner's listings)
but could not build it: seller-inbound's event carried the received cartons and no
item lines, so `InboundShipmentReceived` stayed one of the ignored, counted types
of `retailnetwork.seller-inbound.events`.

seller-inbound's ADR 0014 adds the missing lines, additively, to
`com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived`
on the topic this service already consumes:

- `data = {shipmentId, sellerId, nodeId, status: CLOSED|CLOSED_WITH_DISCREPANCY,
  cartons: [{sscc, status}], receivedAt, received: [{catalogItemId?: string,
  gtin: string ^[0-9]{14}$, qtyEaches: integer >= 1}]}`;
- `received` is the EACH-level GTIN when the producer can resolve it, otherwise the
  carton GTIN; `catalogItemId` is omitted when unresolved; the list is sorted by
  `gtin` with zero lines omitted;
- events published before that deploy have no `received`.

Golden payload (the consumer decode test pins it):

```json
{"shipmentId":"shp-1","sellerId":"S-1","nodeId":"n-1","status":"CLOSED",
 "cartons":[{"sscc":"006141410000000012","status":"RECEIVED"}],
 "receivedAt":"2026-10-24T12:00:00Z",
 "received":[{"catalogItemId":"item-1","gtin":"00614141000012","qtyEaches":24},
             {"gtin":"00614141000036","qtyEaches":6}]}
```

## Decision

The orchestrator's contract, verbatim:

- Build rule 8 of ADR 0013 exactly as written there.
- For each received line with qtyEaches > 0, resolve catalogItemId:
  - from the line when present;
  - else through this context's own GTIN/packaging index (ADR 0006): a case or inner GTIN resolves to its item, a retired GTIN still resolves;
  - an unresolvable GTIN is skipped with a WARN and a counter metric (extend the existing availability.seller_events.ignored counter's reason vocabulary, or add one following the repo's telemetry naming), never an error.
- For each (catalogItemId, nodeId, ownerRef=sellerId) with no listing, create one (system actor): ListingCreated with ownerRef and availability 0 / basis UNKNOWN, exactly like CreateListing. Then raise ListingSuppressed when the node is not sellable, the item is discontinued, or the owner is SUSPENDED (rule 7, the SellerStatusView already built). An existing listing is a no-op. Do not touch availability: only the vendor feed sets it.
- One transaction per event, inbox-idempotent.
- Events published before that deploy have no `received`. Treat absence as UNKNOWN: create nothing, and count it under a distinct reason label.

### How the build reads the contract

- **Consumed type.** `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived`
  joins `SellerSuspended` and `SellerReinstated` on the seller-inbound consumer
  (same topic, same `SELLER_INBOUND_CONSUMER_GROUP`, same DLQ), dispatched on the
  full type. Fields read: `sellerId`, `nodeId`, `shipmentId` (logs only) and
  `received[].{catalogItemId?, gtin, qtyEaches}`. `status`, `cartons` and
  `receivedAt` are not read: a `CLOSED_WITH_DISCREPANCY` shipment still lists what
  it did receive, and the quantities are never applied (availability comes only
  from the vendor feed). The other seller-inbound types stay ignored and counted.
- **Use case `ApplyReceivedStock`.** ONE unit of work per event, inbox row
  included (`processed_events`): a CloudEvents id already in the inbox changes
  nothing and counts nothing. Then, in the order of `received` (a slice, so the
  order is the producer's gtin order):
  - a line with `qtyEaches <= 0` is skipped silently (the producer omits them);
  - the item is the line's `catalogItemId` when present, otherwise
    `CatalogRepository.FindByGTIN`, which answers an item's own GTIN and every
    pack level GTIN of it, retired levels included (ADR 0006 decisions 2 and 4);
    a GTIN that is not a well-formed GS1 GTIN cannot be in the index and is
    unresolvable too;
  - a line that resolves to an item already handled for this event is a no-op
    (the EACH and a CASE line of one item make one listing);
  - when no listing of `(item, nodeId, sellerId)` exists, the listing is created
    with `listing.CreateOwned` from the same three causes `CreateListing` reads
    (local NodeStatusView: an unknown node is not sellable; the item's
    discontinued flag; local SellerStatusView: an unknown seller is ACTIVE), so it
    raises `ListingCreated` (with `ownerRef`, availability 0, basis `UNKNOWN`) and,
    for the highest active cause, `ListingSuppressed`; an existing listing is left
    exactly as it is.
  A listing created concurrently by `POST /listings` between the lookup and the
  save trips the unique `(item, node, owner)` index: the whole attempt runs once
  more on fresh state, where that listing is the no-op case.
- **Skips are counted, never errors.** The existing counter
  `availability.seller_events.ignored` (attribute `type`) gains a `reason`
  attribute:
  - `type_not_consumed`: a type of the topic this service does not consume (what
    the counter counted before, one per event);
  - `received_unknown`: an `InboundShipmentReceived` without `received` (published
    before seller-inbound's ADR 0014); nothing is created, the id still lands in
    the inbox (one per event);
  - `gtin_unresolved`: one per received line that carries no `catalogItemId` and
    whose GTIN no catalogue item holds; logged at WARN with the shipment, seller,
    node and GTIN;
  - `item_unknown`: one per line whose `catalogItemId` this catalogue does not
    hold (the producer resolves ids from this service's own events, so it should
    not happen); logged at WARN like `gtin_unresolved`. This label is the build's
    reading of "never an error" for a case the contract does not name: a listing
    always names a stored item, and refusing the whole event would dead-letter the
    lines that do resolve.
  The counts are recorded after the unit of work commits, so a retried attempt or
  a redelivered id never counts twice.
- **Poison.** An `InboundShipmentReceived` whose `data` does not decode, or that
  lacks `sellerId` or `nodeId`, or whose `sellerId`/`nodeId` is not a valid id, is
  dead-lettered to `retailnetwork.seller-inbound.events.dlq` like the seller
  types. A malformed line is never poison: it is skipped and counted.
- **Contract surfaces.** `apis/asyncapi.yaml` declares the consumed message with
  exactly the fields read; ADR 0003 and ADR 0004 gain an amendment listing the
  type. No REST, MCP or A2A shape changes: the new listings are ordinary owner
  listings read through the existing routes and tools.

## Consequences

- Received seller stock appears as a listing of the seller on the receiving node
  within the consumer lag, at availability 0 / `UNKNOWN` until the vendor's
  inventory feed declares it; the listing starts `SUPPRESSED` when its node is not
  sellable, its item is discontinued or its owner is suspended, exactly like a
  listing created over REST.
- Events published before seller-inbound's ADR 0014 are not replayed into
  listings: they are counted as `received_unknown`, and their stock needs a
  `POST /listings` or a later shipment.
- `availability.seller_events.ignored` gains the `reason` label (additive for
  Prometheus: `availability_seller_events_ignored_total{type, reason}`); a query
  that sums by `type` is unchanged.
- No chart, environment or consumer-group change: the type arrives on the topic
  and in the group the service already consumes.
