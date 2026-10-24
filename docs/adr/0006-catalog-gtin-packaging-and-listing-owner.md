# ADR 0006: Catalogue GTIN and packaging hierarchy, and the optional owner of a listing

## Status

Accepted (2026-10-24).

## Context

Sellers will ship inbound stock in cartons identified by GS1 GTINs, and the
network needs to tell a case of 24 from a case of 12 of the same item, and stock
owned by a seller from stock the operator owns. Two things were missing:

1. **No identifier, no pack levels.** A `CatalogItem` was title, dimensions,
   weight and two flags. The fleet's product-master already models the same thing
   (its ADR 0011): a packaging hierarchy of GS1 pack levels, with the content
   quantity of a GTIN immutable. Diverging would make every later integration
   (the seller inbound loop reads these events) translate between two models.
2. **One listing per (item, node).** A seller's stock of an item on a node could
   not be told from the operator's.

Both changes must be additive: a caller that never sends the new fields sees the
service exactly as before.

## Decision

### Identifier and packaging (mirrors product-master ADR 0011)

1. **`identifiers.gtin`** on a catalogue item is optional. It is validated with
   the template's `internal/domain/shared/gs1` value objects (installed with the
   template's `migrate_v3.py --with-gs1`, never hand-edited): 8, 12, 13 or 14
   digits, mod-10 check digit, normalised to 14 digits; a variable measure GTIN
   (indicator digit 9) is refused. Wrong GTIN is `invalid-gtin` (422).
2. **A GTIN identifies one thing, forever.** It may be held by one item (as its
   identifier) or by one pack level of one item, retired levels included. A
   second holder is `gtin-already-assigned` (409). The use case pre-checks
   (`CatalogRepository.FindByGTIN`); a partial unique index on
   `catalog_items.gtin` and a unique constraint on `catalog_item_pack_levels.gtin`
   are the backstop and map to the same error.
3. **Pack levels** are `EACH < INNER < CASE < PALLET`, each `{level, gtin,
   childGtin?, unitsPerPack, dimsMm?, grossWeightG?, active}` inside the
   `CatalogItem` aggregate (one transaction, one version per save). Quantities
   are in **eaches**: `unitsPerPack` is 1 for EACH, at least 2 above it, and a
   multiple of the child's, at least twice (`invalid-pack-quantity`, max
   1,000,000). A level above EACH names the `childGtin` of an **active, lower**
   level of the same item (`pack-level-order`); a gross weight below the weight of
   the contents, when the child's is known, is `invalid-pack-dimensions`.
   Availability stays in eaches: a pack level is a conversion plus an identity,
   never a second ledger.
4. **`unitsPerPack` is immutable per GTIN.** `PUT /catalog-items/{id}/packaging/{level}`
   with the GTIN of the ACTIVE level and another count or child is
   `pack-quantity-immutable` (409). The identical declaration changes nothing and
   raises nothing; new `dimsMm` / `grossWeightG` on an active GTIN are updated in
   place (and published again as `CatalogItemPackagingDeclared`). A **new GTIN**
   at a level that has an active one supersedes it: the old level is retired
   (`CatalogItemPackLevelRetired`) and the new one declared
   (`CatalogItemPackagingDeclared`) in ONE aggregate version, both events carrying
   it. A retired GTIN stays on the item (so a document naming it still resolves)
   and can never be declared again.
5. **`POST /catalog-items/{id}/packaging/{level}:retire`** (the repo's colon-verb
   style, like `:discontinue`) retires the active definition; retiring a level
   that only has history is a no-op, a level never declared is
   `pack-level-not-found` (404). A DISCONTINUED item takes no packaging change
   (`illegal-transition`). Both routes sit behind the Idempotency-Key middleware
   and answer 200 with the item; `GET /catalog` and `GET /catalog/{id}` carry
   `identifiers` and `packaging[]` (omitted when empty).
6. **Events** `CatalogItemPackagingDeclared {catalogItemId, level, gtin,
   childGtin?, unitsPerPack, declaredAt}` and `CatalogItemPackLevelRetired
   {catalogItemId, level, gtin, retiredAt}`, entity `catalogitem`, type
   `com.retailnetwork.commerce.offer-availability.catalogitem.<Event>` (this
   service's tier segment is `commerce`, ADR 0004), same topic, key and outbox.
   `CatalogItemCreated` gains an optional `gtin`. All additive: payloads that
   omit the new optional fields are unchanged, so existing consumers and goldens
   stay valid. ADR 0004 and `apis/asyncapi.yaml` list them.
7. **Persistence**: migration `0002` adds `catalog_items.gtin` and
   `catalog_item_pack_levels` (rewritten with the item inside its transaction,
   `seq` keeps declaration order, at most one active definition per item and
   level).

### Owner of a listing

8. `Listing` and `Availability` gain an optional **`ownerRef`** (the `sellerId`).
   Absent means operator-owned. There is **one listing per (catalogItemId, nodeId,
   ownerRef)**: `listings.owner_ref` is `NOT NULL DEFAULT ''` and the unique
   constraint becomes `(catalog_item_id, node_id, owner_ref)`, so every existing
   row keeps its meaning and the old uniqueness still holds for it. `POST
   /listings` accepts `ownerRef`; `ListingCreated` and
   `ListingAvailabilityChanged` carry it when set. A feed line names a `listingId`,
   so feeds, staleness, suppression and restoring need no change: the owner is a
   property of the listing they already address.

## Out of scope and follow-ups

- **order-sourcing seller-awareness.** Sourcing and the promise do not yet
  distinguish seller-owned from operator-owned availability when they read
  `ListingAvailabilityChanged`; they must be made seller-aware (use `ownerRef`)
  before sellers' stock is sold. Tracked as a follow-up in that service, not done
  here.
- Measured (versus declared) pack dimensions, which product-master models, are
  not mirrored: this context only keeps what the merchandiser declares.
- `pack-level` weights are checked against the child only when both are given.

## Consequences

- The seller inbound context can validate case contents against the pack levels it
  consumes from `CatalogItemPackagingDeclared` / `CatalogItemPackLevelRetired`.
- A changed count is a new GTIN and a new event pair, never a mutation: a
  consumer's local view can be keyed by GTIN and never needs a correction.
- Dropping the old `(item, node)` constraint is irreversible for data with two
  owners: the down migration fails if two listings share an item and node.
