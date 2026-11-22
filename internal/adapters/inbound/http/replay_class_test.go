package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// allProblemTypes is the catalogue and the standalone problems: every problem
// type the service writes on an idempotent route.
func allProblemTypes() []catalogueEntry {
	return append(append([]catalogueEntry{}, problemCatalogue...), standaloneProblems...)
}

func TestEveryStandaloneProblemDeclaresAReplayClass(t *testing.T) {
	for _, e := range standaloneProblems {
		if e.replay == replayUnset {
			t.Errorf("standalone problem %q declares no replay class", e.p.slug)
		}
	}
}

// One slug, one class: the catalogue and the standalone list never disagree, and a
// 5xx is RETRYABLE (never stored).
func TestAProblemTypeHasExactlyOneClass(t *testing.T) {
	seen := map[string]catalogueEntry{}
	for _, e := range allProblemTypes() {
		if prev, ok := seen[e.p.slug]; ok && (prev.replay != e.replay || prev.record != e.record || prev.p.status != e.p.status) {
			t.Errorf("slug %q is declared twice with different classes or statuses: %v/%v vs %v/%v", e.p.slug, prev.replay, prev.p.status, e.replay, e.p.status)
		}
		seen[e.p.slug] = e
		if e.p.status >= http.StatusInternalServerError && e.replay != replayRetryable {
			t.Errorf("%q answers %d: a 5xx is never stored, so its class is RETRYABLE, got %v", e.p.slug, e.p.status, e.replay)
		}
	}
}

// record is the one exception to "RETRYABLE remembers nothing" and it is pinned: only the
// FAILED feed of listing-not-on-node, only on a RETRYABLE entry.
func TestOnlyTheListingNotOnNodeEntryKeepsARecordWhileFreeingTheKey(t *testing.T) {
	var got []string
	for _, e := range allProblemTypes() {
		if e.record {
			got = append(got, e.p.slug)
			if e.replay != replayRetryable {
				t.Errorf("%q has record set but is %v: record only qualifies RETRYABLE", e.p.slug, e.replay)
			}
		}
	}
	if len(got) != 1 || got[0] != "listing-not-on-node" {
		t.Fatalf("record entries = %v, want [listing-not-on-node]", got)
	}
}

func TestOnlyNegativeAvailabilityIsAnOutcome(t *testing.T) {
	var got []string
	for _, e := range allProblemTypes() {
		if e.replay == replayOutcome {
			got = append(got, e.p.slug)
		}
	}
	if len(got) != 1 || got[0] != "negative-availability" {
		t.Fatalf("OUTCOME entries = %v, want [negative-availability]", got)
	}
}

func TestSettlementIsDecidedByTheClassAndOnlyTwoStatusRanges(t *testing.T) {
	for name, tc := range map[string]struct {
		code   int
		class  replayClass
		record bool
		want   settlement
	}{
		"200 stores":                            {200, replayUnset, false, settleStore},
		"201 stores":                            {201, replayUnset, false, settleStore},
		"204 stores":                            {204, replayUnset, false, settleStore},
		"500 releases":                          {500, replayFinal, false, settleRelease},
		"503 releases whatever its class":       {503, replayOutcome, true, settleRelease},
		"a FINAL refusal is stored":             {409, replayFinal, false, settleFinal},
		"an OUTCOME refusal commits and stores": {422, replayOutcome, false, settleOutcome},
		"a RETRYABLE refusal releases":          {409, replayRetryable, false, settleRelease},
		"a recording RETRYABLE keeps the fact":  {422, replayRetryable, true, settleRecord},
		"a refusal with no class releases":      {409, replayUnset, false, settleRelease},
		"a 3xx with no class releases":          {302, replayUnset, false, settleRelease},
		"record on a FINAL entry is ignored":    {409, replayFinal, true, settleFinal},
	} {
		t.Run(name, func(t *testing.T) {
			if got := settlementOf(tc.code, tc.class, tc.record); got != tc.want {
				t.Errorf("settlementOf(%d, %v, %v) = %v, want %v", tc.code, tc.class, tc.record, got, tc.want)
			}
		})
	}
}

func TestReplayClassNames(t *testing.T) {
	for c, want := range map[replayClass]string{replayUnset: "UNSET", replayFinal: "FINAL", replayOutcome: "OUTCOME", replayRetryable: "RETRYABLE"} {
		if c.String() != want {
			t.Errorf("%d = %q, want %q", c, c.String(), want)
		}
	}
}

// writeError marks the capturing writer with the class of the problem; the FAILED feed
// of POST /inventory-feeds reaches it through the handler, no per-handler call.
func TestTheFailedFeedHandlerMarksTheCaptureWithTheClassOfItsProblem(t *testing.T) {
	r := newRig(t)
	r.activate("N1")
	lst := r.createListing(r.createItem(), "N1")
	for name, tc := range map[string]struct {
		body       string
		feed       string
		slug       string
		class      replayClass
		record     bool
		wantSettle settlement
	}{
		"negative availability": {feedBody("f-neg", "N1", "2026-10-23T10:00:00Z", false, line(lst, -1, "PHYSICAL")), "f-neg", "negative-availability", replayOutcome, false, settleOutcome},
		"listing not on node":   {feedBody("f-miss", "N1", "2026-10-23T10:00:00Z", false, line("lst-unknown", 1, "PHYSICAL")), "f-miss", "listing-not-on-node", replayRetryable, true, settleRecord},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &capture{header: http.Header{}}
			r.server.applyInventoryFeed(rec, httptest.NewRequest(http.MethodPost, "/inventory-feeds", strings.NewReader(tc.body)))
			if rec.code != http.StatusUnprocessableEntity || !strings.Contains(rec.body.String(), `"type":"`+tc.slug+`"`) {
				t.Fatalf("answer = %d %s", rec.code, rec.body.String())
			}
			if !strings.Contains(rec.body.String(), `"instance":"/inventory-feeds/`+tc.feed+`"`) {
				t.Errorf("instance is not the recorded feed %s: %s", tc.feed, rec.body.String())
			}
			if rec.class != tc.class || rec.record != tc.record || rec.settlement() != tc.wantSettle {
				t.Errorf("marked %v record=%v settle=%v, want %v record=%v settle=%v", rec.class, rec.record, rec.settlement(), tc.class, tc.record, tc.wantSettle)
			}
		})
	}
}

// ADR 0016 holds the class table; it must equal the catalogue, slug by slug.
func TestTheADRTableEqualsTheCatalogue(t *testing.T) {
	matches, err := filepath.Glob("../../../../docs/adr/0016-*.md")
	if err != nil || len(matches) != 1 {
		t.Fatalf("ADR 0016 not found: %v %v", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z-]+)` \\| (\\d{3}) \\| (FINAL|OUTCOME|RETRYABLE)( \\(record\\))? \\|")
	inADR := map[string]string{}
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		inADR[m[1]] = m[2] + " " + m[3] + m[4]
	}
	inCode := map[string]string{}
	for _, e := range allProblemTypes() {
		suffix := ""
		if e.record {
			suffix = " (record)"
		}
		inCode[e.p.slug] = itoa(e.p.status) + " " + e.replay.String() + suffix
	}
	for slug, want := range inCode {
		if got, ok := inADR[slug]; !ok || got != want {
			t.Errorf("ADR 0016 row for %q = %q, catalogue says %q", slug, got, want)
		}
	}
	for slug := range inADR {
		if _, ok := inCode[slug]; !ok {
			t.Errorf("ADR 0016 lists %q, which is not a problem type", slug)
		}
	}
}
