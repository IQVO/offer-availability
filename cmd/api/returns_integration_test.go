//go:build integration

package main

// The customer-returns part of the real-wiring test (ADR 0015): run by
// TestRealWiring after the exact-topic assertion, against the same running
// binary, broker and database. It restocks the napkin listing the received
// stock flow created (ADR 0014) and proves the skip, poison and replay paths
// of the new consumer.

import (
	"strings"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/offer-availability/internal/adapters/inbound/kafka"
	"github.com/claudioed/offer-availability/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
)

const customerReturnsPrefix = "com.retailnetwork.commerce.customer-returns."

// returnEvent is a customer-returns CloudEvent as that producer writes it.
func returnEvent(id, typ, data string) kafkatest.Message {
	value := `{"specversion":"1.0","id":` + quote(id) + `,"source":"/retailnetwork/customer-returns","type":` + quote(customerReturnsPrefix+typ) + `,` +
		`"subject":` + quote("ret-e2e") + `,"datacontenttype":"application/json","time":"2026-11-14T09:00:00Z","data":` + data + `}`
	return kafkatest.Message{Key: "ret-e2e", Value: []byte(value), Headers: map[string]string{"content-type": cloudevents.MediaType}}
}

func quote(s string) string { return `"` + s + `"` }

// listingAvailability reads a listing's availability map over REST.
func listingAvailability(t *testing.T, s *service, item, node, owner string) map[string]any {
	t.Helper()
	for _, l := range decode(t, s.get("/listings?nodeId="+node+"&catalogItemId="+item))["items"].([]any) {
		if m := l.(map[string]any); str(m, "ownerRef") == owner {
			return m["availability"].(map[string]any)
		}
	}
	t.Fatalf("no listing of %s on %s owned by %s", item, node, owner)
	return nil
}

func returnsFlow(t *testing.T, s *service, w *world) {
	t.Helper()
	waitQty := func(qty float64) {
		s.eventually(60*time.Second, "the returned napkin restocked", func() bool {
			av := listingAvailability(t, s, w.napkin, "node-a", seller)
			return av["availableQty"] == qty
		})
	}

	// The restockable units raise the owner listing; the unsellable one never
	// touches it (ADR 0015). The never-fed listing keeps basis UNKNOWN and
	// stamps the pair with the return's instant and authorization id. The
	// outbox assertion is a DELTA: the earlier flows of this same run have
	// raised their own availability events.
	restockBase := s.count(`SELECT count(*) FROM outbox_events WHERE event_type = 'com.retailnetwork.commerce.offer-availability.listing.ListingAvailabilityChanged'`)
	kafkatest.Produce(t, inboundkafka.CustomerReturnsTopic, returnEvent("evt-ret-1", "returnauthorization.ReturnCompleted",
		`{"authorizationId":"ret-e2e-1","outcome":"RECEIVED","nodeId":"node-a","lines":[`+
			`{"catalogItemId":"`+w.napkin+`","ownerRef":"`+seller+`","qtyRestockable":2,"qtyUnsellable":1}],`+
			`"receivedAt":"2026-11-14T12:00:00Z"}`))
	waitQty(2)
	av := listingAvailability(t, s, w.napkin, "node-a", seller)
	if av["basis"] != "UNKNOWN" || av["asOf"] != "2026-11-14T12:00:00Z" || av["feedId"] != "ret-e2e-1" {
		t.Fatalf("restocked availability = %v", av)
	}
	if n := s.count(`SELECT count(*) FROM outbox_events WHERE event_type = 'com.retailnetwork.commerce.offer-availability.listing.ListingAvailabilityChanged'`) - restockBase; n != 1 {
		t.Fatalf("the restock raised %d ListingAvailabilityChanged events, want 1", n)
	}

	// A line whose listing does not exist is counted and skipped; a redelivery
	// of the first event changes nothing (the inbox).
	kafkatest.Produce(t, inboundkafka.CustomerReturnsTopic,
		returnEvent("evt-ret-2", "returnauthorization.ReturnCompleted",
			`{"authorizationId":"ret-e2e-2","outcome":"RECEIVED","nodeId":"node-a","lines":[`+
				`{"catalogItemId":"`+w.mug+`","ownerRef":"`+seller+`","qtyRestockable":5,"qtyUnsellable":0}],`+
				`"receivedAt":"2026-11-14T12:30:00Z"}`),
		returnEvent("evt-ret-1", "returnauthorization.ReturnCompleted",
			`{"authorizationId":"ret-e2e-1","outcome":"RECEIVED","nodeId":"node-a","lines":[`+
				`{"catalogItemId":"`+w.napkin+`","ownerRef":"`+seller+`","qtyRestockable":2,"qtyUnsellable":1}],`+
				`"receivedAt":"2026-11-14T12:00:00Z"}`),
	)
	waitQty(2) // unchanged: one skip, one duplicate
	s.eventually(30*time.Second, "both return events processed", func() bool {
		return s.count(`SELECT count(*) FROM processed_events`) == 11
	})
	if n := s.count(`SELECT count(*) FROM listings WHERE catalog_item_id = $1 AND owner_ref = $2`, w.mug, seller); n != 0 {
		t.Fatalf("the skipped line created %d listings, want none", n)
	}

	// Poison (no nodeId) goes to the dead-letter topic and does not wedge the partition.
	kafkatest.Produce(t, inboundkafka.CustomerReturnsTopic, returnEvent("evt-ret-poison", "returnauthorization.ReturnCompleted",
		`{"authorizationId":"ret-e2e-3","outcome":"RECEIVED","lines":[]}`))
	dlq := kafkatest.ReadN(t, inboundkafka.CustomerReturnsTopic+".dlq", 1, 90*time.Second)
	if dlq[0].Headers["x-dlq-source-topic"] != inboundkafka.CustomerReturnsTopic || !strings.Contains(dlq[0].Headers["x-dlq-error"], "nodeId") ||
		!strings.Contains(string(dlq[0].Value), "evt-ret-poison") {
		t.Fatalf("dead letter = %+v", dlq[0])
	}
}
