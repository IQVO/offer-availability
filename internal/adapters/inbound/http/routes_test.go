package http

import (
	"net/http"
	"strings"
	"testing"
)

func TestCatalogRoutes(t *testing.T) {
	r := newRig(t)
	res := r.do(http.MethodPost, "/catalog-items", `{"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":true}`)
	if res.code != http.StatusCreated || res.header.Get("Content-Type") != "application/json" {
		t.Fatalf("create: %d %s", res.code, res.body)
	}
	item := res.json()
	if item["state"] != "ACTIVE" || item["title"] != "Bottle" || item["fragile"] != true {
		t.Fatalf("created item = %v", item)
	}
	id := item["catalogItemId"].(string)

	if got := r.do(http.MethodGet, "/catalog/"+id, ""); got.code != http.StatusOK || got.json()["catalogItemId"] != id {
		t.Fatalf("get: %d %s", got.code, got.body)
	}
	page := r.do(http.MethodGet, "/catalog?limit=1", "").json()
	if len(page["items"].([]any)) != 1 {
		t.Fatalf("browse = %v", page)
	}

	// The colon verb of the OpenAPI path routes to the use case.
	disc := r.do(http.MethodPost, "/catalog-items/"+id+":discontinue", "")
	if disc.code != http.StatusOK || disc.json()["state"] != "DISCONTINUED" {
		t.Fatalf("discontinue: %d %s", disc.code, disc.body)
	}
	if again := r.do(http.MethodPost, "/catalog-items/"+id+":discontinue", ""); again.code != http.StatusOK {
		t.Fatalf("discontinuing twice is a no-op: %d %s", again.code, again.body)
	}
}

func TestCatalogErrors(t *testing.T) {
	r := newRig(t)
	r.do(http.MethodGet, "/catalog/none", "").problem(t, http.StatusNotFound, "not-found")
	r.do(http.MethodPost, "/catalog-items/none:discontinue", "").problem(t, http.StatusNotFound, "not-found")
	r.do(http.MethodPost, "/catalog-items", `{"title":"Bottle","lengthMm":0,"widthMm":75,"heightMm":75,"weightG":340}`).
		problem(t, http.StatusUnprocessableEntity, "invalid-item-dimensions")
	r.do(http.MethodPost, "/catalog-items", `{"title":"","lengthMm":1,"widthMm":1,"heightMm":1,"weightG":1}`).
		problem(t, http.StatusUnprocessableEntity, "validation-failed")
	r.do(http.MethodGet, "/catalog?limit=0", "").problem(t, http.StatusUnprocessableEntity, "validation-failed")
	r.do(http.MethodGet, "/catalog?limit=abc", "").problem(t, http.StatusUnprocessableEntity, "validation-failed")
}

func TestListingRoutes(t *testing.T) {
	r := newRig(t)
	r.activate("node-1")
	item := r.createItem()
	res := r.do(http.MethodPost, "/listings", `{"catalogItemId":"`+item+`","nodeId":"node-1"}`)
	if res.code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.code, res.body)
	}
	l := res.json()
	av := l["availability"].(map[string]any)
	if l["state"] != "LIVE" || av["availableQty"] != float64(0) || av["basis"] != "UNKNOWN" || av["asOf"] != nil {
		t.Fatalf("listing = %v", l)
	}
	cold := r.do(http.MethodPost, "/listings", `{"catalogItemId":"`+item+`","nodeId":"node-2"}`).json()
	if cold["state"] != "SUPPRESSED" || cold["suppressionReason"] != "NODE_NOT_SELLABLE" {
		t.Fatalf("listing on an unknown node = %v", cold)
	}

	for target, want := range map[string]int{
		"/listings":                           2,
		"/listings?nodeId=node-1":             1,
		"/listings?catalogItemId=" + item:     2,
		"/listings?nodeId=node-2&limit=1":     1,
		"/listings?nodeId=nowhere":            0,
		"/listings?catalogItemId=none&after=": 0,
	} {
		got := r.do(http.MethodGet, target, "")
		if got.code != http.StatusOK || len(got.json()["listings"].([]any)) != want {
			t.Errorf("GET %s: %d %s, want %d listings", target, got.code, got.body, want)
		}
	}
	r.do(http.MethodPost, "/listings", `{"catalogItemId":"`+item+`","nodeId":"node-1"}`).problem(t, http.StatusConflict, "duplicate-listing")
	r.do(http.MethodPost, "/listings", `{"catalogItemId":"none","nodeId":"node-1"}`).problem(t, http.StatusNotFound, "not-found")
	r.do(http.MethodPost, "/listings", `{"catalogItemId":"`+item+`","nodeId":"bad node"}`).problem(t, http.StatusUnprocessableEntity, "validation-failed")
	r.do(http.MethodGet, "/listings?limit=x", "").problem(t, http.StatusUnprocessableEntity, "validation-failed")
}

func feedBody(feed, node, asOf string, full bool, lines ...string) string {
	f := "false"
	if full {
		f = "true"
	}
	return `{"feedId":"` + feed + `","nodeId":"` + node + `","fullUpdate":` + f + `,"asOf":"` + asOf + `","lines":[` + strings.Join(lines, ",") + `]}`
}

func line(listing string, qty int, basis string) string {
	return `{"listingId":"` + listing + `","availableQty":` + itoa(qty) + `,"basis":"` + basis + `"}`
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func TestFeedRoutes(t *testing.T) {
	r := newRig(t)
	r.activate("node-1")
	item := r.createItem()
	lst := r.createListing(item, "node-1")

	res := r.do(http.MethodPost, "/inventory-feeds", feedBody("f1", "node-1", "2026-10-23T10:00:00Z", false, line(lst, 120, "THROUGHPUT_CONSTRAINED")))
	if res.code != http.StatusCreated {
		t.Fatalf("apply: %d %s", res.code, res.body)
	}
	f := res.json()
	if f["state"] != "APPLIED" || f["appliedCount"] != float64(1) || f["staleCount"] != float64(0) || f["lineCount"] != float64(1) {
		t.Fatalf("feed = %v", f)
	}
	if got := r.do(http.MethodGet, "/inventory-feeds/f1", ""); got.code != http.StatusOK || got.json()["state"] != "APPLIED" {
		t.Fatalf("get: %d %s", got.code, got.body)
	}
	l := r.do(http.MethodGet, "/listings?nodeId=node-1", "").json()["listings"].([]any)[0].(map[string]any)
	av := l["availability"].(map[string]any)
	if av["availableQty"] != float64(120) || av["basis"] != "THROUGHPUT_CONSTRAINED" || av["asOf"] != "2026-10-23T10:00:00Z" || av["feedId"] != "f1" {
		t.Fatalf("availability = %v", av)
	}

	// An older feed is SUPERSEDED (201: it is an outcome, not an error).
	old := r.do(http.MethodPost, "/inventory-feeds", feedBody("f0", "node-1", "2026-10-23T09:00:00Z", false, line(lst, 1, "PHYSICAL")))
	if old.code != http.StatusCreated || old.json()["state"] != "SUPERSEDED" {
		t.Fatalf("superseded: %d %s", old.code, old.body)
	}
	r.do(http.MethodPost, "/inventory-feeds", feedBody("f1", "node-1", "2026-10-23T11:00:00Z", false, line(lst, 1, "PHYSICAL"))).
		problem(t, http.StatusConflict, "feed-already-applied")
	r.do(http.MethodGet, "/inventory-feeds/none", "").problem(t, http.StatusNotFound, "not-found")
}

func TestFailedFeedIsRecordedAndAnswered422WithItsInstance(t *testing.T) {
	r := newRig(t)
	r.activate("node-1")
	lst := r.createListing(r.createItem(), "node-1")

	p := r.do(http.MethodPost, "/inventory-feeds", feedBody("bad", "node-1", "2026-10-23T10:00:00Z", false, line(lst, -1, "PHYSICAL"))).
		problem(t, http.StatusUnprocessableEntity, "negative-availability")
	if p["instance"] != "/inventory-feeds/bad" {
		t.Fatalf("instance = %v", p["instance"])
	}
	got := r.do(http.MethodGet, "/inventory-feeds/bad", "").json()
	if got["state"] != "FAILED" || got["failureCode"] != "ErrNegativeAvailability" {
		t.Fatalf("stored feed = %v", got)
	}
	r.do(http.MethodPost, "/inventory-feeds", feedBody("other", "node-1", "2026-10-23T10:00:00Z", false, line("lst-elsewhere", 1, "PHYSICAL"))).
		problem(t, http.StatusUnprocessableEntity, "listing-not-on-node")
}

func TestFeedValidation(t *testing.T) {
	r := newRig(t)
	for name, body := range map[string]string{
		"missing lines":      `{"feedId":"f","nodeId":"n","fullUpdate":false,"asOf":"2026-10-23T10:00:00Z"}`,
		"missing fullUpdate": `{"feedId":"f","nodeId":"n","asOf":"2026-10-23T10:00:00Z","lines":[]}`,
		"bad feed id":        feedBody("bad id", "n", "2026-10-23T10:00:00Z", false),
		"empty partial feed": feedBody("f", "n", "2026-10-23T10:00:00Z", false),
		"no asOf":            `{"feedId":"f","nodeId":"n","fullUpdate":false,"lines":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			r.do(http.MethodPost, "/inventory-feeds", body).problem(t, http.StatusUnprocessableEntity, "validation-failed")
		})
	}
	r.do(http.MethodPost, "/inventory-feeds", `{"feedId":"f","nodeId":"n","fullUpdate":false,"asOf":"yesterday","lines":[]}`).
		problem(t, http.StatusBadRequest, "malformed-request-body")
}

func TestMalformedRequests(t *testing.T) {
	r := newRig(t)
	cases := map[string]struct{ method, target, body, slug string }{
		"bad json":       {http.MethodPost, "/listings", `{`, "malformed-request-body"},
		"unknown field":  {http.MethodPost, "/listings", `{"catalogItemId":"a","nodeId":"b","x":1}`, "malformed-request-body"},
		"wrong type":     {http.MethodPost, "/listings", `{"catalogItemId":1,"nodeId":"b"}`, "malformed-request-body"},
		"empty body":     {http.MethodPost, "/listings", ``, "malformed-request-body"},
		"trailing data":  {http.MethodPost, "/listings", `{"catalogItemId":"a","nodeId":"b"} {}`, "malformed-request-body"},
		"nul in body":    {http.MethodPost, "/listings", "{\"catalogItemId\":\"a\\u0000\",\"nodeId\":\"b\"}", "malformed-request-body"},
		"nul in query":   {http.MethodGet, "/listings?nodeId=a%00", ``, "malformed-request-query"},
		"nul in path":    {http.MethodGet, "/catalog/a%00b", ``, "not-found"},
		"too large body": {http.MethodPost, "/listings", `{"catalogItemId":"` + strings.Repeat("a", 70<<10) + `"}`, "request-body-too-large"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res := r.do(c.method, c.target, c.body)
			if res.code < 400 || res.json()["type"] != c.slug {
				t.Fatalf("%d %s, want type %s", res.code, res.body, c.slug)
			}
		})
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	r := newRig(t)
	r.do(http.MethodGet, "/nope", "").problem(t, http.StatusNotFound, "not-found")
	r.do(http.MethodDelete, "/listings", "").problem(t, http.StatusMethodNotAllowed, "method-not-allowed")
	r.do(http.MethodPut, "/catalog-items", "").problem(t, http.StatusMethodNotAllowed, "method-not-allowed")
}

func TestHealthAndReadiness(t *testing.T) {
	r := newRig(t)
	if res := r.do(http.MethodGet, "/healthz", ""); res.code != http.StatusOK || res.json()["status"] != "ok" {
		t.Fatalf("healthz: %d %s", res.code, res.body)
	}
	if res := r.do(http.MethodGet, "/readyz", ""); res.code != http.StatusOK || res.json()["status"] != "ready" {
		t.Fatalf("readyz: %d %s", res.code, res.body)
	}
}

func TestNewRouterRefusesAServerWithoutIdempotency(t *testing.T) {
	if _, err := NewRouter(&Server{}); err != ErrNoIdempotency {
		t.Fatalf("err = %v", err)
	}
}

func TestPanicIsA500Problem(t *testing.T) {
	r := newRig(t)
	r.server.BrowseCatalog = panicking{}
	h, err := NewRouter(r.server)
	if err != nil {
		t.Fatal(err)
	}
	r.handler = h
	p := r.do(http.MethodGet, "/catalog", "").problem(t, http.StatusInternalServerError, "internal-error")
	if p["detail"] != "an unexpected error occurred" {
		t.Fatalf("detail leaks: %v", p)
	}
}
