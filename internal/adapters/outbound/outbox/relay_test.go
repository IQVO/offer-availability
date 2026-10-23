package outbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/outbox"
)

var errSend = errors.New("broker down")

// fakeStore drains an in-memory queue the way OutboxRepo does: in order, one at a
// time, stopping at the first send error with an *outbox.SendFailure.
type fakeStore struct {
	mu       sync.Mutex
	rows     []outbox.Message
	attempts map[int]int
	limit    int
	cap      int
	drains   int
}

func newFakeStore(rows ...outbox.Message) *fakeStore {
	return &fakeStore{rows: rows, attempts: map[int]int{}}
}

func (s *fakeStore) Drain(ctx context.Context, limit, maxAttempts int, send func(context.Context, outbox.Message) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drains++
	s.limit, s.cap = limit, maxAttempts
	published := 0
	var rest []outbox.Message
	var failure error
	for i, m := range s.rows {
		if failure != nil || published >= limit || (maxAttempts > 0 && s.attempts[i] >= maxAttempts) {
			rest = append(rest, m)
			continue
		}
		if err := send(ctx, m); err != nil {
			s.attempts[i]++
			failure = &outbox.SendFailure{RowID: int64(i + 1), Message: m, Attempts: s.attempts[i], Err: err}
			rest = append(rest, m)
			continue
		}
		published++
	}
	s.rows = rest
	return published, failure
}

func (s *fakeStore) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

type recordingSink struct {
	mu   sync.Mutex
	got  []string
	fail error
}

func (s *recordingSink) Send(_ context.Context, msgs ...outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	for _, m := range msgs {
		s.got = append(s.got, m.EventID)
	}
	return nil
}

func (s *recordingSink) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

func msgs(ids ...string) []outbox.Message {
	out := make([]outbox.Message, len(ids))
	for i, id := range ids {
		out[i] = outbox.Message{EventID: id, Topic: "t", EventType: "com.example.Type", Key: []byte("k-" + id)}
	}
	return out
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

func TestRelayOncePublishesInOrderAndReportsTheCount(t *testing.T) {
	store, sink := newFakeStore(msgs("a", "b", "c")...), &recordingSink{}
	n, err := NewOutboxRelay(store, sink, quiet(), WithBatchSize(2), WithMaxAttempts(5)).RelayOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("published %d, err %v, want 2 and nil", n, err)
	}
	if got := sink.ids(); len(got) != 2 || got[0] != "a" || got[1] != "b" || store.pending() != 1 {
		t.Fatalf("sent %v, pending %d", got, store.pending())
	}
	if store.limit != 2 || store.cap != 5 {
		t.Fatalf("store saw limit %d cap %d", store.limit, store.cap)
	}
}

func TestOptionsIgnoreInvalidValues(t *testing.T) {
	r := NewOutboxRelay(newFakeStore(), &recordingSink{}, nil, WithInterval(0), WithBatchSize(0), WithMaxAttempts(-1))
	if r.interval != DefaultInterval || r.batchSize != DefaultBatchSize || r.maxAttempts != 0 || r.logger == nil {
		t.Fatalf("relay = %+v", r)
	}
	r = NewOutboxRelay(newFakeStore(), &recordingSink{}, nil, WithInterval(time.Minute), WithBatchSize(7), WithMaxAttempts(0))
	if r.interval != time.Minute || r.batchSize != 7 || r.maxAttempts != 0 {
		t.Fatalf("relay = %+v", r)
	}
}

func TestFailedSendStopsThePassAndLogsTheCappedRowOnce(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store, sink := newFakeStore(msgs("a", "b")...), &recordingSink{fail: errSend}
	relay := NewOutboxRelay(store, sink, logger, WithMaxAttempts(2))
	for i := 1; i <= 2; i++ {
		n, err := relay.RelayOnce(context.Background())
		var sf *outbox.SendFailure
		if n != 0 || !errors.As(err, &sf) || sf.Attempts != i || !errors.Is(err, errSend) {
			t.Fatalf("pass %d: n %d err %v", i, n, err)
		}
	}
	if got := bytes.Count(logs.Bytes(), []byte(AttemptCapReachedMsg)); got != 1 {
		t.Fatalf("the capped row is logged once, got %d: %s", got, logs.String())
	}
	if !bytes.Contains(logs.Bytes(), []byte("k-a")) || !bytes.Contains(logs.Bytes(), []byte("com.example.Type")) {
		t.Fatalf("the log names the key and the type: %s", logs.String())
	}
	sink.fail = nil
	n, err := relay.RelayOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("a capped row is overtaken: published %d err %v", n, err)
	}
	if got := sink.ids(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("sent %v", got)
	}
}

func TestUnlimitedAttemptsNeverLogACap(t *testing.T) {
	var logs bytes.Buffer
	store, sink := newFakeStore(msgs("a")...), &recordingSink{fail: errSend}
	relay := NewOutboxRelay(store, sink, slog.New(slog.NewTextHandler(&logs, nil)))
	for range 3 {
		_, _ = relay.RelayOnce(context.Background())
	}
	if logs.Len() != 0 {
		t.Fatalf("unexpected log: %s", logs.String())
	}
}

func TestRunDrainsUntilCancelled(t *testing.T) {
	store, sink := newFakeStore(msgs("a", "b", "c", "d", "e")...), &recordingSink{}
	relay := NewOutboxRelay(store, sink, quiet(), WithBatchSize(2), WithInterval(5*time.Millisecond), WithMaxAttempts(3))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for store.pending() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	if got := sink.ids(); len(got) != 5 {
		t.Fatalf("sent %v", got)
	}
}

func TestRunReturnsAtOnceOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newFakeStore(msgs("a")...)
	if err := NewOutboxRelay(store, &recordingSink{}, quiet()).Run(ctx); !errors.Is(err, context.Canceled) || store.drains != 0 {
		t.Fatalf("err %v drains %d", err, store.drains)
	}
}

func TestRunKeepsGoingAfterAFailedPass(t *testing.T) {
	store, sink := newFakeStore(msgs("a")...), &recordingSink{fail: errSend}
	relay := NewOutboxRelay(store, sink, quiet(), WithInterval(5*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		drains := store.drains
		store.mu.Unlock()
		if drains >= 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if store.drains < 3 {
		t.Fatalf("the relay stopped after a failed pass: %d drains", store.drains)
	}
}
