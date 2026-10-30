package usecases_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/usecases"
)

// recordingMetrics notes each ports.FeedMetrics call as "outcome" or
// "outcome/reason".
type recordingMetrics struct{ calls []string }

func (m *recordingMetrics) FeedApplied(context.Context)    { m.calls = append(m.calls, "applied") }
func (m *recordingMetrics) FeedSuperseded(context.Context) { m.calls = append(m.calls, "superseded") }
func (m *recordingMetrics) FeedFailed(_ context.Context, reason string) {
	m.calls = append(m.calls, "failed/"+reason)
}

func (m *recordingMetrics) FeedRejected(_ context.Context, reason string) {
	m.calls = append(m.calls, "rejected/"+reason)
}

func metricsEnv(t *testing.T) (*env, *recordingMetrics) {
	t.Helper()
	e := feedEnv(t)
	m := &recordingMetrics{}
	e.deps.Metrics = m
	return e, m
}

func wantCalls(t *testing.T, m *recordingMetrics, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(m.calls, want) {
		t.Fatalf("metric calls = %v, want %v", m.calls, want)
	}
}

func TestFeedMetricsCountsAnAppliedFeedOnce(t *testing.T) {
	e, m := metricsEnv(t)
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	mustNot(t, err)
	wantCalls(t, m, "applied")
}

func TestFeedMetricsCountsASupersededFeed(t *testing.T) {
	e, m := metricsEnv(t)
	_, err := apply(e, feedCmd("feed-2", false, asOf2, ln("lst-a", 20)))
	mustNot(t, err)
	_, err = apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 10)))
	mustNot(t, err)
	wantCalls(t, m, "applied", "superseded")
}

func TestFeedMetricsCountsAFailedFeedWithTheBrokenInvariant(t *testing.T) {
	e, m := metricsEnv(t)
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 5), ln("lst-x", 5)))
	if err == nil {
		t.Fatal("a line of another node must fail the feed")
	}
	_, err = apply(e, feedCmd("feed-2", false, asOf1, ln("lst-b", -1)))
	if err == nil {
		t.Fatal("a negative quantity must fail the feed")
	}
	wantCalls(t, m, "failed/listing-not-on-node", "failed/negative-availability")
}

func TestFeedMetricsCountsARefusalWithItsReason(t *testing.T) {
	e, m := metricsEnv(t)
	_, err := apply(e, feedCmd("", false, asOf1, ln("lst-a", 1)))
	if err == nil {
		t.Fatal("a feed without an id must be refused")
	}
	_, err = apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 1)))
	mustNot(t, err)
	_, err = apply(e, feedCmd("feed-1", false, asOf2, ln("lst-a", 2)))
	if err == nil {
		t.Fatal("an applied feed id sent again must be refused")
	}
	wantCalls(t, m, "rejected/validation-failed", "applied", "rejected/feed-already-applied")
}

func TestFeedMetricsCountsAnInfrastructureFailureAsInternalError(t *testing.T) {
	e, m := metricsEnv(t)
	e.marks.lockErr = errBoom
	_, err := apply(e, feedCmd("feed-1", true, asOf1, ln("lst-a", 5)))
	wantIs(t, err, errBoom)
	wantCalls(t, m, "rejected/internal-error")
}

// Without a configured FeedMetrics the use case records nothing and still works.
func TestFeedMetricsNilIsANoOp(t *testing.T) {
	e := feedEnv(t)
	if e.deps.Metrics != nil {
		t.Fatal("the default Deps must carry no metrics")
	}
	if _, err := usecases.NewApplyInventoryFeed(e.deps).Execute(context.Background(), feedCmd("feed-1", false, asOf1, ln("lst-a", 1))); err != nil {
		t.Fatal(err)
	}
}
