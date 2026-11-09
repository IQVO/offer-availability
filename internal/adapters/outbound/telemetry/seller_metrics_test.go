package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/telemetry"
	"github.com/claudioed/offer-availability/internal/application/ports"
)

// The metrics adapter is the use case's port too.
var _ ports.ReceivedStockMetrics = (*telemetry.SellerEventMetrics)(nil)

func TestSellerEventMetricsCountsByTypeAndReason(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	metrics, err := telemetry.NewSellerEventMetrics()
	if err != nil {
		t.Fatalf("NewSellerEventMetrics: %v", err)
	}
	const shipped = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentShipped"
	const received = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived"
	ctx := context.Background()
	metrics.Ignored(ctx, shipped)
	metrics.Ignored(ctx, shipped)
	metrics.ReceivedSkipped(ctx, received, "received_unknown")
	metrics.ReceivedSkipped(ctx, received, "gtin_unresolved")
	metrics.ReceivedSkipped(ctx, received, "gtin_unresolved")
	metrics.ReceivedSkipped(ctx, received, "gtin_unresolved")

	got := ignoredPoints(t, reader)
	want := map[string]int64{
		shipped + " type_not_consumed": 2,
		received + " received_unknown": 1,
		received + " gtin_unresolved":  3,
	}
	if len(got) != len(want) {
		t.Fatalf("data points = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("data points = %v, want %v", got, want)
		}
	}
}

// ignoredPoints collects availability.seller_events.ignored keyed "type reason".
func ignoredPoints(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "availability.seller_events.ignored" {
				continue
			}
			if m.Unit != "{event}" || m.Description == "" {
				t.Errorf("unit = %q, description empty = %v", m.Unit, m.Description == "")
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("data is a %T, want an int64 Sum", m.Data)
			}
			for _, p := range sum.DataPoints {
				typ, _ := p.Attributes.Value("type")
				reason, _ := p.Attributes.Value("reason")
				got[typ.AsString()+" "+reason.AsString()] = p.Value
			}
		}
	}
	return got
}
