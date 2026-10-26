package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

type panicking struct{}

func (panicking) Execute(context.Context, usecases.PageQuery) (views.CatalogPage, error) {
	panic("boom")
}

func TestUnmappedErrorIs500WithoutLeakingItsMessage(t *testing.T) {
	r := newRig(t)
	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil), errors.New("pq: secret connection detail"))
	if rec.Code != http.StatusInternalServerError || contains(rec.Body.String(), "secret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	_ = r
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestProblemCatalogueMapsEveryModelError(t *testing.T) {
	for _, entry := range problemCatalogue {
		if got := problemFor(entry.err); got != entry.p {
			t.Errorf("%v maps to %v, want %v", entry.err, got, entry.p)
		}
	}
	if got := problemFor(shared.Invalid("x")); got.slug != "validation-failed" {
		t.Fatalf("wrapped validation error maps to %v", got)
	}
	if got := problemFor(errors.New("other")); got != internalProblem {
		t.Fatalf("unmapped = %v", got)
	}
}

func TestContainsNUL(t *testing.T) {
	for body, want := range map[string]bool{
		`{"a":"b"}`:             false,
		"{\"a\":\"\x00\"}":      true,
		`{"a":"\u0000"}`:        true,
		`{"a":"\\u0000"}`:       false, // an escaped backslash followed by the text u0000
		`{"a":"\\\u0000"}`:      true,  // backslash-escape, then a real \u0000
		`{"a":"\\u0000\u0000"}`: true,
	} {
		if got := containsNUL([]byte(body)); got != want {
			t.Errorf("containsNUL(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestReadinessGate(t *testing.T) {
	var fail atomic.Bool
	calls := 0
	clock := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	g := NewReadiness(time.Minute, time.Second, Dependency{Name: "postgres", Check: func(context.Context) error {
		calls++
		if fail.Load() {
			return errors.New("down")
		}
		return nil
	}})
	g.now = func() time.Time { return clock }
	serve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		readyz(g)(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec
	}
	if rec := serve(); rec.Code != http.StatusOK {
		t.Fatalf("ready: %d", rec.Code)
	}
	fail.Store(true)
	if rec := serve(); rec.Code != http.StatusOK || calls != 1 {
		t.Fatalf("a verdict is cached for the ttl: %d, %d calls", rec.Code, calls)
	}
	clock = clock.Add(2 * time.Minute)
	rec := serve()
	if rec.Code != http.StatusServiceUnavailable || !contains(rec.Body.String(), `"dependency-unavailable"`) || !contains(rec.Body.String(), "postgres") {
		t.Fatalf("down: %d %s", rec.Code, rec.Body)
	}
	g.SetNotReady()
	rec = serve()
	if rec.Code != http.StatusServiceUnavailable || !contains(rec.Body.String(), "not_ready") {
		t.Fatalf("draining: %d %s", rec.Code, rec.Body)
	}
	var zero *Readiness
	if !zero.Ready() || zero.Failing(context.Background()) != nil {
		t.Fatal("the nil gate is ready")
	}
	zero.SetNotReady()
	if d := NewReadiness(0, 0); d.ttl != DefaultReadinessTTL || d.timeout != DefaultReadinessTimeout {
		t.Fatalf("defaults: %+v", d)
	}
}
