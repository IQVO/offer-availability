package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	inbounda2a "github.com/claudioed/offer-availability/internal/adapters/inbound/a2a"
)

// newTestRouter builds the router exactly as run() does: the real executor
// (empty deps are fine: no skill that touches a use case is invoked here)
// and the real Agent Card.
func newTestRouter() http.Handler {
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: "test"})
	return newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(inbounda2a.Deps{})), card, quietLogger(), "offer-availability-a2a")
}

func TestRouter_HealthzServesOK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

func TestRouter_ServesTheAgentCardAtTheWellKnownPath(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET agent card: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"offer-availability"`, testAgentURL, `"check_availability"`, `"feed_outcome"`} {
		if !strings.Contains(body, want) {
			t.Errorf("agent card lacks %s: %s", want, body)
		}
	}
}

// TestRouter_BothMountsReachTheJSONRPCHandler pins the fleet's
// <CTX>_A2A_ENDPOINT convention: the endpoint answers at "/" and at "/a2a",
// without credentials (and ignoring any Authorization header).
func TestRouter_BothMountsReachTheJSONRPCHandler(t *testing.T) {
	h := newTestRouter()
	for _, path := range []string{"/", "/a2a"} {
		for _, auth := range []string{"", "Bearer nonsense"} {
			t.Run(path+"/"+auth, func(t *testing.T) {
				// An unknown skill is answered in-band before any use case runs,
				// so a JSON-RPC result proves the request reached the SDK
				// handler at this mount point.
				body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"launch_rocket","arguments":{}}}]}}}`
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("A2A-Version", "1.0")
				if auth != "" {
					req.Header.Set("Authorization", auth)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				raw, _ := io.ReadAll(rec.Body)
				if rec.Code == http.StatusNotFound || !strings.Contains(string(raw), `"jsonrpc"`) {
					t.Fatalf("POST %s: %d %q does not look like a JSON-RPC reply", path, rec.Code, raw)
				}
				if !strings.Contains(string(raw), "unknown-skill") {
					t.Fatalf("POST %s: reply %q lacks the in-band unknown-skill problem", path, raw)
				}
			})
		}
	}
}

// Nothing but the documented paths is served.
func TestRouter_UnknownPathIsNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /mcp = %d, want 404: the A2A binary serves no MCP endpoint", rec.Code)
	}
}
