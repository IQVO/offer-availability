package http

import (
	"net/http"
	"strconv"
	"testing"
)

// TestRetiredRoutesAreGone pins the REST guideline cutover: the :verb routes
// answer 404 or 405, there is no alias.
func TestRetiredRoutesAreGone(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/catalog-items/item-1:discontinue"},
		{http.MethodPost, "/catalog-items/item-1/packaging/EACH:retire"},
	} {
		got := r.do(c.method, c.path, "")
		if got.code != http.StatusNotFound && got.code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", c.method, c.path, got.code)
		}
	}
}

func TestCollectionRejectsRetiredAndInvalidQueryParameters(t *testing.T) {
	r := newRig(t)
	for target, slug := range map[string]string{
		"/catalog?after=x":           "unknown-query-parameter",
		"/catalog?offset=1":          "unknown-query-parameter",
		"/catalog?nextToken=x":       "unknown-query-parameter",
		"/listings?after=x":          "unknown-query-parameter",
		"/listings?nodeId=n&from":    "unknown-query-parameter",
		"/catalog?cursor=!!!":        "invalid-cursor",
		"/catalog?cursor=a&cursor=b": "invalid-cursor",
		"/listings?cursor=bm90":      "invalid-cursor",
		"/catalog?limit=":            "invalid-limit",
		"/catalog?limit=501":         "invalid-limit",
		"/catalog?limit=1&limit=2":   "invalid-limit",
	} {
		r.do(http.MethodGet, target, "").problem(t, http.StatusBadRequest, slug)
	}
}

// walkIDs follows nextCursor of a collection and returns every row id in order.
func walkIDs(t *testing.T, r *rig, base, idField string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		target := base + "?limit=3"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		body := r.do(http.MethodGet, target, "").json()
		for _, it := range body["items"].([]any) {
			id, _ := it.(map[string]any)[idField].(string)
			ids = append(ids, id)
		}
		cursor, _ = body["nextCursor"].(string)
		if cursor == "" {
			return ids
		}
	}
	t.Fatalf("%s: more than 20 pages", base)
	return nil
}

// TestPagesWalkEveryRowOnce walks both collections page by page: no duplicate, no skip.
func TestPagesWalkEveryRowOnce(t *testing.T) {
	r := newRig(t)
	r.activate("node-1")
	const n = 7
	for i := 0; i < n; i++ {
		item := r.createItem()
		r.do(http.MethodPost, "/listings", `{"catalogItemId":"`+item+`","nodeId":"node-`+strconv.Itoa(i%2+1)+`"}`)
	}
	for base, field := range map[string]string{"/catalog": "catalogItemId", "/listings": "listingId"} {
		seen := map[string]bool{}
		for _, id := range walkIDs(t, r, base, field) {
			if seen[id] {
				t.Fatalf("%s: %s seen twice", base, id)
			}
			seen[id] = true
		}
		if len(seen) != n {
			t.Errorf("%s: walked %d rows, want %d", base, len(seen), n)
		}
	}
}
