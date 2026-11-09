//go:build integration

package main

// The end-state assertions of TestRealWiring: the Postgres rows and the exact
// CloudEvents the whole flow published.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/offer-availability/internal/testing/kafkatest"
	"github.com/claudioed/offer-availability/internal/testing/pgtest"
)

func assertRows(t *testing.T, s *service) {
	t.Helper()
	for table, want := range map[string]int{
		"catalog_items": 5, "listings": 6, "inventory_feeds": 3, "node_status_view": 1, "seller_status_view": 1, "processed_events": 9, "node_feed_marks": 1,
	} {
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
	// sellerFlow (ADR 0013).
	"catalogitem.CatalogItemCreated", "listing.ListingCreated", "listing.ListingSuppressed",
	"listing.ListingCreated", "listing.ListingSuppressed",
	"listing.ListingRestored", "listing.ListingRestored",
	// InboundShipmentReceived (ADR 0014): the item created over REST, then its owner listing.
	"catalogitem.CatalogItemCreated", "listing.ListingCreated",
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
	assertSellerData(t, w, data)
}
