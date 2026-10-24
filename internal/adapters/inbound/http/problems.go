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
	idempotencyKeyReused   = problem{http.StatusUnprocessableEntity, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}

	dependencyUnavailable = problem{http.StatusServiceUnavailable, "dependency-unavailable", "A runtime dependency is unavailable"}
)

// Typed errors the adapter raises itself, each carrying its own detail.
var (
	errNULQuery error = detailError{"a NUL character (U+0000) is not allowed in the query string", malformedQuery}
	errNULBody  error = detailError{"a NUL character (U+0000) is not allowed in a request body", malformedBody}
	errTooLarge error = detailError{"the request body is larger than the limit", bodyTooLarge}
)

// detailError is an adapter error that already knows its problem.
type detailError struct {
	detail string
	p      problem
}

func (e detailError) Error() string { return e.detail }

// problemCatalogue maps every typed error to its problem, in match order. The
// slugs are the catalogue of ADR 0002 (A18): the four shared types of the model
// conventions plus one kebab-case slug per model Err.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, malformedBody},
	// Shared types (model conventions.errors).
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Resource not found"}},
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}},
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified by another request; re-fetch it and retry"}},
	// CatalogItem identifiers and packaging (ADR 0006).
	{catalog.ErrInvalidGTIN, problem{http.StatusUnprocessableEntity, "invalid-gtin", "The GTIN is malformed or its check digit is wrong"}},
	{catalog.ErrGtinAlreadyAssigned, problem{http.StatusConflict, "gtin-already-assigned", "The GTIN already identifies an item or a pack level"}},
	{catalog.ErrPackQuantityImmutable, problem{http.StatusConflict, "pack-quantity-immutable", "The unitsPerPack of an active pack level cannot change; declare a new GTIN"}},
	{catalog.ErrPackLevelOrder, problem{http.StatusUnprocessableEntity, "pack-level-order", "A pack level needs an active child level that is lower than it"}},
	{catalog.ErrInvalidPackQuantity, problem{http.StatusUnprocessableEntity, "invalid-pack-quantity", "The unitsPerPack is out of range or disagrees with the child chain"}},
	{catalog.ErrInvalidPackDimensions, problem{http.StatusUnprocessableEntity, "invalid-pack-dimensions", "The pack dimensions or gross weight are invalid"}},
	{catalog.ErrInvalidPackLevel, problem{http.StatusUnprocessableEntity, "invalid-pack-level", "The pack level must be EACH, INNER, CASE or PALLET"}},
	{catalog.ErrPackLevelNotFound, problem{http.StatusNotFound, "pack-level-not-found", "The item has no such pack level"}},
	// CatalogItem.
	{catalog.ErrInvalidItemDimensions, problem{http.StatusUnprocessableEntity, "invalid-item-dimensions", "Dimensions and weight must be positive"}},
	// Listing.
	{listing.ErrDuplicateListing, problem{http.StatusConflict, "duplicate-listing", "A listing of this item on this node exists"}},
	{listing.ErrNegativeAvailability, problem{http.StatusUnprocessableEntity, "negative-availability", "Availability cannot be negative"}},
	{listing.ErrStaleAvailability, problem{http.StatusConflict, "stale-availability", "The availability is not newer than the current one"}},
	{listing.ErrNodeNotSellable, problem{http.StatusConflict, "node-not-sellable", "The node is not sellable"}},
	// InventoryFeed.
	{feed.ErrListingNotOnNode, problem{http.StatusUnprocessableEntity, "listing-not-on-node", "A feed line names a listing of another node"}},
	{feed.ErrFeedAlreadyApplied, problem{http.StatusConflict, "feed-already-applied", "This feed id was already applied or superseded"}},
	// Any value-object rule.
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}},
}

// problemFor returns the problem of err: the adapter's own detailError first,
// then the first catalogue entry err matches, then the internal-error problem.
func problemFor(err error) problem {
	var de detailError
	if errors.As(err, &de) {
		return de.p
	}
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
