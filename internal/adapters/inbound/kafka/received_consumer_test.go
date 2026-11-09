package kafka

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0014: the seller-inbound consumer applies InboundShipmentReceived.

// goldenReceived is the producer's golden payload (seller-inbound ADR 0014).
const goldenReceived = `{"shipmentId":"shp-1","sellerId":"S-1","nodeId":"n-1","status":"CLOSED",` +
	`"cartons":[{"sscc":"006141410000000012","status":"RECEIVED"}],"receivedAt":"2026-10-24T12:00:00Z",` +
	`"received":[{"catalogItemId":"item-1","gtin":"00614141000012","qtyEaches":24},{"gtin":"00614141000036","qtyEaches":6}]}`

type recordingReceivedUseCase struct {
	mu  sync.Mutex
	got []usecases.ReceivedStockEvent
	res usecases.ReceivedStockResult
	err error
}

func (u *recordingReceivedUseCase) Execute(_ context.Context, ev usecases.ReceivedStockEvent) (usecases.ReceivedStockResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.got = append(u.got, ev)
	return u.res, u.err
}

func receivedConsumer(uc receivedStockUseCase) (*SellerConsumer, *recordingSellerUseCase, *countingIgnored, *bytes.Buffer) {
	sellers := &recordingSellerUseCase{}
	consumer, ignored, logs := newSellerConsumer(sellers)
	consumer.Received = uc
	return consumer, sellers, ignored, logs
}

func TestReceivedConsumerDecodesTheGolden(t *testing.T) {
	uc := &recordingReceivedUseCase{}
	consumer, sellers, ignored, _ := receivedConsumer(uc)
	if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", typeInboundShipmentReceived, goldenReceived)); err != nil {
		t.Fatal(err)
	}
	want := usecases.ReceivedStockEvent{
		EventID: "ev-1", EventType: typeInboundShipmentReceived, ShipmentID: "shp-1", SellerID: "S-1", NodeID: "n-1",
		ReceivedPresent: true, Received: []usecases.ReceivedLine{
			{CatalogItemID: "item-1", GTIN: "00614141000012", QtyEaches: 24},
			{GTIN: "00614141000036", QtyEaches: 6},
		},
	}
	if len(uc.got) != 1 || !reflect.DeepEqual(uc.got[0], want) {
		t.Fatalf("got %+v, want %+v", uc.got, want)
	}
	if len(sellers.got) != 0 || len(ignored.types) != 0 {
		t.Fatalf("a received event is neither a seller status event nor ignored: %v %v", sellers.got, ignored.types)
	}
}

func TestReceivedConsumerTellsAnAbsentListFromAnEmptyOne(t *testing.T) {
	cases := []struct {
		name, data string
		present    bool
	}{
		{"absent (published before the producer carried it)", `{"shipmentId":"shp-1","sellerId":"S-1","nodeId":"n-1","status":"CLOSED","cartons":[],"receivedAt":"2026-10-24T12:00:00Z"}`, false},
		{"null", `{"sellerId":"S-1","nodeId":"n-1","received":null}`, false},
		{"empty", `{"sellerId":"S-1","nodeId":"n-1","received":[]}`, true},
	}
	for _, c := range cases {
		uc := &recordingReceivedUseCase{}
		consumer, _, _, _ := receivedConsumer(uc)
		if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", typeInboundShipmentReceived, c.data)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(uc.got) != 1 || uc.got[0].ReceivedPresent != c.present || len(uc.got[0].Received) != 0 {
			t.Fatalf("%s: got %+v", c.name, uc.got)
		}
	}
}

func TestReceivedConsumerPoisonAndTransientErrors(t *testing.T) {
	poison := []struct{ name, data string }{
		{"data is not an object", `"x"`},
		{"no sellerId", `{"nodeId":"n-1","received":[]}`},
		{"no nodeId", `{"sellerId":"S-1","received":[]}`},
		{"qtyEaches is not a number", `{"sellerId":"S-1","nodeId":"n-1","received":[{"gtin":"00614141000012","qtyEaches":"many"}]}`},
	}
	for _, c := range poison {
		uc := &recordingReceivedUseCase{}
		consumer, _, _, _ := receivedConsumer(uc)
		if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", typeInboundShipmentReceived, c.data)); !errors.Is(err, errPoison) {
			t.Errorf("%s: err = %v, want poison", c.name, err)
		}
		if len(uc.got) != 0 {
			t.Errorf("%s: a poison payload reached the use case", c.name)
		}
	}
	good := sellerEnvelope("ev-1", typeInboundShipmentReceived, goldenReceived)
	consumer, _, _, _ := receivedConsumer(&recordingReceivedUseCase{err: shared.Invalid("bad node id")})
	if err := consumer.HandleMessage(context.Background(), good); !errors.Is(err, errPoison) {
		t.Fatalf("validation error: %v", err)
	}
	boom := errors.New("connection reset")
	consumer, _, _, _ = receivedConsumer(&recordingReceivedUseCase{err: boom})
	if err := consumer.HandleMessage(context.Background(), good); !errors.Is(err, boom) || errors.Is(err, errPoison) {
		t.Fatalf("transient error: %v", err)
	}
}

func TestReceivedConsumerLogsEverySkippedLine(t *testing.T) {
	uc := &recordingReceivedUseCase{res: usecases.ReceivedStockResult{
		Created: []listing.ID{"lst-1"},
		Skipped: []usecases.SkippedLine{{GTIN: "00614141000036", Reason: usecases.SkipGTINUnresolved}, {CatalogItemID: "item-9", GTIN: "00614141000012", Reason: usecases.SkipItemUnknown}},
	}}
	consumer, _, _, logs := receivedConsumer(uc)
	if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", typeInboundShipmentReceived, goldenReceived)); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 2 || !strings.Contains(out, "gtin=00614141000036") || !strings.Contains(out, "reason=item_unknown") ||
		!strings.Contains(out, "shipment_id=shp-1") || !strings.Contains(out, "listings_created=1") {
		t.Fatalf("logs = %s", out)
	}

	uc.res = usecases.ReceivedStockResult{Unknown: true}
	logs.Reset()
	if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-2", typeInboundShipmentReceived, `{"sellerId":"S-1","nodeId":"n-1"}`)); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); strings.Contains(out, "level=WARN") || !strings.Contains(out, "received stock is unknown") {
		t.Fatalf("an unknown list is logged at INFO: %s", out)
	}

	uc.res = usecases.ReceivedStockResult{Duplicate: true}
	logs.Reset()
	if err := consumer.HandleMessage(context.Background(), sellerEnvelope("ev-1", typeInboundShipmentReceived, goldenReceived)); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); strings.Contains(out, "level=INFO") || strings.Contains(out, "level=WARN") {
		t.Fatalf("a duplicate is not news: %s", out)
	}
}

// receivedBench wires the consumer to the REAL use case over the memory store:
// node n-1 is ACTIVE and item-1 is in the catalogue; the golden's second GTIN
// is held by no item.
func receivedBench(t *testing.T) (*SellerConsumer, *memory.Store, *countingSkips) {
	t.Helper()
	s := memory.NewStore()
	ctx := context.Background()
	if err := s.Nodes().Save(ctx, repocontract.NewNodeView(t, "n-1", nodestatus.StatusActive)); err != nil {
		t.Fatal(err)
	}
	if err := s.Catalog().Save(ctx, repocontract.NewItem(t, "item-1")); err != nil {
		t.Fatal(err)
	}
	skips := &countingSkips{}
	uc := usecases.NewApplyReceivedStock(usecases.Deps{
		Catalog: s.Catalog(), Listings: s.Listings(), Nodes: s.Nodes(), Sellers: s.Sellers(), Inbox: s.Inbox(),
		Outbox: s.Outbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(repocontract.Now), IDs: &memory.SequentialIDs{},
		ReceivedMetrics: skips,
	})
	consumer, _, _, _ := receivedConsumer(uc)
	return consumer, s, skips
}

type countingSkips struct{ reasons []string }

func (c *countingSkips) ReceivedSkipped(_ context.Context, _, reason string) {
	c.reasons = append(c.reasons, reason)
}

func TestReceivedConsumerGoldenCreatesTheOwnerListing(t *testing.T) {
	consumer, s, skips := receivedBench(t)
	golden := sellerEnvelope("ev-1", typeInboundShipmentReceived, goldenReceived)
	for range 2 { // the second delivery is a duplicate
		if err := consumer.HandleMessage(context.Background(), golden); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.Listings().FindByItemNodeOwner(context.Background(), "item-1", "n-1", "S-1")
	if err != nil {
		t.Fatalf("the owner listing: %v", err)
	}
	if l.State() != listing.StateLive || l.Availability().Basis != listing.BasisUnknown || l.Availability().Qty != 0 {
		t.Fatalf("listing = %+v", l.Snapshot())
	}
	if n := len(s.Outbox().Events()); n != 1 || s.Counts().Processed != 1 || s.Counts().Listings != 1 {
		t.Fatalf("outbox %d, counts %+v", n, s.Counts())
	}
	if strings.Join(skips.reasons, ",") != usecases.SkipGTINUnresolved {
		t.Fatalf("counted %v, want one gtin_unresolved", skips.reasons)
	}
}
