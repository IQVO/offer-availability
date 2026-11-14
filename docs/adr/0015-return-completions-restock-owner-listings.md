# ADR 0015: Return completions restock owner listings

## Status

Accepted (2026-11-14). Extends [ADR 0003](./0003-consumed-contracts-and-local-node-status.md)
and [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) with the first
consumed type of the customer-returns context, on a topic this service did not
read yet. Builds the same shape of fact as [ADR 0014](./0014-received-stock-creates-owner-listings):
an event-fed quantity change of an owner listing.

## Context

The customer-returns context (wave 4) closes a return at a node and publishes
what physically came back. The orchestrator's contract, verbatim:

- OA (branch `feature/returns-restock`, ADR 0015, migration 0005): consumes
  `ReturnCompleted` (new consumer group `offer-availability-customer-returns` on
  the CR topic) exactly like the SI received-stock consumer (F28): per line,
  raise availability of the owner listing at `nodeId` by `qtyRestockable`; lines
  without a known listing are counted and skipped (WARN). Unsellable units never
  touch listings.

The consumed event (the design's event shape is the contract; the producing repo
lands in parallel with goldens that match it):

- type `com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnCompleted`
  on `retailnetwork.customer-returns.events`, subject and Kafka key
  `authorizationId`, source `/retailnetwork/customer-returns`;
- `data = {authorizationId, outcome: RECEIVED, nodeId,
  lines: [{catalogItemId, ownerRef, qtyRestockable, qtyUnsellable}], receivedAt}`;
- `qtyRestockable` is the count the node can sell again, `qtyUnsellable` the
  count it cannot; `ownerRef` is the seller whose stock was returned (the id the
  fulfillment lock carried), empty meaning the operator's own stock.

Golden payload (the consumer decode test pins it):

```json
{"authorizationId":"ret-1","outcome":"RECEIVED","nodeId":"n-1",
 "lines":[{"catalogItemId":"item-1","ownerRef":"S-1","qtyRestockable":2,"qtyUnsellable":1}],
 "receivedAt":"2026-11-14T12:00:00Z"}
```

## Decision

- **Consumed type.** `ReturnCompleted` is consumed from
  `retailnetwork.customer-returns.events` under the new mandatory
  `CUSTOMER_RETURNS_CONSUMER_GROUP` (a boot error when unset, like the other
  groups; never a literal). Dispatch on the full `type`; every other type on the
  topic is acknowledged and counted. Fields read: `authorizationId`, `nodeId`,
  `lines[].{catalogItemId, ownerRef, qtyRestockable, qtyUnsellable}` and
  `receivedAt` (the CloudEvents `time` when the payload carries no instant).
  `outcome` is not read: `ReturnCompleted` is published for one outcome only.
- **Use case `ApplyReturnCompletion`.** ONE unit of work per event, the
  CloudEvents id recorded in `processed_events` first (a redelivery changes
  nothing). Then, in the order of `lines`:
  - a line with `qtyRestockable <= 0` is skipped and counted
    (`nothing_restockable`): unsellable units never touch a listing, and a line
    that restocks nothing has nothing to do;
  - a line without a usable `catalogItemId` (absent or not a valid id) is
    skipped and counted (`item_absent`), never an error;
  - the listing of `(catalogItemId, nodeId, ownerRef)` is looked up; a line
    with no such listing is skipped and counted (`listing_unknown`) and logged
    at WARN — the return was real, only the listing is missing (the item is not
    offered from that node for that owner, or the listing was never created);
  - a found listing is restocked by `qtyRestockable` (below). Its
    `ListingAvailabilityChanged` is appended to the outbox in the same
    transaction.
- **`Listing.Restock(qty, at, ref)`.** A restock is a DELTA, not a snapshot:
  the available quantity becomes `availableQty + qty`; a feed replaces the
  whole value, a return completion adds to it. The basis never changes (a
  listing that never had a feed stays `UNKNOWN`; the vendor's claim is still
  the only basis knower). The provenance pair `(asOf, feedId)` moves only
  forward: a restock newer than the current fact stamps the pair with
  `receivedAt` and the `authorizationId`; a restock older than the current
  fact leaves the feed's stamp and still adds its quantity. The state is not
  touched (A8): a SUPPRESSED listing is restocked too, so the quantity is
  right the day the cause clears.
- **Skips are counted, never errors**, on the new counter
  `availability.customer_returns_events.ignored` (attributes `type`, `reason`;
  the fleet naming, mirroring `availability.seller_events.ignored`):
  `type_not_consumed` (one per event of another type), `nothing_restockable`,
  `item_absent`, `listing_unknown` (one per skipped line). The counts are
  recorded after the unit of work commits, so a retried attempt or a
  redelivered id never counts twice. An event without `lines` is acknowledged
  and counted once (`lines_absent`): nothing was restocked and nothing was
  lost.
- **Poison.** A `ReturnCompleted` whose `data` does not decode, or that lacks a
  usable `authorizationId`, `nodeId` or instant, is dead-lettered to
  `retailnetwork.customer-returns.events.dlq` after the same 5-attempt policy
  as the other consumers. A malformed line is never poison: it is skipped and
  counted.
- **No migration.** The restock reuses the availability provenance pair
  (`as_of`, `feed_id`) the schema already stores; the pair's id column carries
  the id of the fact that last stamped availability — a feed id or, from here,
  a return authorization id. Nothing is renamed and no column is added, so
  there is no migration 0005 in this branch; the number stays free.

## Consequences

- A closed return raises the owner listing's available quantity within the
  consumer lag; shoppers can buy the returned sellable units again without any
  vendor feed.
- `ListingAvailabilityChanged` (and the listing views) can now carry an
  authorizationId where they carried only feed ids; tolerant consumers ignore
  the value, and the pair's meaning ("the fact that last set availability") is
  the additive reading. A feed after a restock replaces the quantity as always.
- Two env/chart values gain a sibling: `CUSTOMER_RETURNS_CONSUMER_GROUP` from
  `config.customerReturnsConsumerGroup` (required, no default; the deploy sets
  `offer-availability-customer-returns`), a fifth always-on loop, and the CR
  topic with its DLQ pre-created by warehouse-infra.
- Unsellable units vanish from the sellable side by construction: they are
  never added anywhere.
