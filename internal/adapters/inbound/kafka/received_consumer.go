package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// receivedData is the tolerant copy of the InboundShipmentReceived payload
// (seller-inbound ADR 0014): only the fields read are declared. `status`,
// `cartons` and `receivedAt` are not read. Received is a pointer so an absent
// list (an event published before the producer carried it) is told from an
// empty one.
type receivedData struct {
	ShipmentID string          `json:"shipmentId"`
	SellerID   string          `json:"sellerId"`
	NodeID     string          `json:"nodeId"`
	Received   *[]receivedLine `json:"received"`
}

type receivedLine struct {
	CatalogItemID string `json:"catalogItemId"`
	GTIN          string `json:"gtin"`
	QtyEaches     int    `json:"qtyEaches"`
}

// receivedEventFrom maps an InboundShipmentReceived CloudEvent to the use-case
// input. An undecodable payload, or one without sellerId or nodeId, is
// errBadPayload (poison).
func receivedEventFrom(e ce.Event) (usecases.ReceivedStockEvent, error) {
	var d receivedData
	if err := e.DataAs(&d); err != nil {
		return usecases.ReceivedStockEvent{}, fmt.Errorf("%w: %w", errBadPayload, err)
	}
	if d.SellerID == "" || d.NodeID == "" {
		return usecases.ReceivedStockEvent{}, fmt.Errorf("%w: sellerId and nodeId are required", errBadPayload)
	}
	ev := usecases.ReceivedStockEvent{
		EventID: e.ID(), EventType: e.Type(), ShipmentID: d.ShipmentID,
		SellerID: listing.OwnerRef(d.SellerID), NodeID: listing.NodeID(d.NodeID), ReceivedPresent: d.Received != nil,
	}
	if d.Received != nil {
		ev.Received = make([]usecases.ReceivedLine, 0, len(*d.Received))
		for _, l := range *d.Received {
			ev.Received = append(ev.Received, usecases.ReceivedLine{CatalogItemID: catalog.ID(l.CatalogItemID), GTIN: l.GTIN, QtyEaches: l.QtyEaches})
		}
	}
	return ev, nil
}

// handleReceived applies one InboundShipmentReceived (ADR 0014) with the
// contract of HandleMessage: poison for a payload that can never be applied,
// any other error only when transient. Each skipped line is a WARN; an event
// without `received` is logged at INFO (it is counted by the use case).
func (c *SellerConsumer) handleReceived(ctx context.Context, logger *slog.Logger, e ce.Event) error {
	ev, err := receivedEventFrom(e)
	if err == nil {
		var res usecases.ReceivedStockResult
		res, err = c.Received.Execute(ctx, ev)
		if err == nil {
			logReceived(ctx, logger, ev, res)
			return nil
		}
		if !errors.Is(err, shared.ErrValidation) {
			return err
		}
	}
	logger.WarnContext(ctx, "an event can never be applied; it goes to the dead-letter topic", "type", e.Type(), "event_id", e.ID(), "error", err)
	return fmt.Errorf("%w: %w", errPoison, err)
}

func logReceived(ctx context.Context, logger *slog.Logger, ev usecases.ReceivedStockEvent, res usecases.ReceivedStockResult) {
	attrs := []any{"type", ev.EventType, "event_id", ev.EventID, "shipment_id", ev.ShipmentID, "seller_id", string(ev.SellerID), "node_id", string(ev.NodeID)}
	if res.Duplicate {
		logger.DebugContext(ctx, "event already processed", attrs...)
		return
	}
	if res.Unknown {
		logger.InfoContext(ctx, "received stock is unknown (the event carries no received list); no listing created", attrs...)
		return
	}
	for _, s := range res.Skipped {
		logger.WarnContext(ctx, "a received line resolves to no catalogue item; skipped",
			append(attrs, "gtin", s.GTIN, "catalog_item_id", string(s.CatalogItemID), "reason", s.Reason)...)
	}
	logger.InfoContext(ctx, "event processed", append(attrs, "listings_created", len(res.Created), "listings_existing", res.Existing, "lines_skipped", len(res.Skipped))...)
}
