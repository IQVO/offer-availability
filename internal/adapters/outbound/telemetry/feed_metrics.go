package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/offer-availability/internal/application/ports"
)

// meterName scopes this service's own instruments, keeping them distinct from
// the ones otelchi and the runtime collector register.
const meterName = "github.com/claudioed/offer-availability"

// inventoryFeedsAppliedName is the Tier 2 business metric of the context (ADR 0009,
// the fleet's standard metrics convention): every inventory feed attempt, by
// outcome.
const inventoryFeedsAppliedName = "availability.inventory_feeds.applied"

const (
	outcomeKey = attribute.Key("outcome")
	reasonKey  = attribute.Key("reason")

	outcomeApplied    = "applied"
	outcomeSuperseded = "superseded"
	outcomeFailed     = "failed"
	outcomeRejected   = "rejected"
)

// FeedMetrics implements ports.FeedMetrics against the global MeterProvider.
// Until Setup installs a real provider the global one is a no-op, so recording
// is cheap and safe in tests and local runs.
type FeedMetrics struct {
	counter metric.Int64Counter
}

var _ ports.FeedMetrics = (*FeedMetrics)(nil)

// NewFeedMetrics registers the counter. It only fails if the instrument name is
// invalid, which is a programming error, not a runtime condition. Call it after
// Setup so the instrument binds to the real MeterProvider.
func NewFeedMetrics() (*FeedMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		inventoryFeedsAppliedName,
		metric.WithDescription("Inventory feed attempts by outcome (applied, superseded, failed or rejected; reason says why a failed or rejected "+
			"feed did not apply, as a problem slug such as listing-not-on-node, negative-availability or feed-already-applied). "+
			"A falling applied share means shoppers see stale availability: a rising superseded means vendors send feeds out of order, "+
			"a rising failed means vendor data does not match our listings, a rising internal-error means the service itself is failing."),
		metric.WithUnit("{feed}"),
	)
	if err != nil {
		return nil, err
	}
	return &FeedMetrics{counter: counter}, nil
}

// FeedApplied counts a feed that was applied.
func (m *FeedMetrics) FeedApplied(ctx context.Context) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeApplied)))
}

// FeedSuperseded counts a feed stored as SUPERSEDED by a newer one.
func (m *FeedMetrics) FeedSuperseded(ctx context.Context) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeSuperseded)))
}

// FeedFailed counts a feed stored as FAILED, with the invariant that broke.
func (m *FeedMetrics) FeedFailed(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeFailed), reasonKey.String(reason)))
}

// FeedRejected counts a feed request that was refused with nothing recorded.
func (m *FeedMetrics) FeedRejected(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeRejected), reasonKey.String(reason)))
}
