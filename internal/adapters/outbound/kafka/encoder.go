package kafka

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/offer-availability/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/offer-availability/internal/application/outbox"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// Topic is this service's integration-event topic. The model declares no
// analytics-only event for this context, so there is no analytics topic.
const Topic = "retailnetwork.offer-availability.events"

// schemaVersion is the dataschema version of every payload below
// (urn:retailnetwork:offer-availability:events:<EventName>:v1).
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, camelCase JSON). They are exactly the ones
// pinned in apis/asyncapi.yaml and ADR 0004; optional fields are omitted when
// unset (none of the published fields is optional) and timestamps are RFC 3339 UTC.

type catalogItemCreatedData struct {
	CatalogItemID string `json:"catalogItemId"`
	Title         string `json:"title"`
	LengthMm      int    `json:"lengthMm"`
	WidthMm       int    `json:"widthMm"`
	HeightMm      int    `json:"heightMm"`
	WeightG       int    `json:"weightG"`
	Hazmat        bool   `json:"hazmat"`
	Fragile       bool   `json:"fragile"`
}

type catalogItemDiscontinuedData struct {
	CatalogItemID string `json:"catalogItemId"`
}

type listingCreatedData struct {
	ListingID     string `json:"listingId"`
	CatalogItemID string `json:"catalogItemId"`
	NodeID        string `json:"nodeId"`
}

type listingAvailabilityChangedData struct {
	ListingID     string `json:"listingId"`
	CatalogItemID string `json:"catalogItemId"`
	NodeID        string `json:"nodeId"`
	AvailableQty  int    `json:"availableQty"`
	Basis         string `json:"basis"`
	AsOf          string `json:"asOf"`
	FeedID        string `json:"feedId"`
}

type listingSuppressedData struct {
	ListingID string `json:"listingId"`
	NodeID    string `json:"nodeId"`
	Reason    string `json:"reason"`
}

type listingRestoredData struct {
	ListingID string `json:"listingId"`
	NodeID    string `json:"nodeId"`
}

type inventoryFeedAppliedData struct {
	FeedID     string `json:"feedId"`
	NodeID     string `json:"nodeId"`
	FullUpdate bool   `json:"fullUpdate"`
	LineCount  int    `json:"lineCount"`
	AppliedAt  string `json:"appliedAt"`
}

// Encoder implements ports.EventEncoder: each domain event becomes one
// outbox.Message holding the CloudEvents bytes, the Kafka key (the aggregate
// root id) and the content-type header. The CloudEvents `id` is minted HERE,
// once, and persisted with the outbox row, so a relay retry republishes the
// same id.
type Encoder struct {
	// NewID mints a CloudEvents id; uuid.NewString when nil.
	NewID func() string
}

// NewEncoder returns an Encoder minting random UUID v4 ids.
func NewEncoder() *Encoder { return &Encoder{NewID: uuid.NewString} }

var _ ports.EventEncoder = (*Encoder)(nil)

// Encode encodes events in order. An event type it does not publish is a
// programming error and fails the whole call.
func (e *Encoder) Encode(events ...shared.Event) ([]outbox.Message, error) {
	newID := e.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	out := make([]outbox.Message, 0, len(events))
	for _, ev := range events {
		data, err := payloadFor(ev)
		if err != nil {
			return nil, err
		}
		msg, err := buildMessage(ev, newID(), cloudevents.StreamEvents, data)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// buildMessage wraps data in the CloudEvents envelope of ev (on the given
// stream) and returns the outbox row for the events topic.
func buildMessage(ev shared.Event, id, stream string, data any) (outbox.Message, error) {
	value, err := cloudevents.New(cloudevents.Spec{
		ID: id, Entity: ev.Entity(), EventName: ev.EventName(), Subject: ev.SubjectID(),
		Time: ev.OccurredAt(), Stream: stream, Version: schemaVersion, Data: data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s: %w", ev.EventName(), err)
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    id,
		Topic:      Topic,
		EventType:  cloudevents.Type(ev.Entity(), ev.EventName()),
		Subject:    ev.SubjectID(),
		Key:        []byte(ev.SubjectID()),
		DataSchema: cloudevents.DataSchema(stream, ev.EventName(), schemaVersion),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func payloadFor(ev shared.Event) (any, error) {
	switch e := ev.(type) {
	case catalog.CatalogItemCreated:
		return catalogItemCreatedData{
			CatalogItemID: string(e.CatalogItemID), Title: e.Title, LengthMm: e.LengthMm, WidthMm: e.WidthMm,
			HeightMm: e.HeightMm, WeightG: e.WeightG, Hazmat: e.Hazmat, Fragile: e.Fragile,
		}, nil
	case catalog.CatalogItemDiscontinued:
		return catalogItemDiscontinuedData{CatalogItemID: string(e.CatalogItemID)}, nil
	case listing.ListingCreated:
		return listingCreatedData{ListingID: string(e.ListingID), CatalogItemID: string(e.CatalogItemID), NodeID: string(e.NodeID)}, nil
	case listing.ListingAvailabilityChanged:
		return listingAvailabilityChangedData{
			ListingID: string(e.ListingID), CatalogItemID: string(e.CatalogItemID), NodeID: string(e.NodeID),
			AvailableQty: e.AvailableQty, Basis: string(e.Basis), AsOf: ts(e.AsOf), FeedID: e.FeedID,
		}, nil
	case listing.ListingSuppressed:
		return listingSuppressedData{ListingID: string(e.ListingID), NodeID: string(e.NodeID), Reason: string(e.Reason)}, nil
	case listing.ListingRestored:
		return listingRestoredData{ListingID: string(e.ListingID), NodeID: string(e.NodeID)}, nil
	case feed.InventoryFeedApplied:
		return inventoryFeedAppliedData{
			FeedID: string(e.FeedID), NodeID: string(e.NodeID), FullUpdate: e.FullUpdate, LineCount: e.LineCount, AppliedAt: ts(e.AppliedAt),
		}, nil
	default:
		return nil, fmt.Errorf("kafka encoder: %s is not a published event", ev.EventName())
	}
}
