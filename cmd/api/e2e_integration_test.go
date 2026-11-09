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
	"net/http"
	"strings"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/offer-availability/internal/adapters/inbound/kafka"
	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
)

type world struct {
	bottle, mug, lstBottleA, lstMugA, lstBottleB, lstToteSeller, lstMugSeller string
	// napkin and lstNapkinSeller come from received stock (ADR 0014).
	napkin, lstNapkinSeller string
}

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
	t.Run("seller-inbound suspends and reinstates a seller", func(t *testing.T) { sellerFlow(t, s, &w) })
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
	av := decode(t, s.get("/listings?nodeId=node-a&catalogItemId="+w.bottle))["items"].([]any)[0].(map[string]any)["availability"].(map[string]any)
	if av["availableQty"] != float64(120) || av["basis"] != "THROUGHPUT_CONSTRAINED" || av["asOf"] != "2026-10-23T10:00:00Z" {
		t.Fatalf("availability = %v", av)
	}
	// A full feed leaves an omitted listing that is already at 0 untouched (ADR 0002, A10).
	mug := decode(t, s.get("/listings?catalogItemId="+w.mug))["items"].([]any)[0].(map[string]any)["availability"].(map[string]any)
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
	if other.status != http.StatusConflict || str(decode(t, other), "type") != "idempotency-key-reused" {
		t.Fatalf("same key, other body: %d %s", other.status, other.body)
	}
}

func nodeFlow(t *testing.T, s *service, w world) {
	t.Helper()
	state := func(id string) string {
		for _, l := range decode(t, s.get("/listings?nodeId=node-a"))["items"].([]any) {
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
	if av := decode(t, s.get("/listings?nodeId=node-a&catalogItemId="+w.bottle))["items"].([]any)[0].(map[string]any)["availability"].(map[string]any); av["availableQty"] != float64(120) {
		t.Fatalf("suppressed listing lost its availability: %v", av)
	}

	// A discontinued item stays suppressed for good, whatever the node does.
	d := s.write(http.MethodPost, "/catalog-items/"+w.bottle+"/discontinuations", "", http.StatusOK)
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
