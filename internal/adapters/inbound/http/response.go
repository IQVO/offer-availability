package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// maxBodyBytes bounds a request body; every body here is a few kilobytes.
const maxBodyBytes = 64 << 10

type statusBody struct {
	Status string `json:"status"`
}

// problemDetail is the RFC 9457 application/problem+json body.
type problemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

const (
	contentJSON    = "application/json"
	contentProblem = "application/problem+json"
)

// writeJSON encodes v as the response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON strictly decodes r's body into dest: unknown fields, trailing
// data, an empty body and malformed JSON are errMalformedRequest.
func decodeJSON(r *http.Request, dest any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %v", errMalformedRequest, err)
	}
	if len(raw) > maxBodyBytes {
		return errTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: a JSON body is required", errMalformedRequest)
		}
		return fmt.Errorf("%w: %v", errMalformedRequest, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after the JSON body", errMalformedRequest)
	}
	return nil
}

// writeError writes err as its RFC 9457 problem. An unmapped error is a 500
// whose detail never leaks the internal message (it is logged instead).
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	p := problemFor(err)
	detail := err.Error()
	if p == internalProblem {
		slog.ErrorContext(r.Context(), "request failed", "error", err, "method", r.Method, "path", r.URL.Path)
		detail = "an unexpected error occurred"
	}
	writeProblem(w, r, p, detail)
}

// writeProblem writes p with an explicit detail.
func writeProblem(w http.ResponseWriter, r *http.Request, p problem, detail string) {
	writeProblemAt(w, p, detail, r.URL.EscapedPath())
}

// writeProblemAt writes p with an explicit detail and instance (the FAILED feed
// answers with the instance of the feed it recorded, not of the POST).
func writeProblemAt(w http.ResponseWriter, p problem, detail, instance string) {
	w.Header().Set("Content-Type", contentProblem)
	w.WriteHeader(p.status)
	_ = json.NewEncoder(w).Encode(problemDetail{
		Type: p.slug, Title: p.title, Status: p.status, Detail: detail, Instance: instance,
	})
}
