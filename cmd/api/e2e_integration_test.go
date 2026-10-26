//go:build integration

package main

// The real-wiring test boots run(ctx), the production entry point, against a
// real Postgres and a real Kafka broker (both testcontainers). It drives the
// service ONLY through REST and through the node-topology topic and reads the
// result ONLY from Postgres and from the output topic. Nothing here is a
// double: the use cases, the Postgres repositories, the Idempotency-Key
// middleware, the outbox relay, the encoder, the Kafka writer and the consumer
// with its dead-letter topic are the ones the binary runs.

import (
	"bytes"
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

type world struct{ bottle, mug, lstBottleA, lstMugA, lstBottleB string }

func TestRealWiring(t *testing.T) {
	s := boot(t)

	t.Run("ready while the output topic does not exist", func(t *testing.T) {
		r := s.get("/readyz")
		if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != `{"status":"ready"}` {
			t.Fatalf("/readyz = %d %s, want 200 ready", r.status, r.body)
		}
		if n := kafkatest.Count(t, eventsTopic); n != 0 {
			t.Fatalf("the topic already holds %d messages before the first write", n)
		}
	})

	var w world
	t.Run("node-topology activates a node", func(t *testing.T) {
		kafkatest.Produce(t, inboundkafka.NodeTopic, nodeEvent("evt-a1", "NodeActivated", "node-a", "activatedAt", "2026-10-23T09:00:00Z"))
		s.eventually(60*time.Second, "node-a ACTIVE in the local view", func() bool {
			return s.count(`SELECT count(*) FROM node_status_view WHERE node_id = 'node-a' AND status = 'ACTIVE'`) == 1
		})
	})
	t.Run("REST flow", func(t *testing.T) { w = restFlow(t, s) })
	t.Run("idempotent replay", func(t *testing.T) { assertReplay(t, s) })
	t.Run("node-topology suspends, reactivates and sends noise", func(t *testing.T) { nodeFlow(t, s, w) })
	t.Run("Postgres rows", func(t *testing.T) { assertRows(t, s) })
	t.Run("exact CloudEvents on the topic", func(t *testing.T) { assertTopic(t, s, w) })
	t.Run("Kafka down", func(t *testing.T) { assertKafkaDown(t, s) })
}

func restFlow(t *testing.T, s *service) world {
	t.Helper()
	var w world
	w.bottle = str(s.write(http.MethodPost, "/catalog-items", `{"title":"Stainless steel water bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"fragile":true}`, http.StatusCreated), "catalogItemId")
	w.mug = str(s.write(http.MethodPost, "/catalog-items", `{"title":"Ceramic mug","lengthMm":100,"widthMm":100,"heightMm":110,"weightG":300}`, http.StatusCreated), "catalogItemId")

	a1 := s.write(http.MethodPost, "/listings", `{"catalogItemId":"`+w.bottle+`","nodeId":"node-a"}`, http.StatusCreated)
	a2 := s.write(http.MethodPost, "/listings", `{"catalogItemId":"`+w.mug+`","nodeId":"node-a"}`, http.StatusCreated)
	b1 := s.write(http.MethodPost, "/listings", `{"catalogItemId":"`+w.bottle+`","nodeId":"node-b"}`, http.StatusCreated)
	w.lstBottleA, w.lstMugA, w.lstBottleB = str(a1, "listingId"), str(a2, "listingId"), str(b1, "listingId")
	if str(a1, "state") != "LIVE" || str(a2, "state") != "LIVE" {
		t.Fatalf("listings on the ACTIVE node: %v %v", a1, a2)
	}
	if str(b1, "state") != "SUPPRESSED" || str(b1, "suppressionReason") != "NODE_NOT_SELLABLE" {
		t.Fatalf("a listing on a node nobody announced: %v", b1)
	}

	feed := s.write(http.MethodPost, "/inventory-feeds", `{"feedId":"feed-1","nodeId":"node-a","fullUpdate":true,"asOf":"2026-10-23T10:00:00Z","lines":[`+
		`{"listingId":"`+w.lstBottleA+`","availableQty":120,"basis":"THROUGHPUT_CONSTRAINED"}]}`, http.StatusCreated)
	if str(feed, "state") != "APPLIED" || feed["appliedCount"] != float64(1) || feed["zeroedCount"] != float64(0) || feed["staleCount"] != float64(0) {
		t.Fatalf("feed = %v", feed)
	}
	g := decode(t, s.get("/inventory-feeds/feed-1"))
	if str(g, "state") != "APPLIED" {
		t.Fatalf("GET feed = %v", g)
	}
	av := decode(t, s.get("/listings?nodeId=node-a&catalogItemId="+w.bottle))["listings"].([]any)[0].(map[string]any)["availability"].(map[string]any)
	if av["availableQty"] != float64(120) || av["basis"] != "THROUGHPUT_CONSTRAINED" || av["asOf"] != "2026-10-23T10:00:00Z" {
		t.Fatalf("availability = %v", av)
	}
	// A full feed leaves an omitted listing that is already at 0 untouched (ADR 0002, A10).
	mug := decode(t, s.get("/listings?catalogItemId="+w.mug))["listings"].([]any)[0].(map[string]any)["availability"].(map[string]any)
	if mug["availableQty"] != float64(0) || mug["basis"] != "UNKNOWN" || mug["asOf"] != nil {
		t.Fatalf("the omitted listing at 0 changed: %v", mug)
	}

	// A stale feed is SUPERSEDED, a bad one is FAILED and recorded, both over REST.
	if old := s.write(http.MethodPost, "/inventory-feeds", `{"feedId":"feed-0","nodeId":"node-a","fullUpdate":false,"asOf":"2026-10-23T09:00:00Z","lines":[`+
		`{"listingId":"`+w.lstBottleA+`","availableQty":1,"basis":"PHYSICAL"}]}`, http.StatusCreated); str(old, "state") != "SUPERSEDED" {
		t.Fatalf("older feed = %v", old)
	}
	bad := s.write(http.MethodPost, "/inventory-feeds", `{"feedId":"feed-bad","nodeId":"node-a","fullUpdate":false,"asOf":"2026-10-23T11:00:00Z","lines":[`+
		`{"listingId":"`+w.lstBottleB+`","availableQty":1,"basis":"PHYSICAL"}]}`, http.StatusUnprocessableEntity)
	if str(bad, "type") != "listing-not-on-node" || str(bad, "instance") != "/inventory-feeds/feed-bad" {
		t.Fatalf("failed feed problem = %v", bad)
	}
	if g := decode(t, s.get("/inventory-feeds/feed-bad")); str(g, "state") != "FAILED" || str(g, "failureCode") != "ErrListingNotOnNode" {
		t.Fatalf("the FAILED feed is stored: %v", g)
	}
	if dup := s.call(http.MethodPost, "/catalog-items", `{"title":"x"}`, ""); dup.status != http.StatusBadRequest || str(decode(t, dup), "type") != "idempotency-key-required" {
		t.Fatalf("no key: %d %s", dup.status, dup.body)
	}
	return w
}

func assertReplay(t *testing.T, s *service) {
	t.Helper()
	body := `{"title":"Replay item","lengthMm":10,"widthMm":10,"heightMm":10,"weightG":10}`
	before := s.count(`SELECT count(*) FROM catalog_items`)
	first := s.call(http.MethodPost, "/catalog-items", body, "replay-key")
	again := s.call(http.MethodPost, "/catalog-items", body, "replay-key")
	if first.status != http.StatusCreated || again.status != http.StatusCreated || string(first.body) != string(again.body) || again.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d %s / %d %s %v", first.status, first.body, again.status, again.body, again.header)
	}
	if n := s.count(`SELECT count(*) FROM catalog_items`); n != before+1 {
		t.Fatalf("one key created %d items", n-before)
	}
	other := s.call(http.MethodPost, "/catalog-items", strings.Replace(body, "Replay", "Other", 1), "replay-key")
	if other.status != http.StatusUnprocessableEntity || str(decode(t, other), "type") != "idempotency-key-reused" {
		t.Fatalf("same key, other body: %d %s", other.status, other.body)
	}
}

func nodeFlow(t *testing.T, s *service, w world) {
	t.Helper()
	state := func(id string) string {
		for _, l := range decode(t, s.get("/listings?nodeId=node-a"))["listings"].([]any) {
			if m := l.(map[string]any); str(m, "listingId") == id {
				return str(m, "state") + "/" + str(m, "suppressionReason")
			}
		}
		return "missing"
	}
	waitState := func(id, want string) {
		s.eventually(60*time.Second, id+" "+want, func() bool { return state(id) == want })
	}

	kafkatest.Produce(t, inboundkafka.NodeTopic, nodeEvent("evt-a2", "NodeSuspended", "node-a", "suspendedAt", "2026-10-23T12:00:00Z"))
	waitState(w.lstBottleA, "SUPPRESSED/NODE_NOT_SELLABLE")
	waitState(w.lstMugA, "SUPPRESSED/NODE_NOT_SELLABLE")
	// Suppressing keeps the availability the node declared.
	if av := decode(t, s.get("/listings?nodeId=node-a&catalogItemId="+w.bottle))["listings"].([]any)[0].(map[string]any)["availability"].(map[string]any); av["availableQty"] != float64(120) {
		t.Fatalf("suppressed listing lost its availability: %v", av)
	}

	// A discontinued item stays suppressed for good, whatever the node does.
	d := s.write(http.MethodPost, "/catalog-items/"+w.bottle+":discontinue", "", http.StatusOK)
	if str(d, "state") != "DISCONTINUED" {
		t.Fatalf("discontinue = %v", d)
	}

	// The same event twice, an event older than the status, an unknown type and noise.
	kafkatest.Produce(t, inboundkafka.NodeTopic,
		nodeEvent("evt-a2", "NodeSuspended", "node-a", "suspendedAt", "2026-10-23T12:00:00Z"),
		nodeEvent("evt-old", "NodeActivated", "node-a", "activatedAt", "2026-10-23T08:00:00Z"),
		kafkatest.Message{Key: "node-a", Value: []byte("not a cloudevent")},
		nodeEvent("evt-lane", "LaneOpened", "node-a", "", "2026-10-23T12:30:00Z"),
		kafkatest.Message{Key: "node-a", Value: []byte(`{"specversion":"1.0","id":"evt-poison","source":"/x","type":"com.retailnetwork.logistics.node-topology.node.NodeRetired",` +
			`"subject":"node-a","datacontenttype":"application/json","time":"2026-10-23T13:00:00Z","data":{"retiredAt":"2026-10-23T13:00:00Z"}}`)},
	)
	dlq := kafkatest.ReadN(t, inboundkafka.NodeTopic+".dlq", 1, 90*time.Second)
	if dlq[0].Headers["x-dlq-source-topic"] != inboundkafka.NodeTopic || !strings.Contains(dlq[0].Headers["x-dlq-error"], "nodeId") || !strings.Contains(string(dlq[0].Value), "evt-poison") {
		t.Fatalf("dead letter = %+v", dlq[0])
	}

	kafkatest.Produce(t, inboundkafka.NodeTopic, nodeEvent("evt-a3", "NodeActivated", "node-a", "activatedAt", "2026-10-23T14:00:00Z"))
	waitState(w.lstMugA, "LIVE/")
	if got := state(w.lstBottleA); got != "SUPPRESSED/DISCONTINUED" {
		t.Fatalf("the discontinued listing = %s, want SUPPRESSED/DISCONTINUED", got)
	}
	// The consumer stopped nowhere: the poison event did not wedge the partition.
	s.eventually(30*time.Second, "all node events processed", func() bool {
		return s.count(`SELECT count(*) FROM processed_events`) == 4
	})
}

func assertRows(t *testing.T, s *service) {
	t.Helper()
	for table, want := range map[string]int{"catalog_items": 3, "listings": 3, "inventory_feeds": 3, "node_status_view": 1, "processed_events": 4, "node_feed_marks": 1} {
		if got := s.count(`SELECT count(*) FROM ` + table); got != want {
			t.Errorf("%s holds %d rows, want %d", table, got, want)
		}
	}
	if n := s.count(`SELECT count(*) FROM inventory_feeds WHERE feed_id = 'feed-0' AND state = 'SUPERSEDED'`); n != 1 {
		t.Errorf("the SUPERSEDED feed row: %d", n)
	}
	var qty, version int
	var basis, state, reason string
	if err := s.pool.QueryRow(context.Background(), `SELECT available_qty, basis, state, coalesce(suppression_reason,''), version FROM listings WHERE listing_id = (SELECT listing_id FROM listings WHERE node_id = 'node-a' AND state = 'SUPPRESSED' LIMIT 1)`).
		Scan(&qty, &basis, &state, &reason, &version); err != nil {
		t.Fatalf("read the discontinued listing: %v", err)
	}
	if qty != 120 || basis != "THROUGHPUT_CONSTRAINED" || reason != "DISCONTINUED" {
		t.Errorf("listing row = %d %s %s %s v%d", qty, basis, state, reason, version)
	}
	if n := s.count(`SELECT count(*) FROM idempotency_keys WHERE response_status IS NULL`); n != 0 {
		t.Errorf("%d idempotency keys without their outcome", n)
	}
}

// expectedTypes is the exact order of the events the whole flow raises: it is the
// outbox order, and the topic has one partition.
var expectedTypes = []string{
	"catalogitem.CatalogItemCreated", "catalogitem.CatalogItemCreated",
	"listing.ListingCreated", "listing.ListingCreated", "listing.ListingCreated", "listing.ListingSuppressed",
	"listing.ListingAvailabilityChanged", "inventoryfeed.InventoryFeedApplied",
	"catalogitem.CatalogItemCreated",
	"listing.ListingSuppressed", "listing.ListingSuppressed",
	"catalogitem.CatalogItemDiscontinued", "listing.ListingSuppressed", "listing.ListingSuppressed",
	"listing.ListingRestored",
}

func assertTopic(t *testing.T, s *service, w world) {
	t.Helper()
	type row struct {
		eventID, typ, subject string
		value                 []byte
	}
	var rows []row
	r, err := s.pool.Query(context.Background(), `SELECT event_id, event_type, subject, value FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x row
		if err := r.Scan(&x.eventID, &x.typ, &x.subject, &x.value); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()

	if len(rows) != len(expectedTypes) {
		var got []string
		for _, x := range rows {
			got = append(got, strings.TrimPrefix(x.typ, "com.retailnetwork.commerce.offer-availability."))
		}
		t.Fatalf("outbox holds %d events, want %d:\n%s", len(rows), len(expectedTypes), strings.Join(got, "\n"))
	}
	pgtest.WaitAllPublished(t, s.pool)
	got := kafkatest.ReadN(t, eventsTopic, len(rows), 60*time.Second)
	seen := map[string]bool{}
	for i, msg := range got {
		want := "com.retailnetwork.commerce.offer-availability." + expectedTypes[i]
		e, err := cloudevents.Decode(msg.Value)
		if err != nil {
			t.Fatalf("message %d is not a CloudEvent: %v", i, err)
		}
		name := expectedTypes[i][strings.Index(expectedTypes[i], ".")+1:]
		switch {
		case !bytes.Equal(msg.Value, rows[i].value):
			t.Errorf("%s: the message is not the stored outbox bytes", want)
		case e.Type() != want || rows[i].typ != want || e.ID() != rows[i].eventID || e.Subject() != rows[i].subject || msg.Key != rows[i].subject:
			t.Errorf("message %d: type %q id %q subject %q key %q, want %s from outbox row %+v", i, e.Type(), e.ID(), e.Subject(), msg.Key, want, rows[i])
		case e.Source() != "/retailnetwork/offer-availability" || e.DataContentType() != "application/json" ||
			e.DataSchema() != "urn:retailnetwork:offer-availability:events:"+name+":v1" || e.SpecVersion() != "1.0":
			t.Errorf("%s: attributes source %q dct %q dataschema %q spec %q", want, e.Source(), e.DataContentType(), e.DataSchema(), e.SpecVersion())
		case len(msg.Headers) != 2 || msg.Headers["content-type"] != cloudevents.MediaType || msg.Headers["traceparent"] == "":
			// traceparent is the only header besides content-type (ADR 0009); the CloudEvents attributes are untouched.
			t.Errorf("%s: headers %v", want, msg.Headers)
		case e.Time().IsZero() || e.Time().Location() != time.UTC || seen[e.ID()]:
			t.Errorf("%s: time %v, duplicate id %v", want, e.Time(), seen[e.ID()])
		}
		seen[e.ID()] = true
	}

	data := func(i int) map[string]any {
		e, _ := cloudevents.Decode(got[i].Value)
		var d map[string]any
		if err := json.Unmarshal(e.Data(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := data(0); str(d, "catalogItemId") != w.bottle || str(d, "title") != "Stainless steel water bottle" || d["lengthMm"] != float64(270) || d["fragile"] != true || d["hazmat"] != false {
		t.Errorf("CatalogItemCreated data = %v", d)
	}
	if d := data(5); str(d, "listingId") != w.lstBottleB || str(d, "nodeId") != "node-b" || str(d, "reason") != "NODE_NOT_SELLABLE" {
		t.Errorf("ListingSuppressed data = %v", d)
	}
	if d := data(6); str(d, "listingId") != w.lstBottleA || d["availableQty"] != float64(120) || str(d, "basis") != "THROUGHPUT_CONSTRAINED" || str(d, "asOf") != "2026-10-23T10:00:00Z" || str(d, "feedId") != "feed-1" {
		t.Errorf("ListingAvailabilityChanged data = %v", d)
	}
	if d := data(7); str(d, "feedId") != "feed-1" || d["fullUpdate"] != true || d["lineCount"] != float64(1) || str(d, "appliedAt") == "" {
		t.Errorf("InventoryFeedApplied data = %v", d)
	}
	if d := data(14); str(d, "listingId") != w.lstMugA || str(d, "nodeId") != "node-a" {
		t.Errorf("ListingRestored data = %v", d)
	}
}

// assertKafkaDown stops the broker: /readyz must answer 503
// dependency-unavailable (the metadata check dials a fresh connection, so a
// cached answer cannot hide the outage), /healthz stays 200, a write is still
// accepted (the outbox decouples it from Kafka) and the process shuts down
// cleanly.
func assertKafkaDown(t *testing.T, s *service) {
	t.Helper()
	kafkatest.Stop(t)

	var last response
	s.eventually(90*time.Second, "/readyz 503", func() bool {
		last = s.get("/readyz")
		return last.status == http.StatusServiceUnavailable
	})
	if !strings.HasPrefix(last.header.Get("Content-Type"), "application/problem+json") || !strings.Contains(string(last.body), `"type":"dependency-unavailable"`) ||
		!strings.Contains(string(last.body), "kafka") || strings.Contains(string(last.body), "postgres") {
		t.Fatalf("/readyz = %d %s, want 503 dependency-unavailable naming only kafka", last.status, last.body)
	}
	if h := s.get("/healthz"); h.status != http.StatusOK {
		t.Fatalf("/healthz = %d with Kafka down, want 200 (liveness)", h.status)
	}
	s.write(http.MethodPost, "/catalog-items", `{"title":"Queued while Kafka is down","lengthMm":1,"widthMm":1,"heightMm":1,"weightG":1}`, http.StatusCreated)
	if pending := s.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`); pending < 1 {
		t.Fatalf("a write with Kafka down left %d unpublished outbox rows, want it queued", pending)
	}

	s.cancel()
	if err := s.waitStopped(60 * time.Second); err != nil {
		t.Fatalf("run(ctx) returned %v on a cancellation, want a clean shutdown", err)
	}
	if r := s.get("/healthz"); r.status != -1 {
		t.Fatalf("the listener still answers after the shutdown: %d", r.status)
	}
}
