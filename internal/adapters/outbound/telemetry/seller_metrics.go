package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// sellerEventsIgnoredName counts the CloudEvents of
// retailnetwork.seller-inbound.events whose type this service does not consume
// (ADR 0013, rule 1), by type. Follows the fleet naming <ctx>.<noun>.<verb>.
const sellerEventsIgnoredName = "availability.seller_events.ignored"

const typeKey = attribute.Key("type")

// SellerEventMetrics counts the ignored types of the seller-inbound topic
// against the global MeterProvider (a no-op until Setup installs a real one).
type SellerEventMetrics struct {
	ignored metric.Int64Counter
}

// NewSellerEventMetrics registers the counter. It only fails on an invalid
// instrument name. Call it after Setup so the instrument binds to the real
// MeterProvider.
func NewSellerEventMetrics() (*SellerEventMetrics, error) {
	ignored, err := otel.Meter(meterName).Int64Counter(
		sellerEventsIgnoredName,
		metric.WithDescription("seller-inbound events acknowledged without effect because offer-availability does not consume their type "+
			"(only SellerSuspended and SellerReinstated are applied), by full CloudEvents type. A type that keeps rising is "+
			"one the producer publishes that this context may need to consume."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	return &SellerEventMetrics{ignored: ignored}, nil
}

// Ignored counts one event of eventType that was acknowledged without effect.
func (m *SellerEventMetrics) Ignored(ctx context.Context, eventType string) {
	m.ignored.Add(ctx, 1, metric.WithAttributes(typeKey.String(eventType)))
}
