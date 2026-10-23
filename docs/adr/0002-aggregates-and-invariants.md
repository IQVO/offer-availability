# ADR 0002: the CatalogItem, Listing and InventoryFeed aggregates, the NodeStatusView, their invariants and the assumptions the model left open

## Status

Accepted (2026-10-23). Design source: retail-network-docs `model/model.yaml`,
context `offer-availability`, the pack pages (`aggregate-design-canvas`,
`use-cases`, `domain-events`, `bounded-context-canvas`) and the Model Gap Register
items G23 and G24, whose "Proposed resolution" is final (`contracts-delta.md`).

## Context

The model gives three aggregate roots, each its own transaction boundary:
`CatalogItem`, `Listing` and `InventoryFeed`. One command, `ApplyInventoryFeed`,
changes many `Listing` aggregates and one `InventoryFeed` together, and
`DiscontinueItem` changes a `CatalogItem` and every `Listing` of the item. The
model's phrase "applied atomically across every listing" therefore cannot mean
one aggregate per transaction (G23): the **unit of work** is the transaction
boundary and spans several aggregates (A11). The model is silent on a number of
details; every one the implementation had to decide is an **assumption** below,
flagged `A<n>`, and is also reported to the orchestrator as a model gap. The
model is not edited here.

## Decision

### Layout and conventions

- `internal/domain/{shared,catalog,listing,feed,nodestatus}`. Pure Go: no I/O,
  `time.Now` and id generation are never called; `now` and ids are passed in.
  Dependency direction: `listing` <- `feed`; `catalog`, `listing`, `feed` and
  `nodestatus` use `shared`.
- `shared` holds `ErrValidation` (problem `validation-failed`, 422),
  `ErrIllegalTransition` (problem `illegal-transition`, 409), `ErrInvalidSnapshot`
  (a persisted row that breaks an invariant: an internal error, never a client
  error), the `Event` interface, the `Header` and the identifier checks.
  `ErrNotFound` is NOT a domain error: it is the port sentinel `ports.ErrNotFound`.
  `ErrConcurrentUpdate` is raised by the repository on a version mismatch.
- Each aggregate has a constructor or command method per model command, a
  `Snapshot()` and a validating `Restore(Snapshot)`, and a `Version()` for
  optimistic concurrency. The version counts **saves**: a command that raises
  several events (A6) gives them all the same version. The repository stores with
  the expectation `stored version == Version()-1` (`0` for a create). Events are
  typed structs whose fields are exactly the model's `data` list (ADR 0004).
- Bounds that exist because of Postgres `integer` are plain literal constants
  (`const MaxInt32 = 2147483647`).

### CatalogItem (`internal/domain/catalog`)

States `ACTIVE`, `DISCONTINUED`; transition `ACTIVE -> DISCONTINUED`
(DiscontinueItem). `DISCONTINUED` is terminal.

| Invariant | Err | Enforced by |
| --- | --- | --- |
| title, length, width, height and weight are valid; dimensions and weight are positive | `ErrInvalidItemDimensions` (422 `invalid-item-dimensions`) | `catalog.Create` |

Check order in `Create`: id, title (`ErrValidation`), then dimensions and weight
(`ErrInvalidItemDimensions`). Discontinuing a `DISCONTINUED` item is a no-op that
raises no event (A3).

### Listing (`internal/domain/listing`)

States `LIVE`, `SUPPRESSED`; a suppressed listing carries a reason
(`NODE_NOT_SELLABLE`, `DISCONTINUED`). Transitions: `LIVE -> SUPPRESSED`
(SuppressListing), `SUPPRESSED -> LIVE` (RestoreListing). `ApplyAvailability`
replaces the `Availability{availableQty, basis, asOf, feedId}` value object and
does not change the state.

| Invariant | Err | Enforced by |
| --- | --- | --- |
| `availableQty >= 0` | `ErrNegativeAvailability` (422 `negative-availability`) | `Listing.ApplyAvailability` |
| an update with `asOf <=` the current `asOf` is ignored (monotonic per listing) | `ErrStaleAvailability` (never an HTTP answer; slug `stale-availability` is reserved) | `Listing.ApplyAvailability`; the caller skips the line |
| a listing on a non-ACTIVE node is SUPPRESSED | `ErrNodeNotSellable` (409 `node-not-sellable`) | `listing.Create` (starts SUPPRESSED, A5) and `Listing.Restore` (refuses while the node is not sellable) |
| one listing per (catalogItemId, nodeId) | `ErrDuplicateListing` (409 `duplicate-listing`) | the use case pre-check and the unique index `(catalog_item_id, node_id)` |

`Restore` check order: the listing is `SUPPRESSED` (else `ErrIllegalTransition`);
its reason is not `DISCONTINUED` (else `ErrIllegalTransition`, final); the node is
sellable (else `ErrNodeNotSellable`). `Suppress` from `LIVE` suppresses with the
given reason; from `SUPPRESSED(NODE_NOT_SELLABLE)` with `DISCONTINUED` it upgrades
the reason and raises `ListingSuppressed` again (A4); any other `Suppress` of a
`SUPPRESSED` listing is `ErrIllegalTransition`. The policy use cases skip listings
for which the command would be illegal, so a replay raises no event.

### InventoryFeed (`internal/domain/feed`)

States `RECEIVED`, `APPLIED`, `FAILED`, `SUPERSEDED` (G23 adds `SUPERSEDED`).
Transitions: `RECEIVED -> APPLIED`, `RECEIVED -> FAILED`, `RECEIVED -> SUPERSEDED`
(all ApplyInventoryFeed) and `FAILED -> RECEIVED` (the same `feedId` sent again).
`APPLIED` and `SUPERSEDED` are terminal.

| Invariant | Err | Enforced by |
| --- | --- | --- |
| every line names a listing of the feed's node | `ErrListingNotOnNode` (422 `listing-not-on-node`) | `InventoryFeed.Apply` (feed becomes `FAILED`) |
| a feed is applied at most once | `ErrFeedAlreadyApplied` (409 `feed-already-applied`) | `InventoryFeed.Resubmit` and the use case (an `APPLIED` or `SUPERSEDED` id) |
| a quantity is not negative | `ErrNegativeAvailability` | `InventoryFeed.Apply` (feed becomes `FAILED`) |
| at most 10000 lines, at most one per listing, valid basis, quantity at most 2147483647 | `ErrValidation` (422 `validation-failed`) | `feed.NewSubmission` (nothing is recorded) |

`InventoryFeed.Apply` validates every line first (listing on the node, quantity),
then applies; it returns the first offending line in line order. It returns the
changed listings and their `ListingAvailabilityChanged` events and the counters.
`feed.Superseded(asOf, lastApplied)` is the node-level rule of G23.

### NodeStatusView (`internal/domain/nodestatus`)

A local read model, not an aggregate of the model, with its own optimistic
version: per node a status (`PROPOSED`, `ACTIVE`, `SUSPENDED`, `RETIRED`) and the
instant of that status. `Observe(status, at)` folds one node-topology event; it is
a pure function so the ordering rules (A13) are mutation-tested.

### Assumptions

- **A1 Ids.** `catalogItemId` and `listingId` are minted by the service as
  `item-<uuid v4>` and `lst-<uuid v4>`; `feedId` is chosen by the caller
  (vendor-connectivity). All ids and `nodeId` are opaque: 1 to 64 characters from
  A-Z a-z 0-9 . _ : -, starting with a letter or a digit (`shared.ValidID`).
- **A2 Bounds.** `title`: 1 to 200 characters, trimmed, no control characters.
  `lengthMm`, `widthMm`, `heightMm`, `weightG`: 1 to 2147483647 (both a
  non-positive value and one above the bound are `ErrInvalidItemDimensions`).
  `availableQty`: 0 to 2147483647. A feed has at most 10000 lines (G23).
- **A3 Discontinue twice.** `DiscontinueItem` on a `DISCONTINUED` item answers 200
  with the item unchanged and raises nothing (the pack proposes it; `ErrIllegalTransition`
  is not used for it).
- **A4 Discontinuing suppresses the listings (G24).** In the same transaction every
  listing of the item that is not already `SUPPRESSED(DISCONTINUED)` is suppressed
  with reason `DISCONTINUED`, one `ListingSuppressed` each. A listing that was
  suppressed for `NODE_NOT_SELLABLE` changes reason and raises the event again, so
  that a later `NodeActivated` can never restore it. `DISCONTINUED` is final. A
  listing created for a `DISCONTINUED` item starts `SUPPRESSED(DISCONTINUED)`.
  Creating a listing for an unknown `catalogItemId` is `ErrNotFound` (404).
- **A5 Initial state (G24).** A listing starts `LIVE` only when the local
  `NodeStatusView` says the node is `ACTIVE`. For any other status, and for a node
  the view does not know yet, it starts `SUPPRESSED` with reason
  `NODE_NOT_SELLABLE`: an unknown node is not known to be sellable, and the first
  `NodeActivated` restores the listing, so event lag between node-topology and
  this context never needs a retry from merchandising. `DISCONTINUED` wins over
  `NODE_NOT_SELLABLE` when both apply.
- **A6 Created and suppressed.** `ListingCreated` does not carry the state (the
  model's field list is final), so a listing that starts `SUPPRESSED` raises
  `ListingCreated` and then `ListingSuppressed` in the same transaction, both with
  aggregate version 1.
- **A7 Availability before the first feed.** `availableQty 0`, basis `UNKNOWN`, no
  `asOf`, no `feedId` (omitted on the wire).
- **A8 Suppressed listings take availability.** `ApplyAvailability` ignores the
  state: a `SUPPRESSED` listing still takes feed lines and raises
  `ListingAvailabilityChanged`; the state travels in `ListingSuppressed` and
  `ListingRestored`.
- **A9 Applying a line.** Check order: basis valid and `asOf` set (`ErrValidation`),
  quantity (`ErrNegativeAvailability`, a negative number fails the feed even on a
  stale line), then staleness (`asOf <=` current is `ErrStaleAvailability`; an
  equal `asOf` is stale). An applied line always replaces the value object and
  raises `ListingAvailabilityChanged`, even when quantity and basis are unchanged,
  because `asOf` and `feedId` changed and consumers keep the highest `asOf`.
- **A10 FullFeedZeroing (G24).** A full feed sets `availableQty 0` on every listing
  of the node that has no line, keeping the listing's basis and stamping the
  feed's `asOf` and `feedId`. A listing already at 0 is left untouched (no state
  change, no event), and a listing whose `asOf` is not before the feed's is left
  untouched too; neither is counted as a stale line. `SUPPRESSED` listings are
  zeroed as well.
- **A11 Feed outcomes (G23).** The feed is one transaction: it saves the feed row
  as `RECEIVED`, evaluates, and ends `APPLIED`, `FAILED` or `SUPERSEDED`. A stale
  line is skipped and counted (`staleCount`); a feed whose lines are all stale is
  still `APPLIED`. A feed whose `asOf` is strictly older than the `asOf` of the
  last `APPLIED` feed of that node is `SUPERSEDED` (nothing applied, no event); the
  per-node high-water mark lives in `node_feed_marks` and only an `APPLIED` feed
  advances it. A `FAILED` feed (`ErrListingNotOnNode` or `ErrNegativeAvailability`)
  changes no listing and raises no event; its row stays with `failureCode` and the
  request is answered 422 with `instance` `/inventory-feeds/{feedId}`. A `FAILED`
  `feedId` may be sent again (`FAILED -> RECEIVED`), it must keep its `nodeId`
  (else `ErrValidation`); an `APPLIED` or `SUPERSEDED` id is refused with
  `ErrFeedAlreadyApplied`. Feeds are retained 30 days (the model); they store
  counters, not lines. Two feeds of one node are serialised by locking the node's
  mark row.
- **A12 Feed shape.** The model does not define the lines: a line is
  `{listingId, availableQty, basis}` and `asOf` is feed-level (the pack's
  proposal). A partial feed needs at least one line; a full feed may have none (it
  zeroes the node). Two lines for one listing are `ErrValidation`.
- **A13 NodeStatusView ordering.** A node event carries its own instant
  (`activatedAt`, `suspendedAt`, `retiredAt`; the CloudEvents `time` for
  `NodeRegistered`). An event older than the instant of the current status is
  ignored whole (no listing is touched). `RETIRED` is terminal. An event for an
  unknown node creates the row (a lost `NodeRegistered` must not block
  `NodeActivated`). `NodeRegistered` for a known node is a no-op. An event that
  repeats the current status is not ignored: the policy runs again and finds
  nothing to change.
- **A14 SuppressOnNodeUnavailable.** On `NodeSuspended` or `NodeRetired` every
  `LIVE` listing of the node is suppressed with `NODE_NOT_SELLABLE`. On
  `NodeActivated` every listing of the node suppressed for `NODE_NOT_SELLABLE` is
  restored; listings suppressed for `DISCONTINUED` are left alone. A listing for
  which the command would be illegal or refused (`ErrIllegalTransition`,
  `ErrNodeNotSellable`) is skipped silently, which makes a replay a no-op.
- **A15 Concurrency.** Every save is optimistic. A command use case runs its whole
  attempt again once on `ErrConcurrentUpdate` before it reports 409
  `concurrent-update`; the Kafka consumer relies on the same single retry and then
  on redelivery (the transaction, including the inbox row, rolled back).
- **A16 Pagination.** `GET /catalog` and `GET /listings` are keyset paginated by
  id ascending (`limit` 1 to 500, default 100; `after` is the previous page's
  `nextCursor`, which is the last id of the page). An invalid `limit` is
  `ErrValidation`. The model has no pagination; the catalogue is about 10^6 items.
- **A17 Wire types.** The producer's choices inside the wire conventions of
  `contracts-delta.md`: ids and enums are strings, quantities and dimensions are
  integers, instants are RFC 3339 UTC strings, absent optionals are omitted. They
  are documented in `apis/asyncapi.yaml` and ADR 0004.
- **A18 Problem slugs.** One slug per model `Err` name (kebab-case without `Err`):
  `invalid-item-dimensions`, `negative-availability`, `stale-availability`
  (reserved), `node-not-sellable`, `duplicate-listing`, `listing-not-on-node`,
  `feed-already-applied`; plus the shared `not-found`, `illegal-transition`,
  `concurrent-update` (the pack's `concurrent-modification` is superseded by the
  model conventions), `validation-failed`.
- **A19 Retention.** Idempotency keys 24 hours (model conventions); inventory
  feeds and processed (inbox) events 30 days. A background janitor in `cmd/api`
  deletes older rows.
- **A20 No tombstones.** Nothing is deleted from the domain: a node's listings
  are suppressed, never removed; a discontinued item stays in the catalogue.

## Consequences

- Each invariant has a named error, a problem slug (A18) and a domain test; the
  `Restore` boundary tests pin the persisted-row checks.
- `ApplyInventoryFeed` is the only multi-aggregate write besides `DiscontinueItem`
  and the node policies; all three run in one `UnitOfWork`, with bulk listing
  saves, so a 10000-line feed is one transaction.
- The unresolved model questions are reported as `model_gaps`: A3, A5, A6, A8,
  A9, A10, A12, A13, A16.
## Amendment 2026-10-23: assumptions found while building the adapters

- **A21 Instant precision.** Every instant the domain keeps (`asOf`, node status
  instants, event times) is UTC at microsecond precision (`shared.UTC` truncates).
  That is what a Postgres `timestamptz` stores, so the instant the domain compared
  is the instant the database returns, and "newer than" cannot flip across a round
  trip. Two instants less than a microsecond apart are the same instant.
- **A22 A FAILED feed survives the Idempotency-Key savepoint.** `POST
  /inventory-feeds` answers 422 for a FAILED feed, and the middleware drops the
  writes of a 4xx. The FAILED row is a recorded fact (A11) that `GET
  /inventory-feeds/{feedId}` must show, so that one handler asks the middleware to
  commit it (`keepWrites`); every other 4xx still leaves nothing behind. The 422
  carries the instance `/inventory-feeds/{feedId}`. See ADR 0005.
- **A23 A node event that can never be applied is dead-lettered.** An event of a
  consumed type whose payload lacks `nodeId` or its instant, or whose values the
  use case refuses (`ErrValidation`), goes to `retailnetwork.node-topology.events.dlq`
  at once; a transient failure is retried five times first. A message that is not a
  CloudEvent, or a type this service does not consume, is skipped. See ADR 0005.
- **A24 Zeroing a listing already at 0.** Part of A10, restated because a client
  can trip over it: a full feed that omits a listing never fed (0, `UNKNOWN`, no
  `asOf`) leaves it untouched, and `zeroedCount` does not count it.
