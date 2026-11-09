package kafka

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0013: the seller-inbound consumer.

type recordingSellerUseCase struct {
	mu  sync.Mutex
	got []usecases.SellerEvent
	err error
}

func (u *recordingSellerUseCase) Execute(_ context.Context, ev usecases.SellerEvent) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.got = append(u.got, ev)
	return u.err
}

type countingIgnored struct {
	mu    sync.Mutex
	types []string
}

func (c *countingIgnored) Ignored(_ context.Context, eventType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.types = append(c.types, eventType)
}

func sellerEnvelope(id, typ, data string) []byte {
	return []byte(`{"specversion":"1.0","id":"` + id + `","source":"/retailnetwork/seller-inbound","type":"` + typ + `","subject":"seller-1",` +
		`"datacontenttype":"application/json","time":"2026-11-08T09:00:00Z","data":` + data + `}`)
}

func newSellerConsumer(uc sellerEventUseCase) (*SellerConsumer, *countingIgnored, *bytes.Buffer) {
	var logs bytes.Buffer
	ignored := &countingIgnored{}
	return &SellerConsumer{UseCase: uc, Received: &recordingReceivedUseCase{}, Ignored: ignored, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, ignored, &logs
}

func TestSellerConsumerMapsBothEvents(t *testing.T) {
	cases := []struct {
		typ, data string
		status    sellerstatus.Status
		at        time.Time
	}{
		{typeSellerSuspended, `{"sellerId":"seller-1","suspendedAt":"2026-11-08T10:00:00+01:00","reason":"repeated label violations","extra":{"a":1}}`,
			sellerstatus.StatusSuspended, time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)},
		{typeSellerReinstated, `{"sellerId":"seller-1","reinstatedAt":"2026-11-08T11:00:00Z"}`,
			sellerstatus.StatusActive, time.Date(2026, 11, 8, 11, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		uc := &recordingSellerUseCase{}
		consumer, ignored, _ := newSellerConsumer(uc)
		if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", c.typ, c.data)); err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		want := usecases.SellerEvent{EventID: "ev-1", EventType: c.typ, SellerID: "seller-1", Status: c.status, At: c.at}
		if len(uc.got) != 1 || uc.got[0] != want || uc.got[0].At.Location() != time.UTC {
			t.Fatalf("%s: got %+v, want %+v", c.typ, uc.got, want)
		}
		if len(ignored.types) != 0 {
			t.Fatalf("%s: a consumed type is not ignored: %v", c.typ, ignored.types)
		}
	}
}

func TestSellerConsumerIgnoresAndCountsOtherTypesOfTheTopic(t *testing.T) {
	uc := &recordingSellerUseCase{}
	consumer, ignored, logs := newSellerConsumer(uc)
	others := []string{
		"com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentLabelled",
		"com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentShipped",
		"com.retailnetwork.partner.seller-inbound.seller.SellerOnboarded",
		// A suffix match is not a dispatch: the FULL type decides.
		"com.example.other.seller.SellerSuspended",
	}
	for i, typ := range others {
		if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-"+string(rune('a'+i)), typ, `{"sellerId":"seller-1"}`)); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	if len(uc.got) != 0 {
		t.Fatalf("applied %v", uc.got)
	}
	if strings.Join(ignored.types, ",") != strings.Join(others, ",") {
		t.Fatalf("counted %v, want %v", ignored.types, others)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("an ignored type is not a warning: %s", logs.String())
	}
}

func TestSellerConsumerSkipsWhatIsNotACloudEvent(t *testing.T) {
	uc := &recordingSellerUseCase{}
	consumer, ignored, logs := newSellerConsumer(uc)
	for _, value := range [][]byte{[]byte("not json"), []byte(`{"eventType":"SellerSuspended","sellerId":"seller-1"}`)} {
		if err := consumer.HandleMessage(context.Background(), value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	if len(uc.got) != 0 || len(ignored.types) != 0 || strings.Count(logs.String(), "level=WARN") != 2 {
		t.Fatalf("applied %v counted %v logs %s", uc.got, ignored.types, logs.String())
	}
	// Without a counter the consumer still ignores the type.
	bare := &SellerConsumer{UseCase: uc, Logger: discard()}
	if err := bare.HandleMessage(context.Background(), sellerEnvelope("ev-1", "com.example.Other", `{}`)); err != nil || len(uc.got) != 0 {
		t.Fatalf("nil counter: %v %v", err, uc.got)
	}
	// Without a received-stock use case InboundShipmentReceived is an ignored type, as before ADR 0014.
	bare.Ignored = ignored
	if err := bare.HandleMessage(context.Background(), sellerEnvelope("ev-2", typeInboundShipmentReceived, `{}`)); err != nil ||
		ignored.types[len(ignored.types)-1] != typeInboundShipmentReceived {
		t.Fatalf("nil received use case: %v %v", err, ignored.types)
	}
}

func TestSellerConsumerPoisonAndTransientErrors(t *testing.T) {
	poison := []struct{ name, typ, data string }{
		{"data is not an object", typeSellerSuspended, `"x"`},
		{"no sellerId", typeSellerSuspended, `{"suspendedAt":"2026-11-08T10:00:00Z"}`},
		{"no instant", typeSellerReinstated, `{"sellerId":"seller-1"}`},
		{"the other event's instant", typeSellerReinstated, `{"sellerId":"seller-1","suspendedAt":"2026-11-08T10:00:00Z"}`},
		{"unparsable instant", typeSellerSuspended, `{"sellerId":"seller-1","suspendedAt":"soon"}`},
	}
	for _, c := range poison {
		consumer, _, _ := newSellerConsumer(&recordingSellerUseCase{})
		if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", c.typ, c.data)); !errors.Is(err, errPoison) {
			t.Errorf("%s: err = %v, want poison", c.name, err)
		}
	}
	good := sellerEnvelope("ev-1", typeSellerSuspended, `{"sellerId":"seller-1","suspendedAt":"2026-11-08T10:00:00Z"}`)
	consumer, _, _ := newSellerConsumer(&recordingSellerUseCase{err: shared.Invalid("bad seller id")})
	if err := consumer.HandleMessage(context.Background(), good); !errors.Is(err, errPoison) {
		t.Fatalf("validation error: %v", err)
	}
	boom := errors.New("connection reset")
	consumer, _, _ = newSellerConsumer(&recordingSellerUseCase{err: boom})
	if err := consumer.HandleMessage(context.Background(), good); !errors.Is(err, boom) || errors.Is(err, errPoison) {
		t.Fatalf("transient error: %v", err)
	}
}

// sellerBench wires the consumer to the REAL use case over the memory store:
// seller-1 owns LIVE lst-1 on ACTIVE node-1.
func sellerBench(t *testing.T) (*SellerConsumer, *memory.Store) {
	t.Helper()
	s := memory.NewStore()
	ctx := context.Background()
	if err := s.Nodes().Save(ctx, repocontract.NewNodeView(t, "node-1", nodestatus.StatusActive)); err != nil {
		t.Fatal(err)
	}
	l := repocontract.NewOwnedListing(t, "lst-1", "item-1", "node-1", "seller-1", listing.Sellability{NodeActive: true})
	if err := s.Listings().Save(ctx, l); err != nil {
		t.Fatal(err)
	}
	uc := usecases.NewApplySellerEvent(usecases.Deps{
		Catalog: s.Catalog(), Listings: s.Listings(), Nodes: s.Nodes(), Sellers: s.Sellers(), Inbox: s.Inbox(),
		Outbox: s.Outbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(repocontract.Now), IDs: &memory.SequentialIDs{},
	})
	consumer, _, _ := newSellerConsumer(uc)
	return consumer, s
}

func TestSellerConsumerDuplicateDeliveryChangesNothing(t *testing.T) {
	consumer, s := sellerBench(t)
	suspended := sellerEnvelope("ev-1", typeSellerSuspended, `{"sellerId":"seller-1","suspendedAt":"2026-11-08T10:00:00Z"}`)
	for range 2 {
		if err := consumer.HandleMessage(context.Background(), suspended); err != nil {
			t.Fatal(err)
		}
	}
	// The same id again, now carrying another type and a later instant: still skipped.
	if err := consumer.HandleMessage(context.Background(),
		sellerEnvelope("ev-1", typeSellerReinstated, `{"sellerId":"seller-1","reinstatedAt":"2026-11-08T12:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Outbox().Events()); n != 1 {
		t.Fatalf("outbox events = %d, want one ListingSuppressed", n)
	}
	if c := s.Counts(); c.Processed != 1 || c.Sellers != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestSellerConsumerOlderInstantIsIgnored(t *testing.T) {
	consumer, s := sellerBench(t)
	steps := [][]byte{
		sellerEnvelope("ev-1", typeSellerSuspended, `{"sellerId":"seller-1","suspendedAt":"2026-11-08T10:00:00Z"}`),
		sellerEnvelope("ev-2", typeSellerReinstated, `{"sellerId":"seller-1","reinstatedAt":"2026-11-08T09:59:59Z"}`),
	}
	for _, value := range steps {
		if err := consumer.HandleMessage(context.Background(), value); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.Listings().Get(context.Background(), "lst-1")
	if err != nil {
		t.Fatal(err)
	}
	if l.Reason() != listing.ReasonOwnerSuspended || len(s.Outbox().Events()) != 1 || s.Counts().Processed != 2 {
		t.Fatalf("an older reinstatement must not restore: %s, %d events", l.Reason(), len(s.Outbox().Events()))
	}
}

func sellerMsg(offset int64, value []byte) kafkago.Message {
	return kafkago.Message{Topic: SellerTopic, Offset: offset, Key: []byte("seller-1"), Value: value}
}

func TestSellerConsumerRunDeadLettersPoisonToItsOwnTopic(t *testing.T) {
	bad := sellerEnvelope("ev-1", typeSellerSuspended, `{"suspendedAt":"2026-11-08T10:00:00Z"}`)
	good := sellerEnvelope("ev-2", typeSellerSuspended, `{"sellerId":"seller-1","suspendedAt":"2026-11-08T10:00:00Z"}`)
	other := sellerEnvelope("ev-3", "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentShipped", `{}`)
	uc := &recordingSellerUseCase{}
	dlq := &fakeDLQ{}
	reader := &fakeReader{msgs: []kafkago.Message{sellerMsg(0, bad), sellerMsg(1, good), sellerMsg(2, other)}}
	c := &SellerConsumer{Reader: reader, UseCase: uc, Logger: discard(), DLQ: dlq, Ignored: &countingIgnored{},
		sleep: func(context.Context, time.Duration) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan error, 1)
	go func() { out <- c.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(reader.commits()) < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if err := <-out; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	written := dlq.written()
	if len(written) != 1 || written[0].Topic != SellerTopic+DLQSuffix || string(written[0].Value) != string(bad) {
		t.Fatalf("dead letters = %+v", written)
	}
	if got := reader.commits(); len(got) != 3 || len(uc.got) != 1 {
		t.Fatalf("commits %v applied %v", got, uc.got)
	}
}

func TestNewSellerConsumerWiresTheTopicAndTheGroupWithoutDialling(t *testing.T) {
	ignored := &countingIgnored{}
	received := &recordingReceivedUseCase{}
	c := NewSellerConsumer([]string{"127.0.0.1:1"}, "offer-availability-seller-test", &recordingSellerUseCase{}, received, ignored, nil)
	if c.Reader == nil || c.DLQ == nil || c.Logger == nil || c.Ignored != ignored || c.Received != received {
		t.Fatalf("consumer = %+v", c)
	}
	r, ok := c.Reader.(*kafkago.Reader)
	if !ok || r.Config().Topic != SellerTopic || r.Config().GroupID != "offer-availability-seller-test" {
		t.Fatalf("reader = %T %+v", c.Reader, c.Reader)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if SellerTopic != "retailnetwork.seller-inbound.events" {
		t.Fatalf("topic = %s", SellerTopic)
	}
}
