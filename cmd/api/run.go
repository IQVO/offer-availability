package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/offer-availability/internal/adapters/inbound/http"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/clock"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/offer-availability/internal/adapters/outbound/kafka"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/bootretry"
)

// run boots the service and serves until SIGINT/SIGTERM or until ctx is
// cancelled (the integration test stops the real entry point that way).
func run(ctx context.Context) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: newLogLevel(os.Getenv(envLogLevel))}))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened or bound.
	cfg, err := loadConfig(os.Getenv, logger)
	if err != nil {
		return err
	}
	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain and the workers have stopped.
	ad, closeAdapters, err := buildAdapters(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	readiness := newReadiness(ad.pool.Ping, cfg.brokers)
	handler, err := inboundhttp.NewRouter(buildServer(ad, readiness, logger))
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: cfg.addr, Handler: handler, ReadHeaderTimeout: defaultReadHeaderLimit}

	relay, stopRelay := startOutboxRelay(ad.outbox, cfg, logger)
	consumer, stopConsumer := startNodeConsumer(usecases.NewApplyNodeEvent(ad.deps), cfg, logger)
	janitor, stopJanitor := startJanitor(ad.retention, cfg, logger)
	workers := []*worker{relay, consumer, janitor}
	stops := []func(){stopRelay, stopConsumer, stopJanitor}
	return serveUntilSignal(ctx, logger, httpServer, readiness, cfg.drainDelay, workers, stops)
}

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping and
// a Kafka broker answering a topic-less metadata request (never topics: the
// broker auto-creates them on first write), cached 3 s, bounded 2 s. The ping
// is a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokerList(brokers)).Check},
	)
}

// adapters is the set of outbound adapters the composition root wires.
type adapters struct {
	pool      *pgxpool.Pool
	outbox    *postgres.OutboxRepo
	retention *postgres.Retention
	deps      usecases.Deps
}

// buildAdapters runs the embedded migrations and wires the Postgres adapters.
// The first outbound dial of an injected pod can be reset, so the migrations
// and the first ping retry with backoff; on exhaustion the LAST error is
// returned and the process refuses to boot.
func buildAdapters(ctx context.Context, cfg config, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(cfg.migrationsURL)
	}); err != nil {
		return adapters{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, cfg.databaseURL)
	if err != nil {
		return adapters{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	logger.Info("postgres adapters configured")
	return postgresAdapters(pool), pool.Close, nil
}

func postgresAdapters(pool *pgxpool.Pool) adapters {
	outbox := postgres.NewOutboxRepo(pool)
	return adapters{pool: pool, outbox: outbox, retention: postgres.NewRetention(pool), deps: usecases.Deps{
		Catalog: postgres.NewCatalogRepo(pool), Listings: postgres.NewListingRepo(pool), Feeds: postgres.NewFeedRepo(pool),
		Marks: postgres.NewNodeFeedMarks(pool), Nodes: postgres.NewNodeStatusRepo(pool), Inbox: postgres.NewProcessedEventRepo(pool),
		Outbox: postgres.NewEventOutbox(outbox, outboundkafka.NewEncoder()), UoW: postgres.NewUnitOfWork(pool),
		Clock: clock.System{}, IDs: ids.UUID{},
	}}
}

// buildServer wires every use case into the REST server.
func buildServer(ad adapters, readiness *inboundhttp.Readiness, logger *slog.Logger) *inboundhttp.Server {
	d := ad.deps
	return &inboundhttp.Server{
		CreateCatalogItem:  usecases.NewCreateCatalogItem(d),
		DiscontinueItem:    usecases.NewDiscontinueItem(d),
		BrowseCatalog:      usecases.NewBrowseCatalog(d.Catalog),
		GetCatalogItem:     usecases.NewGetCatalogItem(d.Catalog),
		CreateListing:      usecases.NewCreateListing(d),
		ListListings:       usecases.NewListListings(d.Listings),
		ApplyInventoryFeed: usecases.NewApplyInventoryFeed(d),
		GetInventoryFeed:   usecases.NewGetInventoryFeed(d.Feeds),
		Idempotency:        inboundhttp.RequireIdempotencyKey(ad.pool, pgtx.With),
		Readiness:          readiness,
		Logger:             logger,
	}
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM, ctx cancellation or a
// listen error, then shuts down in the fleet order: (1) /readyz flips to 503,
// (2) wait the drain delay, (3) drain HTTP, (4) stop and await the workers
// (relay, consumer, janitor) LAST, so nothing committed above is stranded; the
// caller's deferred pool.Close runs after all of it. When a worker returns on its
// own the service cannot run: it exits non-zero so the orchestrator restarts it.
func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	drainDelay time.Duration, workers []*worker, stops []func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	died := make(chan *worker, len(workers))
	for _, w := range workers {
		go func() {
			<-w.done
			died <- w
		}()
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		stopAll(logger, workers, stops)
		return fmt.Errorf("http server: %w", err)
	case w := <-died:
		stopAll(logger, workers, stops)
		_ = httpServer.Close()
		return fmt.Errorf("%s stopped unexpectedly: exiting so the orchestrator restarts the service", w.name)
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if drainDelay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", drainDelay.String())
		time.Sleep(drainDelay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)
	stopAll(logger, workers, stops)
	return err
}

// stopAll cancels every worker, then awaits each one (bounded).
func stopAll(logger *slog.Logger, workers []*worker, stops []func()) {
	for _, stop := range stops {
		stop()
	}
	for _, w := range workers {
		select {
		case <-w.done:
		case <-time.After(workerDrainTimeout):
			logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
		}
	}
}
