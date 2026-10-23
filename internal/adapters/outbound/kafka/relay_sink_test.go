package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"

	segmentio "github.com/segmentio/kafka-go"

	"github.com/claudioed/offer-availability/internal/application/outbox"
)

// recordingWriter is a Writer double: it captures every batch it is asked to
// write and can be told to fail.
type recordingWriter struct {
	err     error
	batches [][]segmentio.Message
}

func (w *recordingWriter) WriteMessages(_ context.Context, msgs ...segmentio.Message) error {
	w.batches = append(w.batches, msgs)
	return w.err
}

func TestSendWithNoMessagesIsANoOp(t *testing.T) {
	w := &recordingWriter{}
	if err := NewRelaySinkWithWriter(w).Send(context.Background()); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if len(w.batches) != 0 {
		t.Fatalf("the writer was called %d times, want 0", len(w.batches))
	}
}

func TestSendMapsOutboxMessagesOntoKafkaMessages(t *testing.T) {
	w := &recordingWriter{}
	msgs := []outbox.Message{{
		EventID: "id-1", Topic: "offer-availability.listing.v1", EventType: "com.retailnetwork.commerce.offer-availability.listing.ListingCreated",
		Subject: "lst-1", Key: []byte("lst-1"), Value: []byte(`{"id":"id-1"}`),
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json"}, {Key: "specversion", Value: "1.0"}},
	}, {
		EventID: "id-2", Topic: "offer-availability.catalogitem.v1", EventType: "com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemCreated",
		Subject: "item-1", Key: []byte("item-1"), Value: []byte(`{"id":"id-2"}`),
	}}
	if err := NewRelaySinkWithWriter(w).Send(context.Background(), msgs...); err != nil {
		t.Fatalf("Send() = %v, want nil", err)
	}
	if len(w.batches) != 1 || len(w.batches[0]) != 2 {
		t.Fatalf("batches = %v, want one batch of two messages", w.batches)
	}
	for i, want := range []struct{ topic, key, value string }{
		{"offer-availability.listing.v1", "lst-1", `{"id":"id-1"}`},
		{"offer-availability.catalogitem.v1", "item-1", `{"id":"id-2"}`},
	} {
		got := w.batches[0][i]
		if got.Topic != want.topic || string(got.Key) != want.key || string(got.Value) != want.value {
			t.Errorf("message %d = %+v, want topic %s key %s value %s", i, got, want.topic, want.key, want.value)
		}
	}
	first := w.batches[0][0]
	if len(first.Headers) != 2 || first.Headers[0].Key != "content-type" || string(first.Headers[0].Value) != "application/cloudevents+json" {
		t.Errorf("headers = %v, want the outbox headers copied", first.Headers)
	}
}

func TestSendRefusesAMessageWithoutATopic(t *testing.T) {
	w := &recordingWriter{}
	err := NewRelaySinkWithWriter(w).Send(context.Background(), outbox.Message{EventType: "e", Key: []byte("k")})
	if err == nil || !strings.Contains(err.Error(), "message 0 (e) has no topic") {
		t.Fatalf("Send() = %v, want the no-topic error", err)
	}
	if len(w.batches) != 0 {
		t.Fatalf("nothing must be written: %v", w.batches)
	}
}

func TestSendWrapsATransportFailure(t *testing.T) {
	w := &recordingWriter{err: errors.New("broker unreachable")}
	err := NewRelaySinkWithWriter(w).Send(context.Background(), outbox.Message{Topic: "t", Key: []byte("k")})
	if err == nil || !strings.Contains(err.Error(), "kafka relay sink: write 1 message(s)") {
		t.Fatalf("Send() = %v, want the wrapped write error", err)
	}
}

func TestCloseOfAFakeWriterIsANoOp(t *testing.T) {
	if err := NewRelaySinkWithWriter(&recordingWriter{}).Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
}

func TestNewRelaySinkBuildsAndClosesWithoutDialling(t *testing.T) {
	// Constructing the sink must not touch the network, and closing an unused
	// segmentio.Writer is safe: both return without a broker.
	s := NewRelaySink("b1:9092,b2:9092")
	if s == nil || s.writer == nil {
		t.Fatalf("NewRelaySink = %+v, want a sink over a writer", s)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
}
