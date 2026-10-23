package http

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func (s *Server) mountCatalog(r chi.Router) {
	s.keyed(r, http.MethodPost, "/catalog-items", s.createCatalogItem)
	s.keyed(r, http.MethodPost, "/catalog-items/{catalogItemId}:discontinue", s.discontinueCatalogItem)
	r.Get("/catalog", s.browseCatalog)
	r.Get("/catalog/{catalogItemId}", s.getCatalogItem)
}

func (s *Server) mountListings(r chi.Router) {
	s.keyed(r, http.MethodPost, "/listings", s.createListing)
	r.Get("/listings", s.listListings)
}

func (s *Server) mountFeeds(r chi.Router) {
	s.keyed(r, http.MethodPost, "/inventory-feeds", s.applyInventoryFeed)
	r.Get("/inventory-feeds/{feedId}", s.getInventoryFeed)
}

func itemID(r *http.Request) catalog.ID { return catalog.ID(chi.URLParam(r, "catalogItemId")) }

// createCatalogItem is POST /catalog-items.
func (s *Server) createCatalogItem(w http.ResponseWriter, r *http.Request) {
	var body createCatalogItemBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	item, err := s.CreateCatalogItem.Execute(r.Context(), body.command())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// discontinueCatalogItem is POST /catalog-items/{catalogItemId}:discontinue, which
// takes no body.
func (s *Server) discontinueCatalogItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.DiscontinueItem.Execute(r.Context(), itemID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// pageQuery reads limit and after. A limit that is not an integer is a value
// error (validation-failed), like one out of range.
func pageQuery(r *http.Request) (usecases.PageQuery, error) {
	q := r.URL.Query()
	page := usecases.PageQuery{After: q.Get("after")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return page, shared.Invalid("limit must be an integer")
		}
		page.Limit = &n
	}
	return page, nil
}

// browseCatalog is GET /catalog?limit=&after=.
func (s *Server) browseCatalog(w http.ResponseWriter, r *http.Request) {
	page, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.BrowseCatalog.Execute(r.Context(), page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getCatalogItem is GET /catalog/{catalogItemId}.
func (s *Server) getCatalogItem(w http.ResponseWriter, r *http.Request) {
	item, err := s.GetCatalogItem.Execute(r.Context(), itemID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// createListing is POST /listings.
func (s *Server) createListing(w http.ResponseWriter, r *http.Request) {
	var body createListingBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	l, err := s.CreateListing.Execute(r.Context(), body.command())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

// listListings is GET /listings?nodeId=&catalogItemId=&limit=&after=.
func (s *Server) listListings(w http.ResponseWriter, r *http.Request) {
	page, err := pageQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	out, err := s.ListListings.Execute(r.Context(), usecases.ListListingsQuery{
		NodeID: listing.NodeID(q.Get("nodeId")), CatalogItemID: listing.ItemID(q.Get("catalogItemId")), Page: page,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// applyInventoryFeed is POST /inventory-feeds. APPLIED and SUPERSEDED answer 201
// with the feed. A FAILED feed is recorded and answers 422 whose instance is the
// recorded feed, and the Idempotency-Key middleware is told to keep that record.
func (s *Server) applyInventoryFeed(w http.ResponseWriter, r *http.Request) {
	var body applyInventoryFeedBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.FullUpdate == nil || body.Lines == nil {
		writeError(w, r, shared.Invalid("feedId, nodeId, fullUpdate, asOf and lines are required"))
		return
	}
	v, err := s.ApplyInventoryFeed.Execute(r.Context(), body.command())
	if err != nil {
		if v.State == string(feed.StateFailed) {
			keepWrites(r.Context())
			p := problemFor(err)
			writeProblemAt(w, p, err.Error(), fmt.Sprintf("/inventory-feeds/%s", v.FeedID))
			return
		}
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// getInventoryFeed is GET /inventory-feeds/{feedId}.
func (s *Server) getInventoryFeed(w http.ResponseWriter, r *http.Request) {
	v, err := s.GetInventoryFeed.Execute(r.Context(), feed.ID(chi.URLParam(r, "feedId")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
