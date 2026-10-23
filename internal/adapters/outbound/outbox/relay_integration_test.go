//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	outboundkafka "github.com/claudioed/offer-availability/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/offer-availability/internal/adapters/outbound/outbox"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/application/outbox"
	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

var seq int

func newMessage(topic, key string) outbox.Message {
	seq++
	return outbox.Message{
		EventID: fmt.Sprintf("00000000-0000-4000-8000-%012d", seq), Topic: topic, EventType: "com.example.thing.Happened", Subject: key,
		Key: []byte(key), DataSchema: "urn:example:Happened:v1", Value: []byte(fmt.Sprintf(`{"id":"%s","n":%d}`, key, seq)),
		Headers: []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}},
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestRelayPublishesTheStoredBytesToTheNamedTopic is the Kafka proof of the
// outbox: Postgres rows go out through the real relay and the real Kafka sink and
// come back unchanged, with their key and header, in id order.
func TestRelayPublishesTheStoredBytesToTheNamedTopic(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	topic := fmt.Sprintf("test.relay.%d", time.Now().UnixNano())
	msgs := []outbox.Message{newMessage(topic, "a-1"), newMessage(topic, "a-1"), newMessage(topic, "b-2")}
	if err := repo.Insert(context.Background(), msgs...); err != nil {
		t.Fatal(err)
	}
	sink := outboundkafka.NewRelaySink(kafkatest.BrokerList(t))
	defer func() { _ = sink.Close() }()
	relay := outboxrelay.NewOutboxRelay(repo, sink, quiet(), outboxrelay.WithMaxAttempts(5))

	// The first write to a topic that does not exist yet may fail once with an
	// unknown-topic error; the row stays and the next pass publishes it.
	deadline := time.Now().Add(60 * time.Second)
	for {
		n, err := relay.RelayOnce(context.Background())
		if err == nil && n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the relay never published: %d %v", n, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	pgtest.WaitAllPublished(t, pool)

	got := kafkatest.ReadN(t, topic, len(msgs), 30*time.Second)
	for i, m := range msgs {
		if string(got[i].Value) != string(m.Value) || got[i].Key != string(m.Key) || got[i].Headers["content-type"] != m.Headers[0].Value {
			t.Errorf("message %d = %+v, want the stored %+v", i, got[i], m)
		}
	}
}

// TestACrashAfterTheSendRepublishesTheSameBytes: the relay sent a row but died
// before marking it published (simulated by a send that reports an error after
// writing). The next pass publishes the row again with identical bytes, so the
// CloudEvents id in them is the same and a consumer deduplicates.
func TestACrashAfterTheSendRepublishesTheSameBytes(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	topic := fmt.Sprintf("test.relay.%d", time.Now().UnixNano())
	m := newMessage(topic, "c-3")
	if err := repo.Insert(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	sink := outboundkafka.NewRelaySink(kafkatest.BrokerList(t))
	defer func() { _ = sink.Close() }()

	crash := errors.New("crashed before the UPDATE")
	deadline := time.Now().Add(60 * time.Second)
	for sent := 0; sent == 0; {
		_, err := repo.Drain(context.Background(), 10, 0, func(ctx context.Context, row outbox.Message) error {
			if serr := sink.Send(ctx, row); serr != nil {
				return serr
			}
			sent++
			return crash
		})
		if time.Now().After(deadline) {
			t.Fatalf("never sent: %v", err)
		}
		if sent == 0 {
			// The first write to a not-yet-created topic can fail; every failed
			// try counts as an attempt on the row, so pace the retries.
			time.Sleep(300 * time.Millisecond)
		}
	}
	relay := outboxrelay.NewOutboxRelay(repo, sink, quiet(), outboxrelay.WithMaxAttempts(50))
	if n, err := relay.RelayOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("second pass: %d %v", n, err)
	}
	got := kafkatest.ReadN(t, topic, 2, 30*time.Second)
	if string(got[0].Value) != string(got[1].Value) || string(got[0].Value) != string(m.Value) {
		t.Fatalf("the two deliveries differ: %s / %s", got[0].Value, got[1].Value)
	}
}

func TestRunDrainsTheOutboxInTheBackground(t *testing.T) {
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	topic := fmt.Sprintf("test.relay.%d", time.Now().UnixNano())
	sink := outboundkafka.NewRelaySink(kafkatest.BrokerList(t))
	defer func() { _ = sink.Close() }()
	relay := outboxrelay.NewOutboxRelay(repo, sink, quiet(), outboxrelay.WithInterval(50*time.Millisecond), outboxrelay.WithBatchSize(2), outboxrelay.WithMaxAttempts(50))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	for _, k := range []string{"d-1", "d-2", "d-3", "d-4", "d-5"} {
		if err := repo.Insert(context.Background(), newMessage(topic, k)); err != nil {
			t.Fatal(err)
		}
	}
	kafkatest.ReadN(t, topic, 5, 60*time.Second)
	pgtest.WaitAllPublished(t, pool)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}
