package bootretry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRetrySucceedsFirstTime(t *testing.T) {
	calls := 0
	if err := RetryWithDelay(context.Background(), quiet(), "op", time.Millisecond, func() error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one successful call", err, calls)
	}
}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	err := RetryWithDelay(context.Background(), nil, "op", time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("connection reset by peer")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want success on the third call", err, calls)
	}
}

func TestRetryGivesUpWithTheLastError(t *testing.T) {
	calls := 0
	last := errors.New("still down")
	err := RetryWithDelay(context.Background(), quiet(), "ping postgres", time.Millisecond, func() error { calls++; return last })
	if calls != Retries || !errors.Is(err, last) || !strings.Contains(err.Error(), "ping postgres (after 5 attempts)") {
		t.Fatalf("err=%v calls=%d, want %d attempts and the last error", err, calls, Retries)
	}
}

func TestRetryStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := RetryWithDelay(ctx, quiet(), "op", time.Hour, func() error { calls++; cancel(); return errors.New("down") })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want an abandoned retry after one call", err, calls)
	}
}

func TestRetryUsesTheFleetDelay(t *testing.T) {
	if Delay != time.Second || Retries != 5 {
		t.Fatalf("budget = %d x %v, want 5 x 1s", Retries, Delay)
	}
	if err := Retry(context.Background(), quiet(), "op", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
