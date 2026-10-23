package kafka

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

var (
	at    = time.Date(2026, 10, 23, 14, 5, 12, 0, time.UTC)
	asOf  = time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	fixed = "5b0f8a52-3c1e-4d7a-9f4e-2a6d1c9e7b31"
)

// goldenCases is one domain event per published type, with the subject the
// CloudEvents id/subject/key must carry.
func goldenCases() []struct {
	ev      shared.Event
	subject string
} {
	hdr := shared.Header{Version: 3, At: at}
	return []struct {
		ev      shared.Event
		subject string
	}{
		{catalog.CatalogItemCreated{Header: hdr, CatalogItemID: "item-004512", Title: "Stainless steel water bottle 750 ml", LengthMm: 270, WidthMm: 75, HeightMm: 75, WeightG: 340, Hazmat: false, Fragile: true}, "item-004512"},
		{catalog.CatalogItemDiscontinued{Header: hdr, CatalogItemID: "item-004512"}, "item-004512"},
		{listing.ListingCreated{Header: hdr, ListingID: "lst-000184", CatalogItemID: "item-004512", NodeID: "node-0f3a91"}, "lst-000184"},
		{listing.ListingAvailabilityChanged{Header: hdr, ListingID: "lst-000184", CatalogItemID: "item-004512", NodeID: "node-0f3a91", AvailableQty: 120, Basis: listing.BasisThroughputConstrained, AsOf: asOf, FeedID: "feed-20261023-0007"}, "lst-000184"},
		{listing.ListingSuppressed{Header: hdr, ListingID: "lst-000184", NodeID: "node-0f3a91", Reason: listing.ReasonNodeNotSellable}, "lst-000184"},
		{listing.ListingRestored{Header: hdr, ListingID: "lst-000184", NodeID: "node-0f3a91"}, "lst-000184"},
		{feed.InventoryFeedApplied{Header: hdr, FeedID: "feed-20261023-0007", NodeID: "node-0f3a91", FullUpdate: true, LineCount: 412, AppliedAt: at}, "feed-20261023-0007"},
	}
}

func encodeOne(t *testing.T, ev shared.Event) (value []byte, topic, key, typ, schema string, headers map[string]string) {
	t.Helper()
	msgs, err := (&Encoder{NewID: func() string { return fixed }}).Encode(ev)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Encode(%s): %v %d", ev.EventName(), err, len(msgs))
	}
	m := msgs[0]
	headers = map[string]string{}
	for _, h := range m.Headers {
		headers[h.Key] = h.Value
	}
	if m.EventID != fixed {
		t.Fatalf("event id = %q", m.EventID)
	}
	return m.Value, m.Topic, string(m.Key), m.EventType, m.DataSchema, headers
}

// TestEncodedEventsMatchTheGoldenJSON pins the exact bytes of every published
// event (set UPDATE_GOLDEN=1 to rewrite the files after an intended change).
func TestEncodedEventsMatchTheGoldenJSON(t *testing.T) {
	for _, c := range goldenCases() {
		value, _, _, _, _, _ := encodeOne(t, c.ev)
		path := filepath.Join("testdata", c.ev.EventName()+".golden.json")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, append(value, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", c.ev.EventName(), err)
		}
		if got := string(value) + "\n"; got != string(want) {
			t.Errorf("%s:\n got  %s\n want %s", c.ev.EventName(), got, want)
		}
	}
}

func TestEncodedMessageRoutingFields(t *testing.T) {
	for _, c := range goldenCases() {
		_, topic, key, typ, schema, headers := encodeOne(t, c.ev)
		name := c.ev.EventName()
		if topic != "retailnetwork.offer-availability.events" || key != c.subject {
			t.Errorf("%s: topic %q key %q", name, topic, key)
		}
		if want := "com.retailnetwork.commerce.offer-availability." + c.ev.Entity() + "." + name; typ != want {
			t.Errorf("%s: type %q, want %q", name, typ, want)
		}
		if want := "urn:retailnetwork:offer-availability:events:" + name + ":v1"; schema != want {
			t.Errorf("%s: dataschema %q, want %q", name, schema, want)
		}
		if headers["content-type"] != "application/cloudevents+json; charset=UTF-8" || len(headers) != 1 {
			t.Errorf("%s: headers %v", name, headers)
		}
	}
}

func TestEncodedEnvelopeAttributes(t *testing.T) {
	for _, c := range goldenCases() {
		value, _, _, _, _, _ := encodeOne(t, c.ev)
		var env map[string]any
		if err := json.Unmarshal(value, &env); err != nil {
			t.Fatal(err)
		}
		for attr, want := range map[string]string{
			"specversion": "1.0", "id": fixed, "source": "/retailnetwork/offer-availability", "subject": c.subject,
			"time": "2026-10-23T14:05:12Z", "datacontenttype": "application/json",
		} {
			if env[attr] != want {
				t.Errorf("%s: %s = %v, want %s", c.ev.EventName(), attr, env[attr], want)
			}
		}
	}
}

func TestEncodeMintsDistinctIdsAndKeepsOrder(t *testing.T) {
	evs := []shared.Event{goldenCases()[0].ev, goldenCases()[2].ev, goldenCases()[6].ev}
	msgs, err := NewEncoder().Encode(evs...)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("Encode: %v %d", err, len(msgs))
	}
	seen := map[string]bool{}
	for i, m := range msgs {
		if seen[m.EventID] || m.Subject != evs[i].SubjectID() {
			t.Fatalf("message %d: id %q subject %q", i, m.EventID, m.Subject)
		}
		seen[m.EventID] = true
	}
	zero := &Encoder{}
	if out, err := zero.Encode(evs[0]); err != nil || len(out[0].EventID) != 36 {
		t.Fatalf("an Encoder without NewID mints UUIDs: %v %v", out, err)
	}
	if out, err := NewEncoder().Encode(); err != nil || len(out) != 0 {
		t.Fatalf("no events, no messages: %v %v", out, err)
	}
}

type foreignEvent struct{ shared.Header }

func (foreignEvent) EventName() string { return "Foreign" }
func (foreignEvent) Entity() string    { return "listing" }
func (foreignEvent) SubjectID() string { return "x" }

func TestEncodeRefusesAnUnpublishedEvent(t *testing.T) {
	if _, err := NewEncoder().Encode(foreignEvent{}); err == nil {
		t.Fatal("an event the service does not publish must fail")
	}
}

func TestEncodeRefusesAnEventWithoutSubject(t *testing.T) {
	if _, err := NewEncoder().Encode(listing.ListingRestored{Header: shared.Header{Version: 1, At: at}}); err == nil {
		t.Fatal("an empty subject must fail")
	}
}
