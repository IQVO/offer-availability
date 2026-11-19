# ADR 0016: Every problem type has an idempotency replay class

## Status

Accepted (2026-11-19). Supersedes in part ADR 0005 decision 4 (the FAILED feed's 422 is no longer always stored and
replayed), the idempotency bullet of ADR 0011 ("a 4xx is stored") and ADR 0002 A22 (the `keepWrites` call). The
bodies of those ADRs are unchanged; each carries a dated pointer here.

## Context

The fleet decided on 2026-11-18 that a request retried under the same `Idempotency-Key` is answered from the store only
when its first answer is final for that request (standard: `.claude/rules/fleet/idempotency-and-outbox.md` and
`docs/idempotency-replay-classes.md` of warehouse-harness-template).

Before this ADR the store of this service (`internal/adapters/inbound/http/idempotency.go`) kept the key row in the
business transaction, ran the handler in a savepoint and stored EVERY 4xx (`a 4xx -> savepoint rolled back, answer
stored`). A refusal that a retry could pass was therefore replayed for 24 hours: a `409 concurrent-update` to a client
that did the right thing and retried, a `404 pack-level-not-found` for a level a `PUT` declared a second later, a
`422 listing-not-on-node` for a listing the event consumers created a second later. The only exception was the
per-handler `keepWrites` flag (ADR 0005 decision 4, ADR 0002 A22): `POST /inventory-feeds` raised it for a FAILED feed so
the middleware committed the savepoint (the FAILED feed row) and still stored the 422.

Deterministic-key caller: vendor-connectivity calls `POST /inventory-feeds` with key = feedId = submission id.

## Decision

1. **Every problem type has exactly one class**, declared on its entry of `problemCatalogue` (or of
   `standaloneProblems` for the types written without a catalogue error) in
   `internal/adapters/inbound/http/problems.go`, next to the problem:
   - `FINAL`: the answer is a function of the request (or of state that only moves one way). Stored and replayed; the
     handler's writes roll back (the previous behaviour for every 4xx).
   - `OUTCOME`: the command committed state and then refused; the refusal must be remembered. Stored AND the writes commit
     with the stored answer. This is the former `keepWrites` mechanism, now carried by the class.
   - `RETRYABLE`: nothing is remembered under the key and a retry executes again. The key is released exactly as for a 5xx:
     the whole transaction, key row included, rolls back.
2. A 2xx is stored. A 5xx and a panic are never stored. The middleware reads the class from the capturing writer
   (`writeError` calls `MarkReplay`), holds no list of slugs and no status range beyond 2xx and 5xx. A refusal that carries no
   class (a handler writing a problem by hand) is released like RETRYABLE: the safe fallback, never a stored answer.
3. A problem type without a class fails `TestEveryProblemTypeDeclaresAReplayClass` (the template test) and
   `TestEveryStandaloneProblemDeclaresAReplayClass`; `TestTheADRTableEqualsTheCatalogue` holds the table below equal to the
   catalogue, slug by slug.
4. Releasing a RETRYABLE key means the request is forgotten: the same body runs again and **a different body under the same
   key is a new request**, not `409 idempotency-key-reused`.
5. **A concurrent duplicate** waits on the key row (the `INSERT ... ON CONFLICT` blocks until the first transaction
   resolves). When the first ends RETRYABLE, its transaction rolled back, so the duplicate's insert succeeds and the
   duplicate executes the work itself: it is neither answered `idempotency-key-reused` nor replayed
   (`TestConcurrentDuplicateOfAReleasedRefusalExecutesTheWork`). When the first ends FINAL or OUTCOME or 2xx, the duplicate
   replays it (unchanged).
6. **The FAILED feed (judgement call 1).** `POST /inventory-feeds` saves a FAILED feed before refusing (usecases/feeds.go
   97-101: `settle(ctx, f, f.MarkFailed(err))`, `out.Failure = err`) and ADR 0002 A11 says `GET /inventory-feeds/{feedId}`
   must show it. Its two causes get different classes:
   - `negative-availability` is **OUTCOME**. The quantity is in the request, `validateLines` (feed_apply.go 79-90) fails the
     same way every time, and the FAILED row commits with the stored 422; a replay answers from the key row and records
     nothing new (`TestNegativeAvailabilityOutcomeCommitsOnceAndReplays`).
   - `listing-not-on-node` is **RETRYABLE with a record** (the entry's `record` flag): the listing is event-fed
     (ADR 0013, 0014), so the same request can pass a second later, and `receive` (feeds.go 108-128) lets the same feed id be
     resubmitted when it is FAILED (`feed.Resubmit`, feed.go 176-191), so a re-execution replaces the record instead of
     recording twice. The middleware commits the handler's writes (the FAILED feed, so `GET` still shows it), deletes the key
     row in the same transaction and stores no answer. The retry under the same key re-evaluates the feed and, once the
     listing exists, turns it APPLIED (`TestListingNotOnNodeRefusalKeepsTheFailedFeedAndLetsTheSameKeyRetry`); a retry that
     still fails leaves one FAILED row (`TestListingNotOnNodeRetryThatStillFailsLeavesOneFailedFeed`). This is the only
     RETRYABLE entry that commits anything, it is pinned by `TestOnlyTheListingNotOnNodeEntryKeepsARecordWhileFreeingTheKey`,
     and it needs one extra branch in the middleware (`settleRecord`). If the owner prefers the plain OUTCOME for it, the change is
     the entry's class and the three tests named above; the cost is that vendor-connectivity (key = feedId) would be
     answered the stored 422 for 24 hours after the listing arrives.

### The class of each problem type

The reasons cite the code at the time of this ADR. "Keyed" means reachable from a route behind the Idempotency-Key
middleware (`POST /catalog-items`, `.../discontinuations`, `.../packaging/{level}/retirements`, `POST /listings`,
`POST /inventory-feeds`); `PUT .../packaging/{level}` takes no key (ADR 0011), so its problem types have no runtime effect
today and are classified for when they are.

| Problem type | Status | Class | Reason |
| --- | --- | --- | --- |
| `malformed-request-body` | 400 | FINAL | bad JSON, an unknown field or a NUL: response.go `decodeJSON`, nul.go; the request alone |
| `malformed-request-query` | 400 | FINAL | a NUL in the query (nul.go); the request alone, answered before any key |
| `request-body-too-large` | 413 | FINAL | the body size (response.go `decodeJSON`, idempotency.go `ServeHTTP`); the request alone |
| `invalid-limit` | 400 | FINAL | pagination.go; a GET parameter, the request alone |
| `invalid-cursor` | 400 | FINAL | pagination.go; a GET parameter, the request alone |
| `unknown-query-parameter` | 400 | FINAL | pagination.go; a GET parameter, the request alone |
| `idempotency-key-required` | 400 | FINAL | idempotency.go `keyFrom`; answered before the key is claimed, never stored |
| `idempotency-key-invalid` | 400 | FINAL | idempotency.go `keyFrom`; answered before the key is claimed, never stored |
| `idempotency-key-reused` | 409 | RETRYABLE | answered by the middleware itself, never stored; it depends on a stored row that expires after 24 h or is released |
| `invalid-gtin` | 422 | FINAL | the GTIN and its check digit are in the request (catalog.ParseGTIN) |
| `invalid-item-dimensions` | 422 | FINAL | dimensions and weight are in the request (`POST /catalog-items`) |
| `invalid-pack-level` | 422 | FINAL | the level is a path segment (packaging.go 165, `RetirePackLevel` is keyed) |
| `gtin-already-assigned` | 409 | FINAL | a GTIN identifies one thing forever, retired pack levels included (usecases/catalog.go 129-145 `gtinFree`, packaging.go `redeclare`) and nothing is deleted: the refusal never lifts |
| `pack-quantity-immutable` | 409 | FINAL | an active level never changes its quantity and a retired one keeps its GTIN (packaging.go `redeclare`); PUT only |
| `duplicate-listing` | 409 | FINAL | no listing is ever deleted (retention.go 42-44 purges keys, feeds and inbox rows only), so the (item, node, owner) pair stays taken (usecases/listings.go 50-52, 78-86); keyed |
| `stale-availability` | 409 | FINAL | a listing's asOf only moves forward (listing.go 223-237), so a stale line stays stale; the feed counts it instead of failing (feed_apply.go 51-55); no keyed route answers it |
| `feed-already-applied` | 409 | FINAL | APPLIED and SUPERSEDED are terminal (feed.go 176-179); keyed. vendor-connectivity already reads it as success |
| `negative-availability` | 422 | OUTCOME | the feed is saved FAILED (feeds.go 97-101, 130-139) and the 422 carries the recorded feed as instance (handlers.go `applyInventoryFeed`); the quantity is in the request (feed_apply.go 85-87). Judgement call 1 |
| `listing-not-on-node` | 422 | RETRYABLE (record) | the feed is saved FAILED first (feeds.go 97-101) and stays visible, but the listing is event-fed (feed_apply.go 81-84) and the retry resubmits the same feed id (feeds.go 108-128, feed.go 176-191). Judgement call 1 |
| `concurrent-update` | 409 | RETRYABLE | the optimistic lock lost (postgres unit_of_work.go 108-118); nothing committed, the retry re-reads and may win; keyed on every route |
| `pack-level-not-found` | 404 | RETRYABLE | packaging.go 301-313 (`RetirePackLevel`, keyed); a `PUT` can declare the level a moment later |
| `illegal-transition` | 409 | RETRYABLE | the aggregate has not reached the state: DeclarePackLevel on a DISCONTINUED item (packaging.go 249, PUT), Resubmit of a RECEIVED feed (feed.go 181) |
| `node-not-sellable` | 409 | RETRYABLE | the node status is an event-fed view that changes (listing.go 317-327, node_events.go 155); no keyed route answers it |
| `not-found` | 404 | RETRYABLE | judgement call 2 |
| `pack-level-order` | 422 | RETRYABLE | packaging.go 217: the child must be an active level another request can declare; the slug also covers a request-only check (182), see the follow-up |
| `invalid-pack-quantity` | 422 | RETRYABLE | packaging.go 221 compares with the child level; also request-only checks (171-179), see the follow-up; PUT |
| `invalid-pack-dimensions` | 422 | RETRYABLE | packaging.go 225 compares the gross weight with contents declared elsewhere; also request-only checks (193), see the follow-up; PUT |
| `validation-failed` | 422 | RETRYABLE | mostly request fields, but also state (feed.go 184: the feed was sent for another node; feed_apply.go 40-42), see the follow-up |
| `dependency-unavailable` | 503 | RETRYABLE | a 5xx is never stored (`/readyz` only; no keyed route answers it) |
| `internal-error` | 500 | RETRYABLE | a 5xx is never stored |

### Judgement calls

1. **The FAILED feed** (decision 6): `negative-availability` OUTCOME, `listing-not-on-node` RETRYABLE with a record. The
   record flag is the only departure from "RETRYABLE commits nothing".
2. **`not-found` is RETRYABLE although every keyed target is minted here.** On the keyed routes the target is a catalogue
   item (ids minted by `ids.UUID`, ids.go 20; nothing is deleted; no event creates one), which by the owner's rule would
   make it FINAL. The entry is `ports.ErrNotFound`, shared by every repository miss: a feed id is named by the caller and
   purged after 30 days (retention.go 43), a listing is also created by events. A future keyed command on either would
   silently inherit a stale 404. The cost of RETRYABLE is one more lookup on a retried 404.
3. **`validation-failed`, `pack-level-order`, `invalid-pack-quantity`, `invalid-pack-dimensions` are RETRYABLE** because each
   slug mixes a request-only cause with a state-dependent one. Splitting them into their own problem types is a follow-up
   (it changes the public problem catalogue, so not in this change).
4. **`gtin-already-assigned`, `duplicate-listing`, `stale-availability`, `feed-already-applied`, `pack-quantity-immutable` are
   FINAL** although they read state: the state only moves one way (a GTIN, a listing, an asOf mark, a terminal feed never
   go back), so the refusal never lifts. If a purge or a delete is ever added for any of them, the class must change.
5. Not classified: the router's own 404 and 405 and the `/readyz` 503 are answered before any keyed middleware and are
   never under a key.

## Consequences

- A retried `concurrent-update`, `pack-level-not-found`, `listing-not-on-node`, `node-not-sellable`, `illegal-transition`,
  `not-found` or `validation-failed` runs again; a FINAL or OUTCOME one is replayed with `Idempotency-Replayed: true`.
  2xx, 5xx and FINAL answers are byte-identical to before.
- The per-handler `keepWrites` call is gone; a new refusal that must keep writes is an OUTCOME entry, not handler code.
- Consumers: vendor-connectivity (`usecases/submissions.go` `executeInventory` and `classify`) treats `feed-already-applied`
  as success and EVERY other refusal as a terminal failure (`failure(ref.Code, ...)`, only a network error or a 5xx stays
  pending), so it does not retry through `listing-not-on-node` or `concurrent-update`. With a released key it may now do so
  with the same key (key = feedId); that change belongs to vendor-connectivity and is only reported here.
- The `Idempotency-Key` text of `apis/openapi.yaml` no longer says "replays the first response" for every answer; it lists
  the problem types that release the key.
- MCP and A2A are read-only here (ADR 0007, 0010): there is no write tool with its own store to classify.
- Follow-ups: split the mixed slugs (judgement call 3); one unique violation on a first insert inside the savepoint
  (`duplicate-listing` race) deserves a look separate from this change.
