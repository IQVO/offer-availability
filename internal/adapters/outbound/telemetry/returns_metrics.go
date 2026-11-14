package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// customerReturnsEventsIgnoredName counts what the customer-returns consumer
// acknowledged without effect, by full CloudEvents type and reason (ADR 0015).
// Follows the fleet naming <ctx>.<noun>.<verb>, the twin of
// availability.seller_events.ignored.
const customerReturnsEventsIgnoredName = "availability.customer_returns_events.ignored"

// CustomerReturnsEventMetrics counts the ignored types and the return-restock
// skips of the customer-returns topic against the global MeterProvider (a
// no-op until Setup installs a real one). It is both the consumer's
// IgnoredEvents and the use case's ports.ReturnRestockMetrics.
type CustomerReturnsEventMetrics struct {
	ignored metric.Int64Counter
}

// NewCustomerReturnsEventMetrics registers the counter. It only fails on an
// invalid instrument name. Call it after Setup so the instrument binds to the
// real MeterProvider.
func NewCustomerReturnsEventMetrics() (*CustomerReturnsEventMetrics, error) {
	ignored, err := otel.Meter(meterName).Int64Counter(
		customerReturnsEventsIgnoredName,
		metric.WithDescription("customer-returns events, or lines of ReturnCompleted, acknowledged without effect, "+
			"by full CloudEvents type and reason: type_not_consumed (a type offer-availability does not consume; one that keeps "+
			"rising may need consuming), lines_absent (a ReturnCompleted without its lines list), nothing_restockable (one line "+
			"whose qtyRestockable is zero or less; its unsellable units never touch a listing), item_absent (one line without a "+
			"usable catalogItemId), listing_unknown (one line whose listing of catalogItemId, nodeId and ownerRef does not exist)."),
		metric.WithUnit("{event}"),
	)
	if err != nil {
		return nil, err
	}
	return &CustomerReturnsEventMetrics{ignored: ignored}, nil
}

// Ignored counts one event of eventType whose type this service does not
// consume.
func (m *CustomerReturnsEventMetrics) Ignored(ctx context.Context, eventType string) {
	m.add(ctx, eventType, ReasonTypeNotConsumed)
}

// ReturnSkipped counts one return-restock skip (ports.ReturnRestockMetrics).
func (m *CustomerReturnsEventMetrics) ReturnSkipped(ctx context.Context, eventType, reason string) {
	m.add(ctx, eventType, reason)
}

func (m *CustomerReturnsEventMetrics) add(ctx context.Context, eventType, reason string) {
	m.ignored.Add(ctx, 1, metric.WithAttributes(typeKey.String(eventType), reasonKey.String(reason)))
}
