//go:build integration

package main

// The harness of the real-wiring test (TestRealWiring in e2e_integration_test.go):
// the running binary, its REST client and the producer-side event builders.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/offer-availability/internal/adapters/inbound/kafka"
	"github.com/claudioed/offer-availability/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres"
	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

const eventsTopic = "retailnetwork.offer-availability.events"

// service is the running binary under test.
type service struct {
	t      *testing.T
	base   string
	pool   *pgtest.Pool
	cancel context.CancelFunc
	done   chan error
	keys   int
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// boot starts run(ctx) on a fresh database (no migration applied: the binary
// migrates at boot) and waits until /healthz answers.
func boot(t *testing.T) *service {
	t.Helper()
	dbURL := pgtest.EmptyURL(t)
	addr := freeAddr(t)
	t.Setenv("DATABASE_URL", dbURL)
	kafkatest.SetBrokersEnv(t)
	t.Setenv("NODE_TOPOLOGY_CONSUMER_GROUP", fmt.Sprintf("offer-availability-e2e-%d", time.Now().UnixNano()))
	t.Setenv("SELLER_INBOUND_CONSUMER_GROUP", fmt.Sprintf("offer-availability-seller-e2e-%d", time.Now().UnixNano()))
	// A consumer group joined while its topic does not exist gets no partition and
	// never picks the topic up later (docs/docs/operations/troubleshooting.md), so
	// the seller topic exists before the binary starts, as the deployment ensures.
	// The seed is a type this service does not consume: counted and skipped.
	kafkatest.Produce(t, inboundkafka.SellerTopic, sellerEvent("evt-seed", "inboundshipment.InboundShipmentShipped",
		`{"shipmentId":"shp-0","sellerId":"seller-0","nodeId":"node-0","shippedAt":"2026-10-23T08:00:00Z"}`))
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "100ms")
	t.Setenv("LOG_LEVEL", "error")

	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, base: "http://" + addr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	t.Cleanup(func() { cancel(); s.waitStopped(30 * time.Second) })

	s.eventually(90*time.Second, "healthz", func() bool { return s.get("/healthz").status == http.StatusOK })
	pool, err := postgres.NewPool(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s.pool = pool
	return s
}

func (s *service) waitStopped(timeout time.Duration) error {
	select {
	case err := <-s.done:
		s.done <- err
		return err
	case <-time.After(timeout):
		s.t.Errorf("run(ctx) did not return within %s of the cancellation", timeout)
		return nil
	}
}

func (s *service) eventually(timeout time.Duration, what string, ok func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for !ok() {
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *service) call(method, path, body, key string) response {
	s.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.base+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return response{status: -1, body: []byte(err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: raw}
}

func (s *service) get(path string) response { return s.call(http.MethodGet, path, "", "") }

func decode(t *testing.T, r response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("body is not a JSON object: %s", r.body)
	}
	return out
}

// write sends a state-changing request under a fresh key and requires the status.
func (s *service) write(method, path, body string, want int) map[string]any {
	s.t.Helper()
	s.keys++
	r := s.call(method, path, body, fmt.Sprintf("e2e-%d", s.keys))
	if r.status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, r.status, r.body, want)
	}
	return decode(s.t, r)
}

func (s *service) count(sql string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

// nodeEvent is a node-topology CloudEvent as that producer writes it (only the
// fields this service reads, plus the envelope).
func nodeEvent(id, name, node, field, at string) kafkatest.Message {
	data := fmt.Sprintf(`{"nodeId":%q`, node)
	if field != "" {
		data += fmt.Sprintf(`,%q:%q`, field, at)
	}
	data += `}`
	value := fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/retailnetwork/node-topology","type":"com.retailnetwork.logistics.node-topology.node.%s",`+
		`"subject":%q,"datacontenttype":"application/json","time":%q,"data":%s}`, id, name, node, at, data)
	return kafkatest.Message{Key: node, Value: []byte(value), Headers: map[string]string{"content-type": cloudevents.MediaType}}
}
