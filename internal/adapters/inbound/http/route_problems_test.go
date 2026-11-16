package http

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestWrongMethodCarriesAllowHeader pins RFC 9110 section 15.5.6: a 405 MUST
// list the methods the resource supports. chi sets Allow only in its DEFAULT
// 405 handler; once a custom handler is registered (the RFC 9457 one) it is
// called as-is, so the handler derives the list from the router itself.
func TestWrongMethodCarriesAllowHeader(t *testing.T) {
	r := newRig(t)
	cases := []struct{ method, path, wantAllow string }{
		{http.MethodDelete, "/healthz", "GET"},
		{http.MethodPost, "/readyz", "GET"},
		{http.MethodPut, "/catalog-items", "POST"},
		{http.MethodGet, "/catalog-items/abc/discontinuations", "POST"},
		{http.MethodPost, "/catalog-items/abc/packaging/PALLET", "PUT"},
		{http.MethodGet, "/catalog-items/abc/packaging/PALLET/retirements", "POST"},
		{http.MethodPost, "/catalog", "GET"},
		{http.MethodDelete, "/catalog/abc", "GET"},
		{http.MethodDelete, "/listings", "GET, POST"},
		{http.MethodPut, "/inventory-feeds", "POST"},
		{http.MethodPost, "/inventory-feeds/abc", "GET"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			res := r.do(tc.method, tc.path, "")
			res.problem(t, http.StatusMethodNotAllowed, "method-not-allowed")
			if got := res.header.Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
		})
	}
}

// TestUnknownPathCarriesNoAllow: a 404 is not a 405 and lists no methods.
func TestUnknownPathCarriesNoAllow(t *testing.T) {
	res := newRig(t).do(http.MethodGet, "/nope", "")
	res.problem(t, http.StatusNotFound, "not-found")
	if got := res.header.Get("Allow"); got != "" {
		t.Errorf("Allow on 404 = %q, want none", got)
	}
}

// TestAllowedMethodsListsEveryRegisteredMethod covers a path with several
// methods, so the list is derived from the router and not hard-coded.
func TestAllowedMethodsListsEveryRegisteredMethod(t *testing.T) {
	r := chi.NewRouter()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	r.Get("/x", noop)
	r.Post("/x", noop)
	r.Delete("/x", noop)
	r.Get("/y", noop)
	if got, want := allowedMethods(r, "/x"), "GET, POST, DELETE"; got != want {
		t.Errorf("allowedMethods(/x) = %q, want %q", got, want)
	}
	if got, want := allowedMethods(r, "/y"), "GET"; got != want {
		t.Errorf("allowedMethods(/y) = %q, want %q", got, want)
	}
	if got := allowedMethods(r, "/nope"); got != "" {
		t.Errorf("allowedMethods(/nope) = %q, want empty", got)
	}
}
