---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 1
---

# Use cases

Every application use case of `internal/application/usecases`, in the order a context is used: catalogue, packaging,
listings, feeds, node events, then the reads. Each write runs in **one unit of work** (`transact` over `UnitOfWork`):
the aggregates it changes and the outbox rows for the events it raises commit together or not at all
([Architecture](/docs/overview/architecture#data-stores)). Source: `internal/application/usecases/*.go`; the HTTP binding
is `internal/adapters/inbound/http/handlers.go`; the routes are in `apis/openapi.yaml`.

Every state-changing REST route needs an `Idempotency-Key` header (at most 200 printable characters, stored 24 hours); the
same key with the same method, path and body replays the first answer with `Idempotency-Replayed: true`
([Troubleshooting](/docs/operations/troubleshooting#idempotency-in-practice)).

| Use case | Trigger | Success | Events raised |
| --- | --- | --- | --- |
| `CreateCatalogItem` | `POST /catalog-items` | `201` | `CatalogItemCreated` |
| `DiscontinueItem` | `POST /catalog-items/{catalogItemId}/discontinuations` | `200` | `CatalogItemDiscontinued`, then `ListingSuppressed` per listing |
| `DeclarePackLevel` | `PUT /catalog-items/{catalogItemId}/packaging/{level}` | `200` | `CatalogItemPackLevelRetired` (when it replaces an active level), `CatalogItemPackagingDeclared` |
| `RetirePackLevel` | `POST /catalog-items/{catalogItemId}/packaging/{level}/retirements` | `200` | `CatalogItemPackLevelRetired` |
| `CreateListing` | `POST /listings` | `201` | `ListingCreated`, and `ListingSuppressed` when it starts suppressed |
| `ApplyInventoryFeed` | `POST /inventory-feeds` | `201`, or `422` for a recorded `FAILED` feed | `ListingAvailabilityChanged` per changed listing, then `InventoryFeedApplied` |
| `ApplyNodeEvent` | node-topology event on Kafka | offset committed | `ListingSuppressed` or `ListingRestored` per affected listing |
| `BrowseCatalog` | `GET /catalog`, MCP `browse_catalog` | `200` | none |
| `GetCatalogItem` | `GET /catalog/{catalogItemId}`, MCP `get_catalog_item`, A2A `check_availability` | `200` | none |
| `ListListings` | `GET /listings`, MCP `list_listings`, A2A `check_availability` | `200` | none |
| `GetInventoryFeed` | `GET /inventory-feeds/{feedId}`, MCP `get_inventory_feed`, A2A `feed_outcome` | `200` | none |

## CreateCatalogItem

- **Input:** `title`, `lengthMm`, `widthMm`, `heightMm`, `weightG` (all positive), `hazmat`, `fragile`, optional
  `identifiers.gtin` (raw as sent).
- **Flow:** parse the GTIN when present (8, 12, 13 or 14 digits with a valid mod-10 check digit, normalised to 14;
  variable-measure indicator 9 refused), refuse a GTIN another item or pack level already holds, mint the id
  (`item-<uuid>`), create the item `ACTIVE`, save, publish.
- **Invariants:** dimensions and weight above zero; a GTIN identifies one thing forever.
- **Errors:** `invalid-gtin` (422), `gtin-already-assigned` (409), `invalid-item-dimensions` (422), `validation-failed` (422).

## DiscontinueItem

- **Input:** the catalogue item id.
- **Flow:** load (unknown is `not-found`), discontinue. A `DISCONTINUED` item is returned unchanged with **no event**.
  Otherwise, in the same unit of work, every listing of the item that can legally be suppressed is suppressed with reason
  `DISCONTINUED` (a listing already suppressed for `NODE_NOT_SELLABLE` changes its reason; one already `DISCONTINUED` is
  skipped), then the events leave in the order `CatalogItemDiscontinued`, `ListingSuppressed` by listing id.
- **Invariant:** discontinuation is final, and so is the `DISCONTINUED` suppression: a node event never restores it.

## DeclarePackLevel and RetirePackLevel

- **Input (declare):** `level` (`EACH`, `INNER`, `CASE`, `PALLET`, in the path), `gtin`, `childGtin`, `unitsPerPack`,
  optional `dimsMm` (all three or none) and `grossWeightG`.
- **Rules (ADR 0006):** an `EACH` level has exactly one unit per pack and no child; every higher level needs the
  `childGtin` of an **active lower level of the same item**, `unitsPerPack` a multiple of the child's and at least twice
  it, and a gross weight no lighter than its contents; `unitsPerPack` is at most 1000000. A GTIN the item already holds can
  only be re-declared at its own active level with the same `unitsPerPack` and child (dimensions and weight may change
  in place); the identical declaration returns the item unchanged with no event.
- **Replacing:** a **new** GTIN at a level that has an active one retires the old level and declares the new one in one
  aggregate version, so a retired level stays resolvable (`active: false`).
- **Retire:** retires the active definition at the level; a level with only retired history is returned unchanged with no
  event; a level never declared is `pack-level-not-found` (404).
- **A `DISCONTINUED` item** takes no packaging change (`illegal-transition`, 409).
- **Errors:** `invalid-pack-level`, `invalid-gtin`, `invalid-pack-quantity`, `invalid-pack-dimensions`, `pack-level-order`
  (all 422), `pack-quantity-immutable`, `gtin-already-assigned`, `illegal-transition` (409), `not-found`,
  `pack-level-not-found` (404).

## CreateListing

- **Input:** `catalogItemId`, `nodeId`, optional `ownerRef` (empty = operator-owned).
- **Flow:** both ids and the owner must be well formed; the item must exist (`not-found`); the (item, node, owner) pair
  must be free (`duplicate-listing`); the node's sellability comes from the **local `node_status_view`**, and an unknown
  node is **not sellable**. The listing starts `LIVE` with availability `0` / basis `UNKNOWN` only when the node is
  `ACTIVE` and the item is not discontinued; otherwise it starts `SUPPRESSED` with reason `DISCONTINUED` (item) or
  `NODE_NOT_SELLABLE` (node) and raises `ListingSuppressed` right after `ListingCreated`.
- **Errors:** `not-found` (404), `duplicate-listing` (409), `validation-failed` (422).

## ApplyInventoryFeed

- **Input:** `feedId`, `nodeId`, `fullUpdate`, `asOf`, `lines` of `listingId`, `availableQty`, `basis`
  (`PHYSICAL`, `THROUGHPUT_CONSTRAINED`, `UNKNOWN`).
- **Request validation (nothing recorded):** valid ids, `asOf` present, at most 10000 lines, a partial feed has at least
  one line, no listing twice, a known `basis`, quantity at most 2147483647: otherwise `validation-failed` (422).
- **Flow:** under the node's mark lock (`node_feed_marks`, which serialises the feeds of a node), record the feed
  `RECEIVED` (a new feed, or a `FAILED` one sent again), then settle it:
  - an `APPLIED` or `SUPERSEDED` `feedId` is `feed-already-applied` (409);
  - a feed with `asOf` strictly before the node's last `APPLIED` feed is `SUPERSEDED`: stored, nothing else changes, `201`;
  - a line naming no listing of the node (`listing-not-on-node`) or a negative quantity (`negative-availability`) makes the
    feed `FAILED`: the feed row **is kept** (the middleware commits the writes of this 422), nothing else changes, and the
    answer is `422` with the problem body;
  - otherwise each line is applied (a line not newer than the listing's current `asOf` is skipped and counted in
    `staleCount`), a **full** feed sets every other listing of the node to zero keeping its basis (`zeroedCount`), the
    changed listings are saved in bulk, the node's mark advances and the feed becomes `APPLIED` (`201`).
- **Events:** one `ListingAvailabilityChanged` per changed listing, then `InventoryFeedApplied`.
- **Metric:** every outcome increments `availability.inventory_feeds.applied` ([Observability](/docs/operations/observability#metrics)).
- **State machine:** `RECEIVED` -> `APPLIED` \| `SUPERSEDED` \| `FAILED`; `FAILED` -> `RECEIVED` again on a re-send of the same id.

## ApplyNodeEvent

- **Trigger:** a CloudEvent on `retailnetwork.node-topology.events` of type `NodeRegistered`, `NodeActivated`,
  `NodeSuspended` or `NodeRetired` (the consumer decodes; other types are skipped).
- **Flow, in one unit of work with the inbox row:** a CloudEvents id already in `processed_events` is skipped.
  `NodeRegistered` creates the view as `PROPOSED` when the node is unknown and is otherwise a no-op. The other three go
  through `View.Observe`: an observation older than the current status, or any observation on a `RETIRED` node, is
  `Ignored` and touches nothing. A `Changed` or `Repeated` observation runs the policy `SuppressOnNodeUnavailable`:
  `ACTIVE` restores the listings of the node suppressed for `NODE_NOT_SELLABLE`; `SUSPENDED` and `RETIRED` suppress every
  `LIVE` listing with that reason. Listings for which the move is illegal are skipped, so a replay is a no-op.
- **Errors:** a malformed event (`validation-failed`) is dead-lettered at once; a transient failure rolls back, is retried
  five times and then dead-lettered ([Runbook](/docs/operations/runbook#the-consumer-and-the-dead-letter-topic)).

## Reads

| Use case | Inputs | Result |
| --- | --- | --- |
| `BrowseCatalog` | `limit` 1..500 (default 100), `cursor` | Items by id ascending, with `nextCursor` when another page exists |
| `GetCatalogItem` | item id | The item with `identifiers` and `packaging`, or `not-found` |
| `ListListings` | optional `nodeId`, `catalogItemId` (combine with AND), `limit`, `cursor` | Listings by id ascending with `nextCursor` |
| `GetInventoryFeed` | feed id | The feed and its outcome (state, counters, `failureCode`), or `not-found` |

A `limit` that is not an integer from 1 to 500 is 400 `invalid-limit`, a bad `cursor` is 400 `invalid-cursor` and an undeclared query parameter is 400 `unknown-query-parameter`. The cursor is the last returned id.

## Errors and the problem types they become

The mapping lives in `internal/adapters/inbound/http/problems.go` (`problemCatalogue`); the full table with remedies is on
[Troubleshooting](/docs/operations/troubleshooting#rest-problem-types).

| Domain or application error | Problem `type` | Status |
| --- | --- | --- |
| `ports.ErrNotFound` | `not-found` | 404 |
| `shared.ErrIllegalTransition` | `illegal-transition` | 409 |
| `ports.ErrConcurrentUpdate` | `concurrent-update` | 409 |
| `catalog.ErrInvalidGTIN` | `invalid-gtin` | 422 |
| `catalog.ErrGtinAlreadyAssigned` | `gtin-already-assigned` | 409 |
| `catalog.ErrPackQuantityImmutable` | `pack-quantity-immutable` | 409 |
| `catalog.ErrPackLevelOrder` | `pack-level-order` | 422 |
| `catalog.ErrInvalidPackQuantity`, `ErrInvalidPackDimensions`, `ErrInvalidPackLevel`, `ErrInvalidItemDimensions` | `invalid-pack-quantity`, `invalid-pack-dimensions`, `invalid-pack-level`, `invalid-item-dimensions` | 422 |
| `catalog.ErrPackLevelNotFound` | `pack-level-not-found` | 404 |
| `listing.ErrDuplicateListing` | `duplicate-listing` | 409 |
| `listing.ErrNegativeAvailability` | `negative-availability` | 422 |
| `listing.ErrStaleAvailability`, `ErrNodeNotSellable` | `stale-availability`, `node-not-sellable` | 409 |
| `feed.ErrListingNotOnNode` | `listing-not-on-node` | 422 |
| `feed.ErrFeedAlreadyApplied` | `feed-already-applied` | 409 |
| `shared.ErrValidation` | `validation-failed` | 422 |
| anything else | `internal-error` | 500 |
