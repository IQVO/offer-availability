package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	inboundkafka "github.com/claudioed/offer-availability/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/offer-availability/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/offer-availability/internal/adapters/outbound/outbox"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/application/usecases"
)

// worker is a started background loop and the channel closed once it returned.
type worker struct {
	name string
	done chan struct{}
}

// startWorker runs fn until ctx is cancelled; a return that is not a
// cancellation is logged. The done channel closes after cleanup ran.
func startWorker(name string, logger *slog.Logger, fn func(ctx context.Context) error, cleanup func()) (*worker, func()) {
	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: name, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer cleanup()
		if err := fn(ctx); !errors.Is(err, context.Canceled) {
			logger.Error(name+" stopped", "error", err)
		}
	}()
	return w, stop
}

// startOutboxRelay starts the always-on relay goroutine over the Kafka sink.
// Kafka is dialled lazily on the first send, never here, so a broker that is
// down at boot only makes passes fail until it is up.
func startOutboxRelay(store outboxrelay.Store, cfg config, logger *slog.Logger) (*worker, func()) {
	sink := outboundkafka.NewRelaySink(cfg.brokers)
	relay := outboxrelay.NewOutboxRelay(store, sink, logger,
		outboxrelay.WithInterval(cfg.relayInterval),
		outboxrelay.WithMaxAttempts(cfg.maxAttempts),
	)
	logger.Info("outbox relay running", "interval", cfg.relayInterval, "max_attempts", cfg.maxAttempts, "topic", outboundkafka.Topic)
	return startWorker("outbox-relay", logger, relay.Run, func() {
		if err := sink.Close(); err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	})
}

// startNodeConsumer starts the node-topology consumer under the group id of
// NODE_TOPOLOGY_CONSUMER_GROUP. The reader dials on its first fetch, inside its
// own goroutine, never here.
func startNodeConsumer(uc *usecases.ApplyNodeEvent, cfg config, logger *slog.Logger) (*worker, func()) {
	consumer := inboundkafka.NewNodeConsumer(brokerList(cfg.brokers), cfg.nodeGroup, uc, logger)
	logger.Info("node-topology consumer running", "topic", inboundkafka.NodeTopic, "group", cfg.nodeGroup)
	return startWorker("node-topology-consumer", logger, consumer.Run, func() {
		if err := consumer.Close(); err != nil {
			logger.Error("node-topology consumer close failed", "error", err)
		}
	})
}

// startJanitor deletes the rows past their retention window every interval
// (ADR 0002, A19). A failed pass is logged and retried at the next tick.
func startJanitor(r *postgres.Retention, cfg config, logger *slog.Logger) (*worker, func()) {
	logger.Info("retention janitor running", "interval", cfg.retention)
	return startWorker("retention-janitor", logger, func(ctx context.Context) error {
		ticker := time.NewTicker(cfg.retention)
		defer ticker.Stop()
		for {
			purge(ctx, r, logger)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
	}, func() {})
}

func purge(ctx context.Context, r *postgres.Retention, logger *slog.Logger) {
	n, err := r.Purge(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("retention purge failed", "error", err)
		}
		return
	}
	if n != (postgres.Purged{}) {
		logger.Info("retention purge", "idempotency_keys", n.IdempotencyKeys, "feeds", n.Feeds, "processed_events", n.ProcessedEvents)
	}
}
