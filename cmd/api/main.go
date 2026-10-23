// Command api is offer-availability's composition root: it wires the environment
// into the Postgres and Kafka adapters, the adapters into the use cases and
// the outbox relay, all on real Postgres and real Kafka. There is no
// in-memory mode, no log sink and no relay that is off.
//
// Environment:
//
//	DATABASE_URL             Postgres DSN (REQUIRED)
//	MIGRATIONS_DATABASE_URL  direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS            comma-separated brokers (REQUIRED: relay, /readyz)
//	HTTP_ADDR                listen address (default :8080)
//	OUTBOX_RELAY_INTERVAL    relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_MAX_ATTEMPTS      failures before the relay excludes a row (default 20, 0 = unlimited; ADR 0005)
//	SHUTDOWN_DRAIN_DELAY     wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                debug|info|warn|error (default info)
//
// The process exits non-zero when the relay returns on its own, so the
// orchestrator restarts it.
package main

import (
	"context"
	"log/slog"
	"os"
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}
