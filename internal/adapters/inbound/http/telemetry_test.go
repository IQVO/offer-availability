package http

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recordSpans installs a recording TracerProvider for the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

// loggedRig is the usual rig with its router rebuilt over logger.
func loggedRig(t *testing.T, logger *slog.Logger) *rig {
	t.Helper()
	r := newRig(t)
	r.server.Logger = logger
	h, err := NewRouter(r.server)
	if err != nil {
		t.Fatal(err)
	}
	r.handler = h
	return r
}

// logLines decodes every JSON line the logger wrote.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestRouterServerSpanIsNamedAfterTheRoutePattern(t *testing.T) {
	recorder := recordSpans(t)
	r := loggedRig(t, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))

	r.do(http.MethodGet, "/catalog/item-with-a-distinct-id", "")

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", spans[0].SpanKind())
	}
	if got := spans[0].Name(); !strings.HasSuffix(got, "/catalog/{catalogItemId}") {
		t.Errorf("span name = %q, want the route pattern /catalog/{catalogItemId} (one name per route, not per item id)", got)
	}
}

// The trace_id stamping itself (the telemetry handler turning the request
// context into trace_id/span_id) is covered in the telemetry package and, end to
// end, by cmd/api's TestRouterLogCarriesTheServerSpanTraceID; this test pins the
// shape of the request line.
func TestRouterRequestLogShape(t *testing.T) {
	recorder := recordSpans(t)
	var buf bytes.Buffer
	r := loggedRig(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	res := r.do(http.MethodGet, "/catalog/missing", "")
	if res.code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown item", res.code)
	}

	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines, want exactly the request line: %v", len(lines), lines)
	}
	line := lines[0]
	if line["msg"] != "http request" || line["method"] != "GET" || line["status"] != float64(404) {
		t.Errorf("request line = %v", line)
	}
	if line["level"] != "WARN" {
		t.Errorf("level = %v, want WARN for a 4xx", line["level"])
	}
	if line["route"] != "/catalog/{catalogItemId}" {
		t.Errorf("route = %v, want the pattern, never the raw path", line["route"])
	}
	if id, _ := line["request_id"].(string); id == "" {
		t.Error("request_id is empty: RequestID must run before RequestLogger")
	}
	for _, key := range []string{"duration_ms", "bytes"} {
		if _, ok := line[key]; !ok {
			t.Errorf("request line has no %s", key)
		}
	}
	if len(recorder.Ended()) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(recorder.Ended()))
	}
}

func TestRouterRequestLogLevelFollowsTheStatus(t *testing.T) {
	var buf bytes.Buffer
	r := loggedRig(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	r.createItem()                               // 201 -> INFO
	r.do(http.MethodGet, "/catalog/missing", "") // 404 -> WARN

	levels := map[float64]any{}
	for _, l := range logLines(t, &buf) {
		levels[l["status"].(float64)] = l["level"]
	}
	if levels[201] != "INFO" || levels[404] != "WARN" {
		t.Errorf("levels by status = %v, want 201 INFO and 404 WARN", levels)
	}
}

func TestRouterDoesNotLogProbes(t *testing.T) {
	var buf bytes.Buffer
	r := loggedRig(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	r.do(http.MethodGet, "/healthz", "")
	r.do(http.MethodGet, "/readyz", "")

	if buf.Len() != 0 {
		t.Errorf("probes were logged: %s", buf.String())
	}
}
