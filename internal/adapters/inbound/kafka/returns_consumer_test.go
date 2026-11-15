package kafka

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0015: the customer-returns consumer applies ReturnCompleted.

// goldenReturnCompleted is the producer's golden payload (the design's event
// shape, which the customer-returns repo's goldens match).
const goldenReturnCompleted = `{"authorizationId":"ret-1","outcome":"RECEIVED","nodeId":"n-1",` +
	`"lines":[{"catalogItemId":"item-1","ownerRef":"S-1","qtyRestockable":2,"qtyUnsellable":1}],"receivedAt":"2026-11-14T12:00:00Z"}`

type recordingReturnUseCase struct {
	mu  sync.Mutex
	got []usecases.ReturnCompletionEvent
	res usecases.ReturnCompletionResult
	err error
}

func (u *recordingReturnUseCase) Execute(_ context.Context, ev usecases.ReturnCompletionEvent) (usecases.ReturnCompletionResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.got = append(u.got, ev)
	return u.res, u.err
}

func returnEnvelope(id, typ, data string) []byte {
	return returnEnvelopeAt(id, typ, data, "2026-11-14T09:00:00Z")
}

// returnEnvelopeAt builds the envelope with an explicit time attribute;
// at == "" omits it (a payload without receivedAt then has no instant).
func returnEnvelopeAt(id, typ, data, at string) []byte {
	timeAttr := `,"time":"` + at + `"`
	if at == "" {
		timeAttr = ""
	}
	return []byte(`{"specversion":"1.0","id":"` + id + `","source":"/retailnetwork/customer-returns","type":"` + typ + `","subject":"ret-1",` +
		`"datacontenttype":"application/json"` + timeAttr + `,"data":` + data + `}`)
}

func newReturnsConsumer(uc returnCompletionUseCase) (*ReturnsConsumer, *countingIgnored, *bytes.Buffer) {
	var logs bytes.Buffer
	ignored := &countingIgnored{}
	return &ReturnsConsumer{Returns: uc, Ignored: ignored, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, ignored, &logs
}

func TestReturnsConsumerDecodesTheGolden(t *testing.T) {
	uc := &recordingReturnUseCase{}
	consumer, ignored, _ := newReturnsConsumer(uc)
	if err := consumer.HandleMessage(context.Background(), returnEnvelope("ev-1", typeReturnCompleted, goldenReturnCompleted)); err != nil {
		t.Fatal(err)
	}
	want := usecases.ReturnCompletionEvent{
		EventID: "ev-1", EventType: typeReturnCompleted, AuthorizationID: "ret-1", NodeID: "n-1",
		ReceivedAt: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), LinesPresent: true,
		Lines: []usecases.ReturnLine{{CatalogItemID: "item-1", OwnerRef: "S-1", QtyRestockable: 2, QtyUnsellable: 1}},
	}
	if len(uc.got) != 1 || !reflect.DeepEqual(uc.got[0], want) {
		t.Fatalf("got %+v, want %+v", uc.got, want)
	}
	if len(ignored.types) != 0 {
		t.Fatalf("the consumed type is never ignored: %v", ignored.types)
	}
}

func TestReturnsConsumerInstantAndListTolerance(t *testing.T) {
	cases := []struct {
		name, data string
		at         time.Time
		present    bool
	}{
		{"the CloudEvents time when receivedAt is absent", `{"authorizationId":"ret-1","nodeId":"n-1","lines":[]}`,
			time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), true},
		{"absent lines", `{"authorizationId":"ret-1","nodeId":"n-1","receivedAt":"2026-11-14T12:00:00Z"}`,
			time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), false},
		{"null lines", `{"authorizationId":"ret-1","nodeId":"n-1","receivedAt":"2026-11-14T12:00:00Z","lines":null}`,
			time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		uc := &recordingReturnUseCase{}
		consumer, _, _ := newReturnsConsumer(uc)
		if err := consumer.HandleMessage(context.Background(), returnEnvelope("ev-1", typeReturnCompleted, c.data)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(uc.got) != 1 || !uc.got[0].ReceivedAt.Equal(c.at) || uc.got[0].LinesPresent != c.present {
			t.Fatalf("%s: got %+v", c.name, uc.got)
		}
	}
}

func TestReturnsConsumerPoisonAndTransientErrors(t *testing.T) {
	poison := []struct{ name, data string }{
		{"data is not an object", `"x"`},
		{"no authorizationId", `{"nodeId":"n-1","lines":[]}`},
		{"no nodeId", `{"authorizationId":"ret-1","lines":[]}`},
		{"no instant anywhere", `{"authorizationId":"ret-1","nodeId":"n-1","lines":[]}`},
		{"qtyRestockable is not a number", `{"authorizationId":"ret-1","nodeId":"n-1","lines":[{"catalogItemId":"item-1","qtyRestockable":"two"}]}`},
	}
	for _, c := range poison {
		uc := &recordingReturnUseCase{}
		consumer, _, _ := newReturnsConsumer(uc)
		err := consumer.HandleMessage(context.Background(), returnEnvelopeAt("ev-1", typeReturnCompleted, c.data, ""))
		if !errors.Is(err, errPoison) {
			t.Errorf("%s: want errPoison, got %v", c.name, err)
		}
		if len(uc.got) != 0 {
			t.Errorf("%s: the use case ran", c.name)
		}
	}
	uc := &recordingReturnUseCase{err: errors.New("transient boom")}
	consumer, _, _ := newReturnsConsumer(uc)
	err := consumer.HandleMessage(context.Background(), returnEnvelope("ev-1", typeReturnCompleted, goldenReturnCompleted))
	if err == nil || errors.Is(err, errPoison) {
		t.Fatalf("a transient failure is neither nil nor poison: %v", err)
	}
	uc = &recordingReturnUseCase{err: shared.Invalid("nope")}
	consumer, _, _ = newReturnsConsumer(uc)
	if err := consumer.HandleMessage(context.Background(), returnEnvelope("ev-1", typeReturnCompleted, goldenReturnCompleted)); !errors.Is(err, errPoison) {
		t.Fatalf("a validation failure is poison: %v", err)
	}
}

func TestReturnsConsumerCountsAndSkipsOtherTypes(t *testing.T) {
	uc := &recordingReturnUseCase{}
	consumer, ignored, logs := newReturnsConsumer(uc)
	if err := consumer.HandleMessage(context.Background(), returnEnvelope("ev-9", "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnAuthorized", `{"authorizationId":"ret-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := consumer.HandleMessage(context.Background(), []byte("not a CloudEvent")); err != nil {
		t.Fatal(err)
	}
	if len(uc.got) != 0 || len(ignored.types) != 1 {
		t.Fatalf("use case ran %d times, ignored %v", len(uc.got), ignored.types)
	}
	if !bytes.Contains(logs.Bytes(), []byte("skipping a message that is not a valid CloudEvent")) {
		t.Fatalf("logs = %s", logs.String())
	}
}
