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
var _ ports.ReturnRestockMetrics = (*telemetry.CustomerReturnsEventMetrics)(nil)

func TestCustomerReturnsEventMetricsCountsByTypeAndReason(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(previous) })

	metrics, err := telemetry.NewCustomerReturnsEventMetrics()
	if err != nil {
		t.Fatalf("NewCustomerReturnsEventMetrics: %v", err)
	}
	const authorized = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnAuthorized"
	const completed = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnCompleted"
	ctx := context.Background()
	metrics.Ignored(ctx, authorized)
	metrics.Ignored(ctx, authorized)
	metrics.ReturnSkipped(ctx, completed, "lines_absent")
	metrics.ReturnSkipped(ctx, completed, "listing_unknown")
	metrics.ReturnSkipped(ctx, completed, "listing_unknown")

	got := returnIgnoredPoints(t, reader)
	want := map[string]int64{
		authorized + " type_not_consumed": 2,
		completed + " lines_absent":       1,
		completed + " listing_unknown":    2,
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

// returnIgnoredPoints collects availability.customer_returns_events.ignored
// keyed "type reason".
func returnIgnoredPoints(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := map[string]int64{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "availability.customer_returns_events.ignored" {
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
