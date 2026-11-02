package http

import (
	"net/http"

	"github.com/claudioed/offer-availability/internal/adapters/inbound/http/page"
	"github.com/claudioed/offer-availability/internal/application/usecases"
)

var (
	invalidLimit          = problem{http.StatusBadRequest, "invalid-limit", "The limit is invalid"}
	invalidCursor         = problem{http.StatusBadRequest, "invalid-cursor", "The cursor is invalid"}
	unknownQueryParameter = problem{http.StatusBadRequest, "unknown-query-parameter", "The query parameter is not declared by this collection"}
)

// pageParams reads limit and cursor of a collection GET (REST guideline 3, 4.2).
// filters are the other query parameters the collection declares; any further one
// is 400 unknown-query-parameter. The cursor is the base64url keyset of the page
// package; its ID is the last returned id the use case continues after.
func pageParams(r *http.Request, filters ...string) (usecases.PageQuery, error) {
	var q usecases.PageQuery
	if name := page.Unknown(r, filters...); name != "" {
		return q, detailError{"the query parameter " + sanitizeLog(name) + " is not declared by this collection", unknownQueryParameter}
	}
	limit, err := page.Limit(r)
	if err != nil {
		return q, detailError{err.Error(), invalidLimit}
	}
	q.Limit = &limit
	pos, err := page.After(r)
	if err != nil {
		return q, detailError{err.Error(), invalidCursor}
	}
	if pos != nil {
		q.After = pos.ID
	}
	return q, nil
}

// cursorOf turns the id a use case returns as its next position into the opaque
// cursor of the response ("" stays absent).
func cursorOf(id string) string {
	if id == "" {
		return ""
	}
	return page.Encode(page.Position{ID: id})
}
