//go:build integration

package main

// The seller-inbound part of the real-wiring test (ADR 0013): run by
// TestRealWiring after the node flow, against the same running binary, broker
// and database.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	inboundkafka "github.com/claudioed/offer-availability/internal/adapters/inbound/kafka"
	"github.com/claudioed/offer-availability/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
)

const (
	sellerPrefix = "com.retailnetwork.partner.seller-inbound."
	seller       = "seller-77"
)

// sellerEvent is a seller-inbound CloudEvent as that producer writes it.
func sellerEvent(id, typ, data string) kafkatest.Message {
	value := fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/retailnetwork/seller-inbound","type":%q,`+
		`"subject":%q,"datacontenttype":"application/json","time":"2026-10-23T15:00:00Z","data":%s}`, id, sellerPrefix+typ, seller, data)
	return kafkatest.Message{Key: seller, Value: []byte(value), Headers: map[string]string{"content-type": cloudevents.MediaType}}
}

// listingState reads a listing's "STATE/REASON" over REST.
func listingState(t *testing.T, s *service, id string) string {
	t.Helper()
	for _, l := range decode(t, s.get("/listings?nodeId=node-a"))["items"].([]any) {
		if m := l.(map[string]any); str(m, "listingId") == id {
			return str(m, "state") + "/" + str(m, "suppressionReason")
		}
	}
	return "missing"
}

func sellerFlow(t *testing.T, s *service, w *world) {
	t.Helper()
	waitState := func(id, want string) {
		s.eventually(60*time.Second, id+" "+want, func() bool { return listingState(t, s, id) == want })
	}
	tote := str(s.write(http.MethodPost, "/catalog-items", `{"title":"Canvas tote","lengthMm":400,"widthMm":50,"heightMm":350,"weightG":200}`, http.StatusCreated), "catalogItemId")
	l := s.write(http.MethodPost, "/listings", `{"catalogItemId":"`+tote+`","nodeId":"node-a","ownerRef":"`+seller+`"}`, http.StatusCreated)
	w.lstToteSeller = str(l, "listingId")
	if str(l, "state") != "LIVE" {
		t.Fatalf("a seller listing on an ACTIVE node: %v", l)
	}

	kafkatest.Produce(t, inboundkafka.SellerTopic, sellerEvent("evt-s1", "seller.SellerSuspended",
		`{"sellerId":"`+seller+`","suspendedAt":"2026-10-23T15:00:00Z","reason":"repeated label violations"}`))
	waitState(w.lstToteSeller, "SUPPRESSED/OWNER_SUSPENDED")

	// CreateListing for a SUSPENDED owner starts SUPPRESSED(OWNER_SUSPENDED) (rule 7).
	m := s.write(http.MethodPost, "/listings", `{"catalogItemId":"`+w.mug+`","nodeId":"node-a","ownerRef":"`+seller+`"}`, http.StatusCreated)
	w.lstMugSeller = str(m, "listingId")
	if str(m, "state") != "SUPPRESSED" || str(m, "suppressionReason") != "OWNER_SUSPENDED" {
		t.Fatalf("a listing of a suspended owner: %v", m)
	}

	// A duplicate, an older reinstatement, a type this service does not consume and a poison event.
	kafkatest.Produce(t, inboundkafka.SellerTopic,
		sellerEvent("evt-s1", "seller.SellerSuspended", `{"sellerId":"`+seller+`","suspendedAt":"2026-10-23T15:00:00Z"}`),
		sellerEvent("evt-s-old", "seller.SellerReinstated", `{"sellerId":"`+seller+`","reinstatedAt":"2026-10-23T14:30:00Z"}`),
		sellerEvent("evt-recv", "inboundshipment.InboundShipmentReceived",
			`{"shipmentId":"shp-1","sellerId":"`+seller+`","nodeId":"node-a","status":"CLOSED","cartons":[{"sscc":"006141410000000012","status":"RECEIVED"}],"receivedAt":"2026-10-23T15:10:00Z"}`),
		sellerEvent("evt-s-poison", "seller.SellerSuspended", `{"suspendedAt":"2026-10-23T15:20:00Z"}`),
	)
	dlq := kafkatest.ReadN(t, inboundkafka.SellerTopic+".dlq", 1, 90*time.Second)
	if dlq[0].Headers["x-dlq-source-topic"] != inboundkafka.SellerTopic || !strings.Contains(dlq[0].Headers["x-dlq-error"], "sellerId") ||
		!strings.Contains(string(dlq[0].Value), "evt-s-poison") {
		t.Fatalf("dead letter = %+v", dlq[0])
	}
	if got := listingState(t, s, w.lstToteSeller); got != "SUPPRESSED/OWNER_SUSPENDED" {
		t.Fatalf("an older reinstatement restored the listing: %s", got)
	}

	kafkatest.Produce(t, inboundkafka.SellerTopic, sellerEvent("evt-s2", "seller.SellerReinstated",
		`{"sellerId":"`+seller+`","reinstatedAt":"2026-10-23T16:00:00Z"}`))
	waitState(w.lstToteSeller, "LIVE/")
	waitState(w.lstMugSeller, "LIVE/")
	if got := s.count(`SELECT count(*) FROM seller_status_view WHERE seller_id = $1 AND status = 'ACTIVE' AND version = 2`, seller); got != 1 {
		t.Fatalf("seller_status_view rows for %s ACTIVE v2 = %d", seller, got)
	}
	// Node events 4 + seller events 3 (the duplicate, the ignored type and the poison are not in the inbox).
	s.eventually(30*time.Second, "all consumed events processed", func() bool {
		return s.count(`SELECT count(*) FROM processed_events`) == 7
	})
}

// assertSellerData checks the payloads the seller flow published (indexes 15..21
// of expectedTypes).
func assertSellerData(t *testing.T, w world, data func(int) map[string]any) {
	t.Helper()
	if d := data(16); str(d, "listingId") != w.lstToteSeller || str(d, "ownerRef") != seller {
		t.Errorf("seller ListingCreated data = %v", d)
	}
	if d := data(17); str(d, "listingId") != w.lstToteSeller || str(d, "reason") != "OWNER_SUSPENDED" {
		t.Errorf("seller ListingSuppressed data = %v", d)
	}
	if d := data(19); str(d, "listingId") != w.lstMugSeller || str(d, "reason") != "OWNER_SUSPENDED" {
		t.Errorf("ListingSuppressed of a listing created for a suspended owner = %v", d)
	}
}
