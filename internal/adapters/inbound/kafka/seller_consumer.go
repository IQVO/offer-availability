package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/offer-availability/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// SellerTopic is the seller-inbound integration topic (its contract is restated
// in apis/asyncapi.yaml, never imported; ADR 0013).
const SellerTopic = "retailnetwork.seller-inbound.events"

// The consumed seller-inbound CloudEvents types (full `type` strings).
const (
	typeSellerSuspended         = "com.retailnetwork.partner.seller-inbound.seller.SellerSuspended"
	typeSellerReinstated        = "com.retailnetwork.partner.seller-inbound.seller.SellerReinstated"
	typeInboundShipmentReceived = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived"
)

// sellerEventUseCase is what the consumer calls; *usecases.ApplySellerEvent
// satisfies it.
type sellerEventUseCase interface {
	Execute(ctx context.Context, ev usecases.SellerEvent) error
}

// receivedStockUseCase is what the consumer calls for InboundShipmentReceived;
// *usecases.ApplyReceivedStock satisfies it (ADR 0014).
type receivedStockUseCase interface {
	Execute(ctx context.Context, ev usecases.ReceivedStockEvent) (usecases.ReceivedStockResult, error)
}

// IgnoredEvents counts the CloudEvents of SellerTopic whose type this service
// does not act on (ADR 0013, rule 1: counted, not an error). The implementation
// lives in internal/adapters/outbound/telemetry; nil counts nothing.
type IgnoredEvents interface {
	Ignored(ctx context.Context, eventType string)
}

// sellerData is the tolerant payload copy: only the fields read are declared
// (the optional `reason` of SellerSuspended is not stored).
type sellerData struct {
	SellerID     string    `json:"sellerId"`
	SuspendedAt  time.Time `json:"suspendedAt"`
	ReinstatedAt time.Time `json:"reinstatedAt"`
}

// SellerConsumer reads SellerTopic under a stable consumer group and applies
// SellerSuspended, SellerReinstated and InboundShipmentReceived, dispatching on
// the FULL CloudEvents `type`. Every other type on the topic is acknowledged
// and counted.
type SellerConsumer struct {
	Reader  Reader
	UseCase sellerEventUseCase
	// Received creates the owner listings of a received shipment (ADR 0014).
	Received receivedStockUseCase
	Logger   *slog.Logger
	Retry    RetryPolicy
	// Ignored counts the types of the topic the service does not consume.
	Ignored IgnoredEvents
	// DLQ is the dead-letter writer (topic SellerTopic + DLQSuffix), with the
	// same bound as the node consumer's.
	DLQ DeadLetterWriter

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewSellerConsumer builds the consumer reading SellerTopic from brokers under
// groupID (from an environment variable at the composition root, never a
// literal). Constructing the reader and the writer does not dial.
func NewSellerConsumer(brokers []string, groupID string, uc sellerEventUseCase, received receivedStockUseCase, ignored IgnoredEvents, logger *slog.Logger) *SellerConsumer {
	return &SellerConsumer{
		Reader:   kafkago.NewReader(readerConfig(brokers, SellerTopic, groupID)),
		UseCase:  uc,
		Received: received,
		Logger:   defaultLogger(logger),
		Ignored:  ignored,
		DLQ:      newDomainDLQWriter(brokers),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset is
// committed only after HandleMessage returned nil.
func (c *SellerConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error {
			return traceHandling(ctx, msg, SellerTopic, c.HandleMessage)
		},
		logger: defaultLogger(c.Logger),
		name:   "seller-inbound consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	if c.DLQ != nil {
		loop.maxAttempts = domainMaxHandlerAttempts
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return publishDeadLetter(ctx, c.DLQ, SellerTopic+DLQSuffix, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *SellerConsumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage decodes one CloudEvents 1.0 message and applies it, with the
// same contract as NodeConsumer.HandleMessage: nil for everything that is not
// ours (a type of the topic this service does not consume is counted), an
// error wrapping errPoison for an event of a consumed type that can never be
// applied, and any other non-nil error ONLY for a transient failure.
func (c *SellerConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", SellerTopic, "error", err)
		return nil
	}
	if e.Type() == typeInboundShipmentReceived && c.Received != nil {
		return c.handleReceived(ctx, logger, e)
	}
	ev, handled, err := sellerEventFrom(e)
	if err != nil {
		logger.WarnContext(ctx, "an event can never be applied; it goes to the dead-letter topic", "type", e.Type(), "event_id", e.ID(), "error", err)
		return fmt.Errorf("%w: %w", errPoison, err)
	}
	if !handled {
		if c.Ignored != nil {
			c.Ignored.Ignored(ctx, e.Type())
		}
		logger.DebugContext(ctx, "ignoring a type this service does not consume", "topic", SellerTopic, "type", e.Type(), "event_id", e.ID())
		return nil
	}
	err = c.UseCase.Execute(ctx, ev)
	if errors.Is(err, shared.ErrValidation) {
		logger.WarnContext(ctx, "an event can never be applied; it goes to the dead-letter topic", "type", e.Type(), "event_id", e.ID(), "error", err)
		return fmt.Errorf("%w: %w", errPoison, err)
	}
	if err == nil {
		logger.InfoContext(ctx, "event processed", "type", e.Type(), "event_id", e.ID(), "subject", e.Subject())
	}
	return err
}

// sellerEventFrom maps a CloudEvent to the use-case input. handled is false for
// a type the service does not consume.
func sellerEventFrom(e ce.Event) (usecases.SellerEvent, bool, error) {
	status, ok := map[string]sellerstatus.Status{
		typeSellerSuspended:  sellerstatus.StatusSuspended,
		typeSellerReinstated: sellerstatus.StatusActive,
	}[e.Type()]
	if !ok {
		return usecases.SellerEvent{}, false, nil
	}
	var d sellerData
	if err := e.DataAs(&d); err != nil {
		return usecases.SellerEvent{}, true, fmt.Errorf("%w: %w", errBadPayload, err)
	}
	at := d.ReinstatedAt
	if status == sellerstatus.StatusSuspended {
		at = d.SuspendedAt
	}
	if d.SellerID == "" || at.IsZero() {
		return usecases.SellerEvent{}, true, fmt.Errorf("%w: sellerId and the event instant are required", errBadPayload)
	}
	return usecases.SellerEvent{
		EventID: e.ID(), EventType: e.Type(), SellerID: sellerstatus.ID(d.SellerID), Status: status, At: at.UTC(),
	}, true, nil
}
