# ADR 0013: Seller status and received stock drive owner listings

## Status

Accepted (2026-11-08). Closes network gap G48 (a suspended seller's listings stay
live). Network gap G51 (received seller stock never becomes a listing) is decided
here but NOT built: see "Rule 8 is blocked on the producer payload" below.
Extends [ADR 0003](./0003-consumed-contracts-and-local-node-status.md) (a second
consumed topic and a second local read model),
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) (the consumed type
list) and [ADR 0006](./0006-catalog-gtin-packaging-and-listing-owner.md) (the
optional `ownerRef` of a listing). Amends ADR 0002 A4, A5 and A14 by precedence
(rule 3).

## Context

Listings carry an optional `ownerRef` (the `sellerId`; ADR 0006), but nothing in
this context knows what happens to the seller:

- **G48.** seller-inbound suspends a seller (`SellerSuspended`), yet the seller's
  listings stay `LIVE`, because offer-availability consumes no seller event.
- **G51.** seller-inbound closes an inbound shipment (`InboundShipmentReceived`),
  but `CreateListing` is a manual command and the vendor inventory feed names only
  listings that already exist, so received seller stock never becomes a listing.

Alternatives weighed:

- *A synchronous call to seller-inbound when a listing is created or restored.*
  Refused: hard rule 4 (sibling data used in a decision is a local, event-fed read
  model) and the promise hot path makes no synchronous call.
- *A per-listing set of active causes, persisted on the listing.* Refused: the two
  causes that can clear (node, owner) already live in local read models
  (`NodeStatusView`, the new `SellerStatusView`), and `DISCONTINUED` never clears.
  Deriving the remaining cause from the views at the moment a cause clears keeps one
  source of truth per cause and needs no new listing column.

## Decision

The orchestrator's contract, verbatim:

1. Subscribe to `retailnetwork.seller-inbound.events`. Dispatch on the full type string. Ignore every other type in that topic (counted, not an error). Inbox-idempotent.
2. New local read model SellerStatusView(sellerId PK, status ACTIVE|SUSPENDED, asOf). It is fed by SellerSuspended (status SUSPENDED, asOf=suspendedAt) and SellerReinstated (ACTIVE, asOf=reinstatedAt). An event whose instant is older than the stored asOf is ignored. An unknown seller is ACTIVE.
3. New suppression reason `OWNER_SUSPENDED`: add it to the reasons vocabulary, the OpenAPI enum and the ADR. Precedence: DISCONTINUED (final) > NODE_NOT_SELLABLE > OWNER_SUSPENDED. A listing shows one current reason, the highest active cause.
4. On SellerSuspended: every LIVE listing with ownerRef = sellerId is suppressed with reason OWNER_SUSPENDED (one ListingSuppressed each, same transaction as the view update). Listings already suppressed for a higher-precedence cause keep that reason; record that the owner is suspended so a later restore of the higher cause does not make the listing LIVE.
5. On SellerReinstated: every listing of that owner whose current reason is OWNER_SUSPENDED is restored (ListingRestored), unless another cause still holds. Then the reason switches to the remaining highest cause and a ListingSuppressed with the new reason is raised, mirroring the existing NODE_NOT_SELLABLE -> DISCONTINUED re-suppression rule.
6. NodeActivated's existing restore path must not restore a listing whose owner is SUSPENDED. It switches the reason to OWNER_SUSPENDED instead.
7. CreateListing, both REST and the automatic path below, for an owner whose SellerStatusView is SUSPENDED creates the listing SUPPRESSED (OWNER_SUSPENDED), following the existing pattern for a non-sellable node.
8. On InboundShipmentReceived: for each received (catalogItemId, nodeId, ownerRef=sellerId) with eaches > 0 and no existing listing for that triple, create a listing (system actor):
   - ListingCreated with ownerRef;
   - availability 0 with basis UNKNOWN, exactly as today's CreateListing (availability keeps coming only from the vendor's inventory feed: declared-then-measured);
   - followed by ListingSuppressed when the node is not sellable, the item is discontinued, or the owner is suspended.
   Resolve the item:
   - If the payload carries catalogItemId, use it.
   - If it carries only GTINs, resolve through this context's own GTIN/packaging index (ADR 0006). A case GTIN resolves to its item.
   - An unresolvable GTIN is skipped with a WARN log and a counter metric (follow the repo's telemetry naming), never an error.
   A listing that already exists is a no-op. One transaction per event.

### How the build reads the contract

- **Consumed types.** From seller-inbound, topic `retailnetwork.seller-inbound.events`,
  dispatched on the FULL type:
  - `com.retailnetwork.partner.seller-inbound.seller.SellerSuspended`: reads `sellerId`, `suspendedAt` (an optional `reason` is not stored);
  - `com.retailnetwork.partner.seller-inbound.seller.SellerReinstated`: reads `sellerId`, `reinstatedAt`.

  Every other type on the topic (`InboundShipmentLabelled`, `InboundShipmentShipped`,
  `InboundShipmentReceived` while rule 8 is blocked, `PlacementPlanSuggested`, any
  future type) is acknowledged and counted in the counter
  `availability.seller_events.ignored` (attribute `type`), never dead-lettered. A
  payload of a consumed type that lacks `sellerId` or its instant goes to
  `retailnetwork.seller-inbound.events.dlq` (as ADR 0002 A23 does for nodes). The
  inbox is the existing `processed_events`, in the same transaction as the view
  and the listing changes. The consumer group comes from the mandatory
  `SELLER_INBOUND_CONSUMER_GROUP` (a boot error naming it; never a literal).
- **SellerStatusView.** Table `seller_status_view(seller_id, status, as_of,
  version)`, written only by the seller consumer. Ordering mirrors A13: an
  observation older than `asOf` is ignored whole (no listing is touched); an equal
  or later one for the same status repeats the policy, which then finds nothing to
  change; an unknown seller is created from its first observation. A seller the
  view does not know is `ACTIVE` (the opposite of A5's unknown node: a seller is
  sellable until seller-inbound says otherwise, so event lag never hides stock).
- **Precedence is one rule on the listing.** `Suppress` accepts a SUPPRESSED
  listing only when the new reason ranks above the current one
  (`OWNER_SUSPENDED` -> `NODE_NOT_SELLABLE` -> `DISCONTINUED`), which generalises
  A4's `NODE_NOT_SELLABLE` -> `DISCONTINUED` upgrade. `Reconcile` takes the
  sellability of the listing's three causes (node active, item discontinued, owner
  suspended) and moves the listing to the highest active cause: `LIVE` when none
  holds (`ListingRestored`), otherwise `SUPPRESSED` with that reason (one
  `ListingSuppressed` when the reason changes). `DISCONTINUED` stays final.
- **The record of rule 4 is the SellerStatusView.** A listing kept at
  `NODE_NOT_SELLABLE` or `DISCONTINUED` while its owner is suspended carries no
  extra column: the restore paths read the owner's row. On `NodeActivated`
  (rule 6) every listing of the node is reconciled against the node view and its
  owner's view, so a `NODE_NOT_SELLABLE` listing whose owner is `SUSPENDED` becomes
  `SUPPRESSED(OWNER_SUSPENDED)` (one `ListingSuppressed`) instead of `LIVE`. On
  `SellerReinstated` (rule 5) every `OWNER_SUSPENDED` listing of the owner is
  reconciled against its node view, so a listing whose node is not sellable turns
  `NODE_NOT_SELLABLE` (one `ListingSuppressed`) instead of `LIVE`.
- **CreateListing (rule 7).** The REST command reads the owner's view; a
  `SUSPENDED` owner yields `ListingCreated` + `ListingSuppressed(OWNER_SUSPENDED)`
  at version 1 (A6), unless a higher cause applies, which then wins (A5).
- **Contract surfaces.** `OWNER_SUSPENDED` joins the `reason` enum of
  `apis/openapi.yaml` and the `ListingSuppressed.reason` of `apis/asyncapi.yaml`;
  the two consumed messages appear in `apis/asyncapi.yaml` with exactly the fields
  read; the consumed type list is in ADR 0003 and ADR 0004.

### Rule 8 is blocked on the producer payload

seller-inbound's published `InboundShipmentReceived` (`origin/develop`
`apis/asyncapi.yaml`, schema `InboundShipmentReceivedEvent`) carries
`{shipmentId, sellerId, nodeId, status, cartons: [{sscc, status}], receivedAt}`:
no `catalogItemId`, no GTIN and no eaches. The `(catalogItemId, nodeId, sellerId)`
triples with eaches > 0 that rule 8 iterates are not on the wire, so rule 8
cannot be met as written. Joining the received SSCCs with the contents of the
earlier `InboundShipmentLabelled` manifest would be a different contract (a second
local read model of manifests, and a manifest that can differ from what was
received), so it is not improvised here. Until seller-inbound publishes the
received lines (for example an optional `contents: [{catalogItemId?, gtin,
qtyEaches}]` per received carton, the shape its manifest `CartonContent` already
uses), `InboundShipmentReceived` is one of the ignored, counted types and G51 stays
open. When the producer adds the lines, rule 8 is built as written, in a new step
that amends this ADR.

## Consequences

- A suspended seller's listings leave the shelf within the consumer lag of
  seller-inbound; reinstating them restores exactly the listings no other cause
  holds.
- A listing shows one reason. A consumer of `ListingSuppressed` sees a second event
  for the same listing whenever the highest cause changes (owner -> node, node ->
  owner, any -> discontinued) and must keep the latest one, as it already does for
  A4's upgrade.
- The node and seller restore paths now read a second view per listing; the seller
  path reads one node row per listing of the owner. Both stay inside the one
  transaction of the event.
- The deployment needs a second consumer group (`SELLER_INBOUND_CONSUMER_GROUP`,
  chart value `config.sellerInboundConsumerGroup`): the chart refuses to render
  without it, and the topic `retailnetwork.seller-inbound.events` must exist.
- G51 stays open until seller-inbound publishes received lines.
