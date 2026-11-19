// Package http is the inbound REST adapter: a chi router implementing every
// operation of apis/openapi.yaml, the Idempotency-Key and NUL-rejection
// middleware, RFC 9457 problem+json errors and the /healthz and /readyz
// probes. It depends on the application layer only; the composition root
// (cmd/api) hands it the use cases, the readiness gate and the transaction
// binder.
package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// errMalformedRequest marks a body or query the adapter itself rejects (bad
// JSON, an unknown field, a wrong type, a NUL character).
var errMalformedRequest = errors.New("request is not valid for this operation")

// problem is the fixed (status, slug, title) of one error category; the detail
// comes from the error at write time. The slug is the `type` of the RFC 9457
// body (apis/openapi.yaml Problem).
type problem struct {
	status int
	slug   string
	title  string
}

var (
	internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

	routeNotFound    = problem{http.StatusNotFound, "not-found", "Resource not found"}
	methodNotAllowed = problem{http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed"}

	malformedBody  = problem{http.StatusBadRequest, "malformed-request-body", "Malformed request body"}
	malformedQuery = problem{http.StatusBadRequest, "malformed-request-query", "Malformed query string"}
	bodyTooLarge   = problem{http.StatusRequestEntityTooLarge, "request-body-too-large", "Request body too large"}

	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyInvalid  = problem{http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid"}
	idempotencyKeyReused   = problem{http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}

	dependencyUnavailable = problem{http.StatusServiceUnavailable, "dependency-unavailable", "A runtime dependency is unavailable"}
)

// Typed errors the adapter raises itself, each carrying its own detail.
var (
	errNULQuery error = detailError{"a NUL character (U+0000) is not allowed in the query string", malformedQuery}
	errNULBody  error = detailError{"a NUL character (U+0000) is not allowed in a request body", malformedBody}
	errTooLarge error = detailError{"the request body is larger than the limit", bodyTooLarge}
)

// detailError is an adapter error that already knows its problem (its class is
// the entry of standaloneProblems that holds the same problem).
type detailError struct {
	detail string
	p      problem
}

func (e detailError) Error() string { return e.detail }

// catalogueEntry is one problem type: the typed error it answers, its problem
// and its replay class, the field the Idempotency-Key middleware reads (ADR
// 0016). record is set only on a RETRYABLE entry whose use case SAVED a fact
// before refusing that a retry replaces: the middleware commits that fact and
// still frees the key (the FAILED feed of listing-not-on-node).
type catalogueEntry struct {
	err    error
	p      problem
	replay replayClass
	record bool
}

// problemCatalogue maps every typed error to its problem, in match order. The
// slugs are the catalogue of ADR 0002 (A18): the four shared types of the model
// conventions plus one kebab-case slug per model Err.
//
// Every entry declares its replay class (ADR 0016 holds the reason of each):
// FINAL when the same request always gets the same answer, OUTCOME when the
// command committed state and then refused, RETRYABLE when nothing is
// remembered because the answer depends on state that can change. When unsure
// the class is RETRYABLE: a released refusal costs one more execution. An entry
// without a class fails TestEveryProblemTypeDeclaresAReplayClass.
var problemCatalogue = []catalogueEntry{
	// FINAL: bad JSON, an unknown field or a NUL is a function of the request alone.
	{errMalformedRequest, malformedBody, replayFinal, false},
	// Shared types (model conventions.errors).
	// RETRYABLE: on the keyed routes the target is a catalogue item this service mints (ids.go) and never deletes, which would make it FINAL, but the entry is shared by every
	// repository miss (a feed id is named by the caller and purged by retention; a listing is also created by events), so a future keyed command would inherit a stale 404 (ADR 0016).
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Resource not found"}, replayRetryable, false},
	// RETRYABLE: the aggregate has not reached the state yet (packaging.go DeclarePackLevel on a DISCONTINUED item, feed.go Resubmit of a RECEIVED feed).
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}, replayRetryable, false},
	// RETRYABLE: another writer won the optimistic lock (unit_of_work.go saveResult); nothing committed, the retry re-reads and may win.
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified by another request; re-fetch it and retry"}, replayRetryable, false},
	// CatalogItem identifiers and packaging (ADR 0006).
	// FINAL: the GTIN and its check digit are in the request.
	{catalog.ErrInvalidGTIN, problem{http.StatusUnprocessableEntity, "invalid-gtin", "The GTIN is malformed or its check digit is wrong"}, replayFinal, false},
	// FINAL: a GTIN identifies one thing forever, retired pack levels included, and nothing is deleted (usecases/catalog.go gtinFree, packaging.go redeclare), so the refusal never lifts.
	{catalog.ErrGtinAlreadyAssigned, problem{http.StatusConflict, "gtin-already-assigned", "The GTIN already identifies an item or a pack level"}, replayFinal, false},
	// FINAL: an active level never changes its quantity and a retired one keeps its GTIN (packaging.go redeclare); reachable on the unkeyed PUT only.
	{catalog.ErrPackQuantityImmutable, problem{http.StatusConflict, "pack-quantity-immutable", "The unitsPerPack of an active pack level cannot change; declare a new GTIN"}, replayFinal, false},
	// RETRYABLE: packaging.go checkChain (the child level must be active) depends on levels another request can declare; the slug also covers a request-only shape check, a follow-up split.
	{catalog.ErrPackLevelOrder, problem{http.StatusUnprocessableEntity, "pack-level-order", "A pack level needs an active child level that is lower than it"}, replayRetryable, false},
	// RETRYABLE: packaging.go checkChain compares with the child level declared by another request; the slug also covers request-only checks, a follow-up split.
	{catalog.ErrInvalidPackQuantity, problem{http.StatusUnprocessableEntity, "invalid-pack-quantity", "The unitsPerPack is out of range or disagrees with the child chain"}, replayRetryable, false},
	// RETRYABLE: the gross weight is compared with the contents declared by other requests (packaging.go checkChain); the slug also covers request-only checks, a follow-up split.
	{catalog.ErrInvalidPackDimensions, problem{http.StatusUnprocessableEntity, "invalid-pack-dimensions", "The pack dimensions or gross weight are invalid"}, replayRetryable, false},
	// FINAL: the level is a path segment of the request.
	{catalog.ErrInvalidPackLevel, problem{http.StatusUnprocessableEntity, "invalid-pack-level", "The pack level must be EACH, INNER, CASE or PALLET"}, replayFinal, false},
	// RETRYABLE: packaging.go RetirePackLevel; a PUT can declare the level later.
	{catalog.ErrPackLevelNotFound, problem{http.StatusNotFound, "pack-level-not-found", "The item has no such pack level"}, replayRetryable, false},
	// CatalogItem.
	// FINAL: dimensions and weight are in the request.
	{catalog.ErrInvalidItemDimensions, problem{http.StatusUnprocessableEntity, "invalid-item-dimensions", "Dimensions and weight must be positive"}, replayFinal, false},
	// Listing.
	// FINAL: a listing is never deleted, so the (item, node, owner) pair stays taken (usecases/listings.go pairTaken).
	{listing.ErrDuplicateListing, problem{http.StatusConflict, "duplicate-listing", "A listing of this item on this node exists"}, replayFinal, false},
	// OUTCOME: the feed is saved FAILED with the refusal (usecases/feeds.go 97-101, settle and Save 130-139) and the handler answers 422 with the recorded feed as instance (handlers.go applyInventoryFeed); the record commits with the stored answer. The quantity is in the request.
	{listing.ErrNegativeAvailability, problem{http.StatusUnprocessableEntity, "negative-availability", "Availability cannot be negative"}, replayOutcome, false},
	// FINAL: a listing's asOf only moves forward (listing.go ApplyAvailability), so a stale line stays stale; the feed counts it instead of failing (feed_apply.go), no keyed route answers it today.
	{listing.ErrStaleAvailability, problem{http.StatusConflict, "stale-availability", "The availability is not newer than the current one"}, replayFinal, false},
	// RETRYABLE: the node status is an event-fed view that changes (listing.go Restore, node_events.go); no keyed route answers it today.
	{listing.ErrNodeNotSellable, problem{http.StatusConflict, "node-not-sellable", "The node is not sellable"}, replayRetryable, false},
	// InventoryFeed.
	// RETRYABLE with record: the feed is saved FAILED first (usecases/feeds.go 97-101) and that record must stay (ADR 0002 A11, GET /inventory-feeds/{feedId}), but the listing is event-fed and can exist on the retry, which resubmits the same feed id (feeds.go receive, feed.go Resubmit): the key is freed, the record kept.
	{feed.ErrListingNotOnNode, problem{http.StatusUnprocessableEntity, "listing-not-on-node", "A feed line names a listing of another node"}, replayRetryable, true},
	// FINAL: APPLIED and SUPERSEDED are terminal (feed.go Resubmit); vendor-connectivity already reads it as success.
	{feed.ErrFeedAlreadyApplied, problem{http.StatusConflict, "feed-already-applied", "This feed id was already applied or superseded"}, replayFinal, false},
	// Any value-object rule.
	// RETRYABLE: mostly the request's own fields, but also state (feed.go Resubmit: the feed was sent for another node; feed_apply.go matches); a clean split is a follow-up.
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}, replayRetryable, false},
}

// standaloneProblems are the problem types written without a catalogue error:
// the adapter's own (detailError carries the problem), the Idempotency-Key
// middleware's and the 5xx. They need a class like every other problem type. The
// router's own 404 and 405 and the /readyz 503 are answered before any keyed
// middleware and never under a key: they are not classified here.
var standaloneProblems = []catalogueEntry{
	{nil, malformedQuery, replayFinal, false},
	{nil, malformedBody, replayFinal, false},
	{nil, bodyTooLarge, replayFinal, false},
	{nil, invalidLimit, replayFinal, false},
	{nil, invalidCursor, replayFinal, false},
	{nil, unknownQueryParameter, replayFinal, false},
	{nil, idempotencyKeyRequired, replayFinal, false},
	{nil, idempotencyKeyInvalid, replayFinal, false},
	{nil, idempotencyKeyReused, replayRetryable, false},
	{nil, dependencyUnavailable, replayRetryable, false},
	{nil, internalProblem, replayRetryable, false},
}

// entryFor returns the catalogue entry of err: the adapter's own detailError
// first (its problem's entry in standaloneProblems), then the first catalogue
// entry err matches. ok is false for an error the catalogue does not know; the
// entry then is the internal-error problem with no class (a 5xx, never stored).
func entryFor(err error) (entry catalogueEntry, ok bool) {
	var de detailError
	if errors.As(err, &de) {
		for _, e := range standaloneProblems {
			if e.p == de.p {
				return e, true
			}
		}
		return catalogueEntry{p: de.p}, true
	}
	for _, e := range problemCatalogue {
		if errors.Is(err, e.err) {
			return e, true
		}
	}
	return catalogueEntry{p: internalProblem}, false
}

// problemFor returns the problem of err, or the internal-error problem.
func problemFor(err error) problem {
	entry, _ := entryFor(err)
	return entry.p
}
