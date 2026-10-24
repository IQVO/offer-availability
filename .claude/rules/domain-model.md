# Domain model: ubiquitous language, aggregates, events, use cases

Source of truth: warehouse-docs `network/model/model.yaml` (context `offer-availability`).
Invariants, transitions and every assumption the model left open are in
`docs/adr/0002-aggregates-and-invariants.md` (assumptions A1-A24).

## Ubiquitous Language (use these exact names)

- **Catalogue item** (`CatalogItem`) — what the network can sell: title, length,
  width, height (mm), weight (g), `hazmat`, `fragile`. Identified by
  `catalogItemId` (`item-<uuid>`). `ACTIVE` or `DISCONTINUED`.
- **Listing** — the offer of ONE catalogue item on ONE node (unique per pair).
  Identified by `listingId` (`lst-<uuid>`). `LIVE` or `SUPPRESSED`, with a
  suppression reason `NODE_NOT_SELLABLE` or `DISCONTINUED`.
- **Availability** — the quantity a node claims it can ship, with the **basis** the
  vendor gave for it (`PHYSICAL`, `THROUGHPUT_CONSTRAINED`, `UNKNOWN`), the `asOf`
  instant it is true for and the `feedId` that set it. Kept as declared, never
  re-derived. Before the first feed: 0, `UNKNOWN`, no `asOf`.
- **Inventory feed** — one vendor submission for one node (`feedId` chosen by the
  caller), partial or full (`fullUpdate`). `RECEIVED`, `APPLIED`, `FAILED`,
  `SUPERSEDED`. It stores counters (applied, stale, zeroed), never its lines.
- **Stale line** — a line whose `asOf` is not after the listing's: skipped and
  counted, the feed is still `APPLIED`.
- **Node status view** (`NodeStatusView`) — the local copy of a node's status
  (`PROPOSED`, `ACTIVE`, `SUSPENDED`, `RETIRED`), fed by node-topology events. A
  listing is sellable only while its node is `ACTIVE`.

## Aggregates

- **CatalogItem** (`internal/domain/catalog`): positive dimensions and weight
  (`ErrInvalidItemDimensions`), title 1-200 characters; discontinuing twice is a
  no-op.
- **Listing** (`internal/domain/listing`): one per (item, node)
  (`ErrDuplicateListing`, enforced by the unique key); availability never negative
  (`ErrNegativeAvailability`) and monotonic in `asOf` (`ErrStaleAvailability`);
  `LIVE` only on a sellable node (`ErrNodeNotSellable`); a `DISCONTINUED` listing is
  never restored.
- **InventoryFeed** (`internal/domain/feed`): a line must name a listing of the
  feed's node (`ErrListingNotOnNode`); an `APPLIED` or `SUPERSEDED` feed id is never
  applied twice (`ErrFeedAlreadyApplied`), a `FAILED` one may be sent again; a feed
  older than the last applied feed of its node is `SUPERSEDED`; at most 10000 lines.
- `internal/domain/nodestatus`: the read model of node-topology; an event older
  than the stored status instant is ignored, `RETIRED` is final.
- `internal/domain/shared`: `Event`, `Header`, `ErrValidation`,
  `ErrIllegalTransition`, `ErrInvalidSnapshot`, id and text checks, `UTC`.

Every aggregate has `Snapshot()`, a validating `Restore(Snapshot)` and an
optimistic `Version()`; `now` and ids are passed in. `ErrNotFound` and
`ErrConcurrentUpdate` are port errors (`internal/application/ports/errors.go`),
not domain errors. The domain imports nothing outside `internal/domain`.

## Domain events

All seven are integration events on `retailnetwork.offer-availability.events`
(`internal/domain/*/events.go`; wire shape in `apis/asyncapi.yaml`):

- `CatalogItemCreated`, `CatalogItemDiscontinued`.
- `ListingCreated`, `ListingAvailabilityChanged` (one per applied or zeroed line),
  `ListingSuppressed`, `ListingRestored`.
- `InventoryFeedApplied` (after the availability events of the same feed).

## Key use cases (`internal/application/usecases`)

One per command and query; commands run in ONE `ports.UnitOfWork` (load, domain
call, save, outbox append) and retry once on `ErrConcurrentUpdate`. Three of them
span several aggregates in that one unit of work (ADR 0002, A11).

- `CreateCatalogItem`, `DiscontinueItem` (suppresses the item's listings)
  (`catalog.go`); `CreateListing` (`listings.go`); `ApplyInventoryFeed`
  (`feeds.go`); `ApplyNodeEvent` (`node_events.go`: inbox claim, status change,
  policy `SuppressOnNodeUnavailable` = `SuppressListing` / `RestoreListing`).
- `ApplyAvailability`, `SuppressListing`, `RestoreListing` are `Listing` methods
  run inside those use cases; they have no REST operation of their own.
- Queries (`queries.go`): `BrowseCatalog`, `GetCatalogItem`, `ListListings`,
  `GetInventoryFeed`. Read DTOs are in `internal/application/views`; filters in
  `internal/application/repository`.
- Ports (`internal/application/ports/ports.go`) are interfaces only; the memory
  test doubles in `internal/adapters/outbound/memory` are test doubles and must
  pass the same suite as Postgres (`internal/adapters/outbound/repocontract`).
