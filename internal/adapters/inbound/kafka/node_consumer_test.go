package kafka

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

type recordingUseCase struct {
	got []usecases.NodeEvent
	err error
}

func (u *recordingUseCase) Execute(_ context.Context, ev usecases.NodeEvent) error {
	u.got = append(u.got, ev)
	return u.err
}

func envelope(typ, data string) []byte {
	return []byte(`{"specversion":"1.0","id":"ev-1","source":"/retailnetwork/node-topology","type":"` + typ + `","subject":"node-1",` +
		`"datacontenttype":"application/json","time":"2026-10-23T09:00:00Z","data":` + data + `}`)
}

func newConsumer(uc nodeEventUseCase) (*NodeConsumer, *bytes.Buffer) {
	var logs bytes.Buffer
	return &NodeConsumer{UseCase: uc, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, &logs
}

func TestHandleMessageMapsEachNodeEvent(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for name, c := range map[string]struct {
		typ, data string
		status    nodestatus.Status
		at        time.Time
	}{
		"registered": {typeNodeRegistered, `{"nodeId":"node-1","type":"FULFILLMENT","extra":{"a":1}}`, nodestatus.StatusProposed, at("2026-10-23T09:00:00Z")},
		"activated":  {typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`, nodestatus.StatusActive, at("2026-10-23T10:00:00Z")},
		"suspended":  {typeNodeSuspended, `{"nodeId":"node-1","reason":"flood","suspendedAt":"2026-10-23T11:00:00Z"}`, nodestatus.StatusSuspended, at("2026-10-23T11:00:00Z")},
		"retired":    {typeNodeRetired, `{"nodeId":"node-1","retiredAt":"2026-10-23T12:00:00Z"}`, nodestatus.StatusRetired, at("2026-10-23T12:00:00Z")},
	} {
		t.Run(name, func(t *testing.T) {
			uc := &recordingUseCase{}
			c0, _ := newConsumer(uc)
			if err := c0.HandleMessage(context.Background(), envelope(c.typ, c.data)); err != nil {
				t.Fatal(err)
			}
			want := usecases.NodeEvent{EventID: "ev-1", EventType: c.typ, NodeID: "node-1", Status: c.status, At: c.at}
			if len(uc.got) != 1 || uc.got[0] != want {
				t.Fatalf("got %+v, want %+v", uc.got, want)
			}
		})
	}
}

func TestHandleMessageSkipsWhatIsNotOurs(t *testing.T) {
	uc := &recordingUseCase{}
	c, logs := newConsumer(uc)
	for name, value := range map[string][]byte{
		"another node-topology type": envelope("com.retailnetwork.logistics.node-topology.lane.LaneOpened", `{"laneId":"l"}`),
		"not json":                   []byte("not json"),
		"flat legacy envelope":       []byte(`{"eventType":"NodeActivated","nodeId":"node-1"}`),
	} {
		if err := c.HandleMessage(context.Background(), value); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(uc.got) != 0 {
		t.Fatalf("applied %v", uc.got)
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != 2 {
		t.Fatalf("logged %d invalid messages: %s", got, logs.String())
	}
}

func TestHandleMessagePoisonAndTransientErrors(t *testing.T) {
	for name, value := range map[string][]byte{
		"data is not an object": envelope(typeNodeActivated, `"x"`),
		"no nodeId":             envelope(typeNodeActivated, `{"activatedAt":"2026-10-23T10:00:00Z"}`),
		"no instant":            envelope(typeNodeSuspended, `{"nodeId":"node-1"}`),
		"unparsable instant":    envelope(typeNodeRetired, `{"nodeId":"node-1","retiredAt":"soon"}`),
	} {
		c, _ := newConsumer(&recordingUseCase{})
		if err := c.HandleMessage(context.Background(), value); !errors.Is(err, errPoison) {
			t.Errorf("%s: err = %v, want poison", name, err)
		}
	}
	// A value the use case refuses can never be applied either.
	c, _ := newConsumer(&recordingUseCase{err: shared.Invalid("bad node id")})
	if err := c.HandleMessage(context.Background(), envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`)); !errors.Is(err, errPoison) {
		t.Fatalf("validation error: %v", err)
	}
	// Anything else is transient: the loop retries it.
	boom := errors.New("connection reset")
	c, _ = newConsumer(&recordingUseCase{err: boom})
	err := c.HandleMessage(context.Background(), envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`))
	if !errors.Is(err, boom) || errors.Is(err, errPoison) {
		t.Fatalf("transient error: %v", err)
	}
}

// fakeReader serves queued messages, then blocks until ctx ends.
type fakeReader struct {
	mu        sync.Mutex
	msgs      []kafkago.Message
	committed []int64
	commitErr []error
	closed    bool
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	if len(r.msgs) > 0 {
		m := r.msgs[0]
		r.msgs = r.msgs[1:]
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.commitErr) > 0 {
		err := r.commitErr[0]
		r.commitErr = r.commitErr[1:]
		if err != nil {
			return err
		}
	}
	for _, m := range msgs {
		r.committed = append(r.committed, m.Offset)
	}
	return nil
}

func (r *fakeReader) Close() error { r.closed = true; return nil }

func (r *fakeReader) commits() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.committed...)
}

type fakeDLQ struct {
	mu     sync.Mutex
	msgs   []kafkago.Message
	errs   []error
	closed bool
}

func (d *fakeDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.errs) > 0 {
		err := d.errs[0]
		d.errs = d.errs[1:]
		if err != nil {
			return err
		}
	}
	d.msgs = append(d.msgs, msgs...)
	return nil
}

func (d *fakeDLQ) Close() error { d.closed = true; return nil }

func (d *fakeDLQ) written() []kafkago.Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]kafkago.Message(nil), d.msgs...)
}

// flakyUseCase fails transiently n times, then succeeds.
type flakyUseCase struct {
	mu       sync.Mutex
	failures int
	calls    int
	applied  []string
}

func (u *flakyUseCase) Execute(_ context.Context, ev usecases.NodeEvent) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.calls <= u.failures {
		return errors.New("deadlock detected")
	}
	u.applied = append(u.applied, ev.EventID)
	return nil
}

func runUntil(t *testing.T, c *NodeConsumer, done func() bool) error {
	t.Helper()
	c.sleep = func(context.Context, time.Duration) error { return nil }
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() { out <- c.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !done() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	return <-out
}

func msg(offset int64, value []byte) kafkago.Message {
	return kafkago.Message{Topic: NodeTopic, Partition: 0, Offset: offset, Key: []byte("node-1"), Value: value}
}

func TestRunCommitsOnlyAfterTheEventWasHandled(t *testing.T) {
	good := envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`)
	uc := &flakyUseCase{failures: 2}
	reader := &fakeReader{msgs: []kafkago.Message{msg(0, good), msg(1, []byte("garbage"))}}
	c := &NodeConsumer{Reader: reader, UseCase: uc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DLQ: &fakeDLQ{}}
	err := runUntil(t, c, func() bool { return len(reader.commits()) == 2 })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if uc.calls != 3 || len(uc.applied) != 1 {
		t.Fatalf("the same message is retried: %d calls, applied %v", uc.calls, uc.applied)
	}
	if got := reader.commits(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("commits = %v", got)
	}
}

func TestRunDeadLettersAPoisonEventAtOnceAndAStuckOneAfterTheBound(t *testing.T) {
	bad := envelope(typeNodeActivated, `{"activatedAt":"2026-10-23T10:00:00Z"}`)
	good := envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`)
	uc := &flakyUseCase{failures: 1000}
	dlq := &fakeDLQ{errs: []error{errors.New("dlq down")}}
	reader := &fakeReader{msgs: []kafkago.Message{msg(0, bad), msg(1, good)}, commitErr: []error{errors.New("commit failed")}}
	c := &NodeConsumer{Reader: reader, UseCase: uc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DLQ: dlq}
	_ = runUntil(t, c, func() bool { return len(reader.commits()) == 2 })

	written := dlq.written()
	if len(written) != 2 || written[0].Topic != NodeTopic+".dlq" || written[1].Topic != NodeTopic+".dlq" {
		t.Fatalf("dead letters = %+v", written)
	}
	if uc.calls != domainMaxHandlerAttempts {
		t.Fatalf("a transient failure is retried %d times, got %d calls", domainMaxHandlerAttempts, uc.calls)
	}
	headers := map[string]string{}
	for _, h := range written[1].Headers {
		headers[h.Key] = string(h.Value)
	}
	if headers["x-dlq-source-topic"] != NodeTopic || headers["x-dlq-source-offset"] != "1" || !strings.Contains(headers["x-dlq-error"], "deadlock") || headers["x-dlq-failed-at"] == "" {
		t.Fatalf("dead-letter headers = %v", headers)
	}
	if string(written[1].Key) != "node-1" || string(written[1].Value) != string(good) {
		t.Fatalf("the original key and value travel: %+v", written[1])
	}
}

func TestRunWithoutADeadLetterWriterRetriesUntilItWorks(t *testing.T) {
	good := envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`)
	uc := &flakyUseCase{failures: 8}
	reader := &fakeReader{msgs: []kafkago.Message{msg(0, good)}}
	c := &NodeConsumer{Reader: reader, UseCase: uc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_ = runUntil(t, c, func() bool { return len(reader.commits()) == 1 })
	if uc.calls != 9 {
		t.Fatalf("calls = %d", uc.calls)
	}
}

func TestACancelledHandlerIsNeverDeadLettered(t *testing.T) {
	good := envelope(typeNodeActivated, `{"nodeId":"node-1","activatedAt":"2026-10-23T10:00:00Z"}`)
	ctx, cancel := context.WithCancel(context.Background())
	dlq := &fakeDLQ{}
	c := &NodeConsumer{
		Reader: &fakeReader{msgs: []kafkago.Message{msg(0, good)}},
		UseCase: useCaseFunc(func(context.Context, usecases.NodeEvent) error {
			cancel() // the process is stopping while the handler runs
			return context.Canceled
		}),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DLQ: dlq,
	}
	if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if len(dlq.written()) != 0 {
		t.Fatalf("dead-lettered a good message on shutdown: %+v", dlq.written())
	}
}

type useCaseFunc func(context.Context, usecases.NodeEvent) error

func (f useCaseFunc) Execute(ctx context.Context, ev usecases.NodeEvent) error { return f(ctx, ev) }

func TestCloseReleasesReaderAndWriter(t *testing.T) {
	r, d := &fakeReader{}, &fakeDLQ{}
	if err := (&NodeConsumer{Reader: r, DLQ: d}).Close(); err != nil || !r.closed || !d.closed {
		t.Fatalf("closed: %v %v %v", err, r.closed, d.closed)
	}
	if err := (&NodeConsumer{Reader: r}).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewNodeConsumerWiresTheTopicAndTheGroupWithoutDialling(t *testing.T) {
	c := NewNodeConsumer([]string{"127.0.0.1:1"}, "offer-availability-test", &recordingUseCase{}, nil)
	defer func() { _ = c.Close() }()
	if c.Reader == nil || c.DLQ == nil || c.Logger == nil {
		t.Fatalf("consumer = %+v", c)
	}
	cfg := readerConfig([]string{"a:1"}, NodeTopic, "grp")
	if cfg.Topic != NodeTopic || cfg.GroupID != "grp" || cfg.CommitInterval != 0 {
		t.Fatalf("reader config = %+v", cfg)
	}
}

func TestRetryPolicyBackoffIsCapped(t *testing.T) {
	p := RetryPolicy{}.withDefaults()
	if p.Initial != DefaultRetryInitial || p.Max != DefaultRetryMax {
		t.Fatalf("defaults: %+v", p)
	}
	d := p.Initial
	for range 10 {
		d = p.next(d)
	}
	if d != DefaultRetryMax {
		t.Fatalf("cap: %v", d)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep: %v", err)
	}
}

func TestDeadLetterWaitsForAnAutoCreatedTopic(t *testing.T) {
	dlq := &fakeDLQ{errs: []error{kafkago.LeaderNotAvailable, kafkago.UnknownTopicOrPartition}}
	if err := writeDLQ(context.Background(), dlq, kafkago.Message{Topic: "t.dlq"}); err != nil || len(dlq.written()) != 1 {
		t.Fatalf("writeDLQ: %v %d", err, len(dlq.written()))
	}
	other := errors.New("authorization failed")
	if err := writeDLQ(context.Background(), &fakeDLQ{errs: []error{other}}, kafkago.Message{}); !errors.Is(err, other) {
		t.Fatalf("a different error is returned: %v", err)
	}
	werrs := kafkago.WriteErrors{kafkago.LeaderNotAvailable, nil}
	if !isTopicNotReady(werrs) || isTopicNotReady(kafkago.WriteErrors{kafkago.LeaderNotAvailable, other}) || isTopicNotReady(kafkago.WriteErrors{nil}) {
		t.Fatal("isTopicNotReady over WriteErrors")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeDLQ(ctx, &fakeDLQ{errs: []error{kafkago.LeaderNotAvailable, kafkago.LeaderNotAvailable}}, kafkago.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait: %v", err)
	}
	if w := newDomainDLQWriter([]string{"a:1"}); w == nil {
		t.Fatal("writer")
	}
}
