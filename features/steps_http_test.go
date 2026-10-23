package features_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/cucumber/godog"

	oahttp "github.com/claudioed/offer-availability/internal/adapters/inbound/http"
	"github.com/claudioed/offer-availability/internal/application/usecases"
)

// This file adds the REST dimension to the suite: the REAL chi router of
// internal/adapters/inbound/http over the same memory doubles the use-case
// steps use, so the scenarios can speak about status codes, problem slugs and
// JSON fields (apis/openapi.yaml, .claude/rules/rest-api.md).
//
// The only substitute is the Idempotency-Key middleware: the production one is
// transactional over Postgres (proven by idempotency_integration_test.go).
// memoryIdempotency below implements its documented contract one-for-one.

// apiRig is the HTTP harness of one scenario.
type apiRig struct {
	w       *world
	handler http.Handler
	idem    *memoryIdempotency

	key     string // explicit Idempotency-Key; "" mints a fresh one per request
	skipKey bool   // send no key at all
	seq     int

	status int
	header http.Header
	body   []byte
}

// rig lazily builds the HTTP harness against the CURRENT world: the world is
// reset before every scenario, so a rig built for an earlier one must not
// survive (its eventsBefore would be written to a dead world).
func (w *world) rig() *apiRig {
	if w.api == nil || w.api.w != w {
		w.api = newAPIRig(w)
	}
	return w.api
}

func newAPIRig(w *world) *apiRig {
	a := &apiRig{w: w, idem: newMemoryIdempotency()}
	srv := &oahttp.Server{
		CreateCatalogItem:  usecases.NewCreateCatalogItem(w.deps),
		DiscontinueItem:    usecases.NewDiscontinueItem(w.deps),
		BrowseCatalog:      usecases.NewBrowseCatalog(w.deps.Catalog),
		GetCatalogItem:     usecases.NewGetCatalogItem(w.deps.Catalog),
		CreateListing:      usecases.NewCreateListing(w.deps),
		ListListings:       usecases.NewListListings(w.deps.Listings),
		ApplyInventoryFeed: usecases.NewApplyInventoryFeed(w.deps),
		GetInventoryFeed:   usecases.NewGetInventoryFeed(w.deps.Feeds),
		Idempotency:        a.idem.middleware,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h, err := oahttp.NewRouter(srv)
	if err != nil {
		panic(fmt.Sprintf("bdd rig: %v", err))
	}
	a.handler = h
	return a
}

// send performs one request through the real router and records the answer.
func (a *apiRig) send(method, target, body string) error {
	a.w.act()
	a.seq++
	key := a.key
	if key == "" {
		key = fmt.Sprintf("scenario-key-%d", a.seq)
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if !a.skipKey {
		req.Header.Set(oahttp.IdempotencyKeyHeader, key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	out, readErr := io.ReadAll(res.Body)
	if readErr != nil {
		return readErr
	}
	a.status, a.header, a.body = res.StatusCode, res.Header, out
	return nil
}

// ---- the Idempotency-Key double ---------------------------------------------

type storedAnswer struct {
	hash        string
	contentType string
	status      int
	body        []byte
}

// memoryIdempotency mirrors the contract of RequireIdempotencyKey
// (internal/adapters/inbound/http/idempotency.go): (route, key) identifies a
// request; a repeat with the same fingerprint (method, path, body) replays the
// FIRST response with Idempotency-Replayed: true and never calls the handler
// again; the same key with another fingerprint is 422 idempotency-key-reused;
// a missing key is 400 idempotency-key-required, an unusable one 400
// idempotency-key-invalid; a 5xx outcome is never stored so the key can retry.
type memoryIdempotency struct {
	mu      sync.Mutex
	handled map[string]storedAnswer
}

func newMemoryIdempotency() *memoryIdempotency {
	return &memoryIdempotency{handled: map[string]storedAnswer{}}
}

func (m *memoryIdempotency) refuse(w http.ResponseWriter, status int, slug, title, detail string) {
	body, _ := json.Marshal(map[string]any{"type": slug, "title": title, "status": status, "detail": detail})
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (m *memoryIdempotency) middleware(route string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(oahttp.IdempotencyKeyHeader)
			if key == "" {
				m.refuse(w, http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required",
					"send an Idempotency-Key header with a unique value per logical request")
				return
			}
			if len(key) > oahttp.MaxIdempotencyKeyLength || hasControlChar(key) {
				m.refuse(w, http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is invalid",
					"the Idempotency-Key must be at most 200 printable characters")
				return
			}
			m.handle(route, key, next, w, r)
		})
	}
}

// handle runs the keyed protocol for a validated key: replay a stored answer,
// refuse a reused key, or run the handler and store its outcome.
func (m *memoryIdempotency) handle(route, key string, next http.Handler, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	hash := requestFingerprint(r, body)

	id := route + "\x00" + key
	m.mu.Lock()
	stored, seen := m.handled[id]
	m.mu.Unlock()
	if seen {
		m.answer(w, hash, stored)
		return
	}
	m.record(id, hash, next, w, r)
}

// answer serves a repeat: the stored outcome when the fingerprint matches, the
// reuse refusal when it does not.
func (m *memoryIdempotency) answer(w http.ResponseWriter, hash string, stored storedAnswer) {
	if stored.hash != hash {
		m.refuse(w, http.StatusUnprocessableEntity, "idempotency-key-reused", "Idempotency-Key was already used with a different request",
			"the Idempotency-Key was used with a different request")
		return
	}
	w.Header().Set("Content-Type", stored.contentType)
	w.Header().Set(oahttp.IdempotencyReplayedHeader, "true")
	w.WriteHeader(stored.status)
	_, _ = w.Write(stored.body)
}

// record runs the handler for a fresh key and stores its outcome (a 5xx is
// never stored, so the key can retry), then forwards the answer.
func (m *memoryIdempotency) record(id, hash string, next http.Handler, w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, r)
	res := rec.Result()
	out, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode < http.StatusInternalServerError {
		m.mu.Lock()
		m.handled[id] = storedAnswer{hash: hash, contentType: res.Header.Get("Content-Type"), status: res.StatusCode, body: out}
		m.mu.Unlock()
	}
	for name, vs := range res.Header {
		if name == "Content-Length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(out)
}

func hasControlChar(s string) bool {
	for _, c := range s {
		if unicode.IsControl(c) {
			return true
		}
	}
	return false
}

// requestFingerprint mirrors the production fingerprint: the method, the path
// and the body — everything but the key.
func requestFingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.EscapedPath()))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ---- JSON helpers -------------------------------------------------------------

// lookup walks a dotted path ("items.0.state") through decoded JSON.
func lookup(v any, path string) (any, bool) {
	for _, seg := range strings.Split(path, ".") {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, true
}

func (a *apiRig) decoded() (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		return nil, fmt.Errorf("the response is not a JSON object: %s", a.body)
	}
	return m, nil
}

// ---- the steps ----------------------------------------------------------------

// registerHTTPSteps registers the REST vocabulary in small groups: the client
// and its requests, the status/problem assertions, the JSON field assertions
// and the event-field assertions.
func registerHTTPSteps(sc *godog.ScenarioContext, w *world) {
	registerClientSteps(sc, w)
	registerRequestSteps(sc, w)
	registerResponseSteps(sc, w)
	registerFieldSteps(sc, w)
	registerShapeSteps(sc, w)
	registerEventSteps(sc, w)
}

func registerClientSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the client uses the Idempotency-Key "([^"]*)"$`, func(key string) error {
		w.rig().key, w.rig().skipKey = key, false
		return nil
	})
	sc.Step(`^the client uses a (\d+)-character Idempotency-Key$`, func(n int) error {
		w.rig().key, w.rig().skipKey = strings.Repeat("k", n), false
		return nil
	})
	sc.Step(`^the client sends no Idempotency-Key$`, func() error {
		w.rig().skipKey = true
		return nil
	})
}

func registerRequestSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a (GET|POST|PUT|PATCH|DELETE) request is sent to "([^"]*)"$`, func(method, target string) error {
		return w.rig().send(method, target, "")
	})
	sc.Step(`^a (GET|POST|PUT|PATCH|DELETE) request is sent to "([^"]*)" with the (JSON|raw) body:$`, func(method, target, _, body string) error {
		return w.rig().send(method, target, body)
	})
}

func registerResponseSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response status is (\d+)$`, func(status int) error {
		if w.rig().status != status {
			return fmt.Errorf("the response status is %d (%s), want %d", w.rig().status, w.rig().body, status)
		}
		return nil
	})
	sc.Step(`^the response is a (\d+) "([a-z0-9-]+)" problem$`, func(status int, slug string) error {
		a := w.rig()
		if a.status != status {
			return fmt.Errorf("the response status is %d (%s), want %d", a.status, a.body, status)
		}
		if ct := a.header.Get("Content-Type"); ct != "application/problem+json" {
			return fmt.Errorf("content type %q, want application/problem+json", ct)
		}
		m, err := a.decoded()
		if err != nil {
			return err
		}
		if m["type"] != slug {
			return fmt.Errorf("problem type %v, want %q (%s)", m["type"], slug, a.body)
		}
		return nil
	})
	sc.Step(`^the response problem has the instance "([^"]*)"$`, func(instance string) error {
		m, err := w.rig().decoded()
		if err != nil {
			return err
		}
		if m["instance"] != instance {
			return fmt.Errorf("problem instance %v, want %q", m["instance"], instance)
		}
		return nil
	})
	sc.Step(`^the response replays the stored answer$`, func() error {
		if got := w.rig().header.Get(oahttp.IdempotencyReplayedHeader); got != "true" {
			return fmt.Errorf("Idempotency-Replayed = %q, want true", got)
		}
		return nil
	})
	sc.Step(`^the response is answered fresh$`, func() error {
		if got := w.rig().header.Get(oahttp.IdempotencyReplayedHeader); got == "true" {
			return fmt.Errorf("Idempotency-Replayed = true, want a fresh answer")
		}
		return nil
	})
}

// field decodes the last response and returns the value at a dotted path, or
// an error when it is absent.
func field(a *apiRig, path string) (any, error) {
	m, err := a.decoded()
	if err != nil {
		return nil, err
	}
	got, ok := lookup(m, path)
	if !ok {
		return nil, fmt.Errorf("the response has no field %q (%s)", path, a.body)
	}
	return got, nil
}

func registerFieldSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response field "([^"]*)" is "([^"]*)"$`, func(path, want string) error {
		got, err := field(w.rig(), path)
		if err != nil {
			return err
		}
		if text, isText := got.(string); isText && text != want {
			return fmt.Errorf("%s = %q, want %q", path, text, want)
		}
		if fmt.Sprint(got) != want {
			return fmt.Errorf("%s = %v, want %q", path, got, want)
		}
		return nil
	})
	sc.Step(`^the response field "([^"]*)" is (-?\d+)$`, func(path string, want int) error {
		got, err := field(w.rig(), path)
		if err != nil {
			return err
		}
		n, isNumber := got.(float64)
		if !isNumber || int(n) != want {
			return fmt.Errorf("%s = %v, want %d", path, got, want)
		}
		return nil
	})
	sc.Step(`^the response field "([^"]*)" is (true|false)$`, func(path, want string) error {
		got, err := field(w.rig(), path)
		if err != nil {
			return err
		}
		b, isBool := got.(bool)
		if !isBool || strconv.FormatBool(b) != want {
			return fmt.Errorf("%s = %v, want %s", path, got, want)
		}
		return nil
	})
}

func registerShapeSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response has no field "([^"]*)"$`, func(path string) error {
		m, err := w.rig().decoded()
		if err != nil {
			return err
		}
		if _, ok := lookup(m, path); ok {
			return fmt.Errorf("the response has a field %q (%s)", path, w.rig().body)
		}
		return nil
	})
	sc.Step(`^the response list "([^"]*)" has (\d+) items?$`, func(path string, want int) error {
		m, err := w.rig().decoded()
		if err != nil {
			return err
		}
		got, ok := lookup(m, path)
		if !ok {
			return fmt.Errorf("the response has no list %q (%s)", path, w.rig().body)
		}
		l, isList := got.([]any)
		if !isList || len(l) != want {
			return fmt.Errorf("list %q has %v entries, want %d", path, got, want)
		}
		return nil
	})
}

// registerEventSteps registers the assertions on the OUTBOX the HTTP handlers
// filled through the same use cases: the event names via the existing steps of
// world_test.go, the key fields here.
func registerEventSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the last "([A-Za-z]+)" event has "([A-Za-z]+)" "([^"]*)"$`, func(name, field, want string) error {
		events := w.store.Outbox().Events()
		for i := len(events) - 1; i >= w.eventsBefore; i-- {
			if events[i].EventName() != name {
				continue
			}
			return assertEventField(events[i], name, field, want)
		}
		return fmt.Errorf("no %s event was raised by the last action", name)
	})
}

func assertEventField(event any, name, field, want string) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	got, ok := lookup(decoded, field)
	if !ok {
		return fmt.Errorf("the %s event has no field %q (%s)", name, field, raw)
	}
	if text, isText := got.(string); isText && text != want {
		return fmt.Errorf("%s.%s = %q, want %q", name, field, text, want)
	}
	if fmt.Sprint(got) != want {
		return fmt.Errorf("%s.%s = %v, want %q", name, field, got, want)
	}
	return nil
}
