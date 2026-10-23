// Package bootretry provides the exponential-backoff retry helper the
// composition root (cmd/api) uses to survive its very first outbound dial.
//
// In this fleet the first outbound TCP dial of an injected pod (Postgres,
// Kafka) can be reset shortly after the app starts (native sidecars). A single
// attempt would turn that transient condition into a crash loop. The retry is
// not a weakening of any fail-closed rule: after the budget is exhausted the
// caller still refuses to boot.
package bootretry

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Retries and Delay bound the startup retry budget: 5 attempts, the backoff
// slept only BETWEEN them (1+2+4+8 = 15 s in total).
const (
	Retries = 5
	Delay   = time.Second
)

// Retry runs op with exponential backoff (base Delay, Retries attempts),
// returning the LAST error so a permanent failure still reports its real
// cause.
func Retry(ctx context.Context, logger *slog.Logger, what string, op func() error) error {
	return RetryWithDelay(ctx, logger, what, Delay, op)
}

// RetryWithDelay is Retry with the base delay injected, so tests can exercise
// the give-up path without sleeping out the real budget. A cancelled ctx
// abandons the retries (the error wraps ctx.Err()).
func RetryWithDelay(ctx context.Context, logger *slog.Logger, what string, base time.Duration, op func() error) error {
	if logger == nil {
		logger = slog.Default()
	}
	delay := base
	var err error
	for attempt := 1; attempt <= Retries; attempt++ {
		if err = op(); err == nil {
			if attempt > 1 {
				logger.Info("succeeded after retry", "op", what, "attempt", attempt)
			}
			return nil
		}
		if attempt == Retries {
			break
		}
		logger.Warn("retrying", "op", what, "attempt", attempt, "in", delay, "err", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(delay):
		}
		delay *= 2
	}
	return fmt.Errorf("%s (after %d attempts): %w", what, Retries, err)
}
