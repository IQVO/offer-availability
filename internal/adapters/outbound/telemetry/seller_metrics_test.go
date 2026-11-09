package telemetry_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/telemetry"
)

func TestSellerEventMetricsCountsIgnoredTypes(t *testing.T) {
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
	metrics.Ignored(ctx, received)

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
				got[typ.AsString()] = p.Value
			}
		}
	}
	if len(got) != 2 || got[shipped] != 2 || got[received] != 1 {
		t.Fatalf("data points = %v", got)
	}
}
