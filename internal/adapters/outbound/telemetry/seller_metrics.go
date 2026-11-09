package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// sellerEventsIgnoredName counts what the seller-inbound consumer acknowledged
// without effect, by full CloudEvents type and reason (ADR 0013 rule 1, ADR
// 0014). Follows the fleet naming <ctx>.<noun>.<verb>.
const sellerEventsIgnoredName = "availability.seller_events.ignored"

// ReasonTypeNotConsumed is the reason of an event whose type this service does
// not consume. The received-stock reasons (received_unknown, gtin_unresolved,
// item_unknown) come from the use case (usecases.Skip*).
const ReasonTypeNotConsumed = "type_not_consumed"

// typeKey is the full CloudEvents type; reasonKey (feed_metrics.go) the reason.
const typeKey = attribute.Key("type")

// SellerEventMetrics counts the ignored types and the received-stock skips of
// the seller-inbound topic against the global MeterProvider (a no-op until
// Setup installs a real one). It is both the consumer's IgnoredEvents and the
// use case's ports.ReceivedStockMetrics.
type SellerEventMetrics struct {
	ignored metric.Int64Counter
}

// NewSellerEventMetrics registers the counter. It only fails on an invalid
// instrument name. Call it after Setup so the instrument binds to the real
// MeterProvider.
func NewSellerEventMetrics() (*SellerEventMetrics, error) {
	ignored, err := otel.Meter(meterName).Int64Counter(
		sellerEventsIgnoredName,
		metric.WithDescription("seller-inbound events, or received lines of InboundShipmentReceived, acknowledged without effect, "+
			"by full CloudEvents type and reason: type_not_consumed (a type offer-availability does not consume; one that keeps "+
			"rising may need consuming), received_unknown (an InboundShipmentReceived without its received list, published before "+
			"the producer carried it), gtin_unresolved (one received line whose GTIN no catalogue item holds), item_unknown "+
			"(one received line whose catalogItemId the catalogue does not hold)."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	return &SellerEventMetrics{ignored: ignored}, nil
}

// Ignored counts one event of eventType whose type this service does not
// consume.
func (m *SellerEventMetrics) Ignored(ctx context.Context, eventType string) {
	m.add(ctx, eventType, ReasonTypeNotConsumed)
}

// ReceivedSkipped counts one received-stock skip (ports.ReceivedStockMetrics).
func (m *SellerEventMetrics) ReceivedSkipped(ctx context.Context, eventType, reason string) {
	m.add(ctx, eventType, reason)
}

func (m *SellerEventMetrics) add(ctx context.Context, eventType, reason string) {
	m.ignored.Add(ctx, 1, metric.WithAttributes(typeKey.String(eventType), reasonKey.String(reason)))
}
