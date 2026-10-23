package main

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	outboxrelay "github.com/claudioed/offer-availability/internal/adapters/outbound/outbox"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
)

// The environment variables of the service. DATABASE_URL and KAFKA_BROKERS are
// mandatory: a missing one is a boot error naming it, raised before anything
// is opened or bound (there is no in-memory mode and no log sink).
const (
	envKafkaBrokers      = "KAFKA_BROKERS"
	envNodeConsumerGroup = "NODE_TOPOLOGY_CONSUMER_GROUP"
	envRetentionInterval = "RETENTION_INTERVAL"
	envHTTPAddr          = "HTTP_ADDR"
	envRelayInterval     = "OUTBOX_RELAY_INTERVAL"
	envMaxAttempts       = "OUTBOX_MAX_ATTEMPTS"
	envShutdownDrainWait = "SHUTDOWN_DRAIN_DELAY"
	envLogLevel          = "LOG_LEVEL"
)

// Defaults of the optional settings.
const (
	defaultHTTPAddr        = ":8080"
	defaultRetentionEvery  = time.Hour
	defaultShutdownDrain   = 5 * time.Second
	shutdownTimeout        = 10 * time.Second
	workerDrainTimeout     = 10 * time.Second
	defaultReadHeaderLimit = 5 * time.Second
)

// ErrNoBrokers is the boot error of an unset or blank KAFKA_BROKERS.
var ErrNoBrokers = errors.New("missing required environment variable: " + envKafkaBrokers + " (comma-separated host:port list)")

// ErrNoConsumerGroup is the boot error of an unset or blank NODE_TOPOLOGY_CONSUMER_GROUP:
// the group id is never defaulted, so a locally run process cannot silently join a
// live cluster's group.
var ErrNoConsumerGroup = errors.New("missing required environment variable: " + envNodeConsumerGroup)

// config is the validated environment.
type config struct {
	databaseURL   string
	migrationsURL string
	brokers       string
	nodeGroup     string
	retention     time.Duration
	addr          string
	relayInterval time.Duration
	maxAttempts   int
	drainDelay    time.Duration
	logLevel      string
}

// loadConfig validates the environment before anything is opened or bound.
// Every boot error names the variable: DATABASE_URL, KAFKA_BROKERS.
func loadConfig(getenv func(string) string, logger *slog.Logger) (config, error) {
	dbURL, err := postgres.DatabaseURL(getenv)
	if err != nil {
		return config{}, err
	}
	brokers := normalizeBrokers(getenv(envKafkaBrokers))
	if brokers == "" {
		return config{}, ErrNoBrokers
	}
	group := strings.TrimSpace(getenv(envNodeConsumerGroup))
	if group == "" {
		return config{}, ErrNoConsumerGroup
	}
	return config{
		databaseURL:   dbURL,
		nodeGroup:     group,
		retention:     retentionInterval(getenv(envRetentionInterval), logger),
		migrationsURL: postgres.MigrationsURL(getenv, dbURL),
		brokers:       brokers,
		addr:          orDefault(getenv(envHTTPAddr), defaultHTTPAddr),
		relayInterval: parseRelayInterval(getenv(envRelayInterval), logger),
		maxAttempts:   outboxMaxAttempts(getenv(envMaxAttempts), logger),
		drainDelay:    shutdownDrainDelay(getenv(envShutdownDrainWait), logger),
		logLevel:      getenv(envLogLevel),
	}, nil
}

// retentionInterval reads RETENTION_INTERVAL (a Go duration): how often the janitor
// deletes rows past their retention window. Unset, malformed or non-positive = 1h.
func retentionInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return defaultRetentionEvery
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Warn("invalid "+envRetentionInterval+"; using the default", "value", raw, "default", defaultRetentionEvery.String())
		return defaultRetentionEvery
	}
	return d
}

// normalizeBrokers trims every entry of the comma-separated list and drops
// blanks; an empty result is a missing KAFKA_BROKERS.
func normalizeBrokers(raw string) string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return strings.Join(out, ",")
}

// brokerList splits the normalised comma-separated KAFKA_BROKERS.
func brokerList(brokers string) []string { return strings.Split(brokers, ",") }

func orDefault(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (a Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid "+envRelayInterval+"; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS: how many times an outbox row
// may fail before the relay excludes it from claims. Unset = the fleet default
// (20); 0 = unlimited; negative or unparsable values log a warning and use the
// default, so a typo never silently removes the cap (ADR 0005).
func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		logger.Warn("invalid "+envMaxAttempts+"; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}

// shutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY: how long shutdown waits after
// flipping /readyz to not-ready before closing the listener ("0" disables;
// unset, negative or unparsable = the default).
func shutdownDrainDelay(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return defaultShutdownDrain
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn(fmt.Sprintf("ignoring invalid %s", envShutdownDrainWait), "value", raw, "default", defaultShutdownDrain.String())
		return defaultShutdownDrain
	}
	return d
}

// newLogLevel maps LOG_LEVEL (debug|info|warn|warning|error, any case) to a
// slog level; anything else is info.
func newLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
