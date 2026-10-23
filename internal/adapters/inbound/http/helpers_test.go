package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// rig is the REST server over the memory doubles, with the Idempotency-Key
// middleware replaced by a pass-through (its own tests need Postgres).
type rig struct {
	t       *testing.T
	store   *memory.Store
	handler http.Handler
	server  *Server
}

func passThrough(string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return next }
}

func newRig(t *testing.T) *rig {
	t.Helper()
	s := memory.NewStore()
	d := usecases.Deps{
		Catalog: s.Catalog(), Listings: s.Listings(), Feeds: s.Feeds(), Marks: s.Marks(), Nodes: s.Nodes(), Inbox: s.Inbox(),
		Outbox: s.Outbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(now), IDs: &memory.SequentialIDs{},
	}
	srv := &Server{
		CreateCatalogItem: usecases.NewCreateCatalogItem(d), DiscontinueItem: usecases.NewDiscontinueItem(d),
		BrowseCatalog: usecases.NewBrowseCatalog(d.Catalog), GetCatalogItem: usecases.NewGetCatalogItem(d.Catalog),
		CreateListing: usecases.NewCreateListing(d), ListListings: usecases.NewListListings(d.Listings),
		ApplyInventoryFeed: usecases.NewApplyInventoryFeed(d), GetInventoryFeed: usecases.NewGetInventoryFeed(d.Feeds),
		Idempotency: passThrough, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h, err := NewRouter(srv)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, store: s, handler: h, server: srv}
}

// activate makes the node ACTIVE in the local node status view.
func (r *rig) activate(node string) {
	r.t.Helper()
	v, err := nodestatus.New(nodestatus.ID(node), nodestatus.StatusActive, now)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.store.Nodes().Save(context.Background(), v); err != nil {
		r.t.Fatal(err)
	}
}

type response struct {
	code   int
	header http.Header
	body   []byte
}

func (r *rig) do(method, target, body string) response {
	r.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return response{code: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

func (res response) json() map[string]any {
	var m map[string]any
	if err := json.NewDecoder(bytes.NewReader(res.body)).Decode(&m); err != nil {
		panic("response is not a JSON object: " + string(res.body))
	}
	return m
}

// problemType asserts an RFC 9457 body and returns its type slug.
func (res response) problem(t *testing.T, status int, slug string) map[string]any {
	t.Helper()
	if res.code != status {
		t.Fatalf("status = %d, want %d: %s", res.code, status, res.body)
	}
	if ct := res.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
	m := res.json()
	if m["type"] != slug || int(m["status"].(float64)) != status {
		t.Fatalf("problem = %v, want type %s", m, slug)
	}
	return m
}

func (r *rig) createItem() string {
	r.t.Helper()
	res := r.do(http.MethodPost, "/catalog-items", `{"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"fragile":true}`)
	if res.code != http.StatusCreated {
		r.t.Fatalf("create item: %d %s", res.code, res.body)
	}
	return res.json()["catalogItemId"].(string)
}

func (r *rig) createListing(item, node string) string {
	r.t.Helper()
	res := r.do(http.MethodPost, "/listings", `{"catalogItemId":"`+item+`","nodeId":"`+node+`"}`)
	if res.code != http.StatusCreated {
		r.t.Fatalf("create listing: %d %s", res.code, res.body)
	}
	return res.json()["listingId"].(string)
}
