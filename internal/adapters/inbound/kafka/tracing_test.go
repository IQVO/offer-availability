package kafka

import (
	"context"
	"errors"
	"sync"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/claudioed/offer-availability/internal/application/usecases"
)

const publisherTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// tracedRun feeds one node-topology message with the given headers through
// NodeConsumer.Run and returns the span context the use case saw and the
// consumer's ended spans. The use case fails with failWith when it is non-nil
// (the loop then retries the same message, so the run stops after the first
// ended span).
func tracedRun(t *testing.T, headers []kafkago.Header, failWith error) (trace.SpanContext, []sdktrace.ReadOnlySpan) {
	t.Helper()
	tp, prop := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTracerProvider(tp); otel.SetTextMapPropagator(prop) })
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var (
		mu   sync.Mutex
		seen trace.SpanContext
	)
	uc := useCaseFunc(func(ctx context.Context, _ usecases.NodeEvent) error {
		mu.Lock()
		defer mu.Unlock()
		seen = trace.SpanContextFromContext(ctx)
		return failWith
	})
	m := msg(0, envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`))
	m.Headers = headers
	reader := &fakeReader{msgs: []kafkago.Message{m}}
	c := &NodeConsumer{Reader: reader, UseCase: uc, Logger: discard()}
	err := runUntil(t, c, func() bool { return len(recorder.Ended()) > 0 && (failWith != nil || len(reader.commits()) == 1) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return seen, recorder.Ended()
}

// The consumer span is a consumer-kind span named "<topic> process" whose parent
// is the publisher's span carried in traceparent, and the use case runs inside it.
func TestConsumerSpanIsParentedToThePublisherTrace(t *testing.T) {
	seen, spans := tracedRun(t, []kafkago.Header{
		{Key: "content-type", Value: []byte("application/cloudevents+json; charset=UTF-8")},
		{Key: "traceparent", Value: []byte(publisherTraceparent)},
	}, nil)

	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.SpanKind() != trace.SpanKindConsumer || span.Name() != NodeTopic+" process" {
		t.Errorf("span = %v %q, want consumer %q", span.SpanKind(), span.Name(), NodeTopic+" process")
	}
	if got := span.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id = %s, want the publisher's", got)
	}
	if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Errorf("parent span id = %s, want the publish span's", got)
	}
	if seen.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("use case ran under span %s, want the consumer span %s", seen.SpanID(), span.SpanContext().SpanID())
	}
}

func TestConsumerWithoutTraceHeadersStartsANewTrace(t *testing.T) {
	_, spans := tracedRun(t, nil, nil)
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if spans[0].Parent().IsValid() {
		t.Errorf("parent = %v, want none for a message without traceparent", spans[0].Parent())
	}
}

func TestConsumerSpanRecordsAFailure(t *testing.T) {
	_, spans := tracedRun(t, []kafkago.Header{{Key: "traceparent", Value: []byte(publisherTraceparent)}}, errors.New("deadlock detected"))
	if len(spans) == 0 || spans[0].Status().Code != codes.Error {
		t.Fatalf("spans = %v, want an errored consumer span", spans)
	}
}

func TestHeaderCarrierReadsHeaders(t *testing.T) {
	c := headerCarrier{headers: []kafkago.Header{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}}}
	c.Set("a", "ignored") // read-only: a consumer never re-injects into the message it read
	if c.Get("a") != "1" || c.Get("absent") != "" {
		t.Fatalf("Get = %q / %q", c.Get("a"), c.Get("absent"))
	}
	if keys := c.Keys(); len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("Keys = %v", keys)
	}
}
