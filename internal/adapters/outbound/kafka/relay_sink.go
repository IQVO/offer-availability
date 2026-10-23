package kafka

import (
	"context"
	"fmt"
	"time"

	segmentio "github.com/segmentio/kafka-go"

	"github.com/claudioed/offer-availability/internal/application/outbox"
)

// Writer is the subset of *segmentio.Writer RelaySink depends on, so unit tests
// can substitute a fake.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...segmentio.Message) error
}

// RelaySink writes already-encoded outbox messages to the topic each names. Its
// Writer has NO fixed Topic (kafka-go requires exactly one of Writer.Topic /
// Message.Topic): NewWriter builds it with RequireAll acks, the Hash balancer on
// the key (the aggregate id, so one aggregate's events stay ordered on one
// partition) and AllowAutoTopicCreation. Constructing it never dials.
type RelaySink struct {
	writer Writer
}

// NewRelaySink constructs a RelaySink over the comma-separated brokers. It does
// not dial.
func NewRelaySink(brokers string) *RelaySink {
	w := NewWriter(brokers)
	w.BatchTimeout = 10 * time.Millisecond
	return NewRelaySinkWithWriter(w)
}

// NewRelaySinkWithWriter constructs a RelaySink over an explicit Writer (a fake
// in tests). The writer must NOT have a Topic set.
func NewRelaySinkWithWriter(w Writer) *RelaySink { return &RelaySink{writer: w} }

// Send writes msgs in one WriteMessages call.
func (s *RelaySink) Send(ctx context.Context, msgs ...outbox.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]segmentio.Message, len(msgs))
	for i, m := range msgs {
		if m.Topic == "" {
			return fmt.Errorf("kafka relay sink: message %d (%s) has no topic", i, m.EventType)
		}
		headers := make([]segmentio.Header, len(m.Headers))
		for j, h := range m.Headers {
			headers[j] = segmentio.Header{Key: h.Key, Value: []byte(h.Value)}
		}
		out[i] = segmentio.Message{Topic: m.Topic, Key: m.Key, Value: m.Value, Headers: headers}
	}
	if err := s.writer.WriteMessages(ctx, out...); err != nil {
		return fmt.Errorf("kafka relay sink: write %d message(s): %w", len(out), err)
	}
	return nil
}

// Close releases the underlying Kafka writer.
func (s *RelaySink) Close() error {
	if w, ok := s.writer.(*segmentio.Writer); ok {
		return w.Close()
	}
	return nil
}
