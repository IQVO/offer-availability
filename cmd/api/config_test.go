package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func quietLogger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

var valid = map[string]string{
	"DATABASE_URL": "postgres://u:p@h/db", "KAFKA_BROKERS": "a:9092", "NODE_TOPOLOGY_CONSUMER_GROUP": "offer-availability",
}

func with(over map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range valid {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func TestLoadConfigNamesTheMissingVariable(t *testing.T) {
	logger, _ := quietLogger()
	for name, c := range map[string]struct {
		drop string
		want error
	}{
		"database": {"DATABASE_URL", postgres.ErrMissingEnv},
		"brokers":  {"KAFKA_BROKERS", ErrNoBrokers},
		"group":    {"NODE_TOPOLOGY_CONSUMER_GROUP", ErrNoConsumerGroup},
	} {
		t.Run(name, func(t *testing.T) {
			m := with(nil)
			delete(m, c.drop)
			_, err := loadConfig(env(m), logger)
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.drop) {
				t.Fatalf("err = %v, want one naming %s", err, c.drop)
			}
		})
	}
	// A blank value is as missing as an absent one.
	for k, blank := range map[string]string{"DATABASE_URL": "", "KAFKA_BROKERS": " , ", "NODE_TOPOLOGY_CONSUMER_GROUP": "   "} {
		if _, err := loadConfig(env(with(map[string]string{k: blank})), logger); err == nil {
			t.Errorf("a blank %s is accepted", k)
		}
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	logger, logs := quietLogger()
	cfg, err := loadConfig(env(valid), logger)
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		databaseURL: "postgres://u:p@h/db", migrationsURL: "postgres://u:p@h/db", brokers: "a:9092", nodeGroup: "offer-availability",
		addr: ":8080", relayInterval: time.Second, maxAttempts: 20, drainDelay: 5 * time.Second, retention: time.Hour,
	}
	if cfg != want {
		t.Fatalf("defaults = %+v, want %+v", cfg, want)
	}
	if logs.Len() != 0 {
		t.Fatalf("valid settings warned: %s", logs)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	logger, _ := quietLogger()
	cfg, err := loadConfig(env(with(map[string]string{
		"KAFKA_BROKERS": " a:1 , ,b:2 ", "HTTP_ADDR": ":9000", "OUTBOX_RELAY_INTERVAL": "250ms", "OUTBOX_MAX_ATTEMPTS": "0",
		"SHUTDOWN_DRAIN_DELAY": "0", "RETENTION_INTERVAL": "5m", "MIGRATIONS_DATABASE_URL": "postgres://direct/db", "NODE_TOPOLOGY_CONSUMER_GROUP": " grp ",
	})), logger)
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		databaseURL: "postgres://u:p@h/db", migrationsURL: "postgres://direct/db", brokers: "a:1,b:2", nodeGroup: "grp",
		addr: ":9000", relayInterval: 250 * time.Millisecond, maxAttempts: 0, drainDelay: 0, retention: 5 * time.Minute,
	}
	if cfg != want {
		t.Fatalf("overrides = %+v, want %+v", cfg, want)
	}
	if got := brokerList(cfg.brokers); len(got) != 2 || got[1] != "b:2" {
		t.Fatalf("brokerList = %v", got)
	}
}

func TestInvalidOptionalSettingsFallBackWithAWarning(t *testing.T) {
	logger, logs := quietLogger()
	cfg, err := loadConfig(env(with(map[string]string{
		"OUTBOX_RELAY_INTERVAL": "soon", "OUTBOX_MAX_ATTEMPTS": "-3", "SHUTDOWN_DRAIN_DELAY": "-1s", "RETENTION_INTERVAL": "0",
	})), logger)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.relayInterval != time.Second || cfg.maxAttempts != 20 || cfg.drainDelay != 5*time.Second || cfg.retention != time.Hour {
		t.Fatalf("fallbacks = %+v", cfg)
	}
	if got := strings.Count(logs.String(), "level=WARN"); got != 4 {
		t.Fatalf("%d warnings: %s", got, logs)
	}
	if parseRelayInterval("2", logger) != 2*time.Second || parseRelayInterval("0", logger) != time.Second {
		t.Fatal("plain seconds are accepted, zero is not")
	}
	if outboxMaxAttempts("x", logger) != 20 || outboxMaxAttempts(" 7 ", logger) != 7 {
		t.Fatal("max attempts")
	}
}

func TestNewLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "DEBUG": slog.LevelDebug, "warn": slog.LevelWarn, "warning": slog.LevelWarn,
		"error": slog.LevelError, "": slog.LevelInfo, "chatty": slog.LevelInfo,
	} {
		if got := newLogLevel(in); got != want {
			t.Errorf("newLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
