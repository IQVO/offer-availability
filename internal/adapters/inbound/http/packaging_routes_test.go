package http

import (
	"net/http"
	"testing"
)

const (
	pEach  = "06141410000120"
	pInner = "06141410000298"
	pOther = "06141410000571"
)

func newItem(t *testing.T, r *rig, extra string) string {
	t.Helper()
	res := r.do(http.MethodPost, "/catalog-items", `{"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":true`+extra+`}`)
	if res.code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.code, res.body)
	}
	return res.json()["catalogItemId"].(string)
}

func TestPackagingRoutes(t *testing.T) {
	r := newRig(t)
	id := newItem(t, r, `,"identifiers":{"gtin":"`+pEach+`"}`)
	if got := r.do(http.MethodGet, "/catalog/"+id, "").json(); got["identifiers"].(map[string]any)["gtin"] != pEach {
		t.Fatalf("identifiers = %v", got["identifiers"])
	}
	put := func(level, body string) response {
		return r.do(http.MethodPut, "/catalog-items/"+id+"/packaging/"+level, body)
	}
	if res := put("EACH", `{"gtin":"`+pEach+`","unitsPerPack":1}`); res.code != http.StatusOK {
		t.Fatalf("each: %d %s", res.code, res.body)
	}
	res := put("INNER", `{"gtin":"`+pInner+`","childGtin":"`+pEach+`","unitsPerPack":6,"dimsMm":{"lengthMm":120,"widthMm":80,"heightMm":80},"grossWeightG":2100}`)
	if res.code != http.StatusOK || res.header.Get("Content-Type") != "application/json" {
		t.Fatalf("inner: %d %s", res.code, res.body)
	}
	levels := res.json()["packaging"].([]any)
	inner := levels[1].(map[string]any)
	if len(levels) != 2 || inner["level"] != "INNER" || inner["unitsPerPack"] != float64(6) || inner["childGtin"] != pEach ||
		inner["grossWeightG"] != float64(2100) || inner["dimsMm"].(map[string]any)["lengthMm"] != float64(120) || inner["active"] != true {
		t.Fatalf("packaging = %v", levels)
	}
	if got := r.do(http.MethodGet, "/catalog/"+id, "").json()["packaging"].([]any); len(got) != 2 {
		t.Fatalf("GET lacks the packaging: %v", got)
	}

	assertRetire(t, r, id)
}

func assertRetire(t *testing.T, r *rig, id string) {
	t.Helper()
	ret := r.do(http.MethodPost, "/catalog-items/"+id+"/packaging/INNER:retire", "")
	if ret.code != http.StatusOK || ret.json()["packaging"].([]any)[1].(map[string]any)["active"] != false {
		t.Fatalf("retire: %d %s", ret.code, ret.body)
	}
	if again := r.do(http.MethodPost, "/catalog-items/"+id+"/packaging/INNER:retire", ""); again.code != http.StatusOK {
		t.Fatalf("retiring twice: %d %s", again.code, again.body)
	}
}

func TestPackagingErrors(t *testing.T) {
	r := newRig(t)
	id := newItem(t, r, "")
	newItem(t, r, `,"identifiers":{"gtin":"`+pOther+`"}`)
	base := "/catalog-items/" + id + "/packaging/"
	good := `{"gtin":"` + pEach + `","unitsPerPack":1}`
	r.do(http.MethodPut, base+"EACH", good)
	for _, c := range []struct {
		name, method, path, body string
		status                   int
		slug                     string
	}{
		{"unknown item", http.MethodPut, "/catalog-items/none/packaging/EACH", good, 404, "not-found"},
		{"unknown item retire", http.MethodPost, "/catalog-items/none/packaging/EACH:retire", "", 404, "not-found"},
		{"bad level", http.MethodPut, base + "BOX", good, 422, "invalid-pack-level"},
		{"bad level retire", http.MethodPost, base + "BOX:retire", "", 422, "invalid-pack-level"},
		{"lower case level", http.MethodPut, base + "each", good, 422, "invalid-pack-level"},
		{"bad check digit", http.MethodPut, base + "INNER", `{"gtin":"06141410000121","childGtin":"` + pEach + `","unitsPerPack":6}`, 422, "invalid-gtin"},
		{"no child", http.MethodPut, base + "INNER", `{"gtin":"` + pInner + `","unitsPerPack":6}`, 422, "pack-level-order"},
		{"units below 2", http.MethodPut, base + "INNER", `{"gtin":"` + pInner + `","childGtin":"` + pEach + `","unitsPerPack":1}`, 422, "invalid-pack-quantity"},
		{"partial dims", http.MethodPut, base + "EACH", `{"gtin":"` + pEach + `","unitsPerPack":1,"dimsMm":{"lengthMm":5}}`, 422, "invalid-pack-dimensions"},
		{"gtin of another item", http.MethodPut, base + "INNER", `{"gtin":"` + pOther + `","childGtin":"` + pEach + `","unitsPerPack":6}`, 409, "gtin-already-assigned"},
		{"never declared", http.MethodPost, base + "PALLET:retire", "", 404, "pack-level-not-found"},
		{"malformed body", http.MethodPut, base + "EACH", `{`, 400, "malformed-request-body"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r.do(c.method, c.path, c.body).problem(t, c.status, c.slug)
		})
	}
	r.do(http.MethodPost, "/catalog-items", `{"title":"Dup","lengthMm":1,"widthMm":1,"heightMm":1,"weightG":1,"identifiers":{"gtin":"`+pOther+`"}}`).
		problem(t, 409, "gtin-already-assigned")
	r.do(http.MethodPost, "/catalog-items", `{"title":"Bad","lengthMm":1,"widthMm":1,"heightMm":1,"weightG":1,"identifiers":{"gtin":"1234"}}`).
		problem(t, 422, "invalid-gtin")
	// immutable units per pack
	r.do(http.MethodPut, base+"INNER", `{"gtin":"06141410000502","childGtin":"`+pEach+`","unitsPerPack":6}`)
	r.do(http.MethodPut, base+"INNER", `{"gtin":"06141410000502","childGtin":"`+pEach+`","unitsPerPack":8}`).
		problem(t, 409, "pack-quantity-immutable")
}

func TestListingOwnerRefRoute(t *testing.T) {
	r := newRig(t)
	id := newItem(t, r, "")
	r.activate("node-1")
	post := func(owner string) response {
		body := `{"catalogItemId":"` + id + `","nodeId":"node-1"` + owner + `}`
		return r.do(http.MethodPost, "/listings", body)
	}
	plain := post("")
	if plain.code != http.StatusCreated {
		t.Fatalf("plain: %d %s", plain.code, plain.body)
	}
	if _, has := plain.json()["ownerRef"]; has {
		t.Fatalf("an operator-owned listing must not carry ownerRef: %s", plain.body)
	}
	owned := post(`,"ownerRef":"seller-7"`)
	if owned.code != http.StatusCreated || owned.json()["ownerRef"] != "seller-7" {
		t.Fatalf("owned: %d %s", owned.code, owned.body)
	}
	post(`,"ownerRef":"seller-7"`).problem(t, 409, "duplicate-listing")
	post("").problem(t, 409, "duplicate-listing")
	post(`,"ownerRef":"bad owner"`).problem(t, 422, "validation-failed")
}
