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
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// CustomerReturnsTopic is the customer-returns integration topic (its contract
// is restated in apis/asyncapi.yaml, never imported; ADR 0015).
const CustomerReturnsTopic = "retailnetwork.customer-returns.events"

// typeReturnCompleted is the one consumed customer-returns CloudEvents type
// (the full `type` string, ADR 0015).
const typeReturnCompleted = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnCompleted"

// returnCompletionUseCase is what the consumer calls;
// *usecases.ApplyReturnCompletion satisfies it.
type returnCompletionUseCase interface {
	Execute(ctx context.Context, ev usecases.ReturnCompletionEvent) (usecases.ReturnCompletionResult, error)
}

// ReturnsConsumer reads CustomerReturnsTopic under a stable consumer group and
// applies ReturnCompleted, dispatching on the FULL CloudEvents `type`. Every
// other type on the topic is acknowledged and counted (ADR 0015).
type ReturnsConsumer struct {
	Reader  Reader
	Returns returnCompletionUseCase
	Logger  *slog.Logger
	Retry   RetryPolicy
	// Ignored counts the types of the topic the service does not consume.
	Ignored IgnoredEvents
	// DLQ is the dead-letter writer (topic CustomerReturnsTopic + DLQSuffix),
	// with the same bound as the other consumers'.
	DLQ DeadLetterWriter

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewReturnsConsumer builds the consumer reading CustomerReturnsTopic from
// brokers under groupID (from an environment variable at the composition
// root, never a literal). Constructing the reader and the writer does not
// dial.
func NewReturnsConsumer(brokers []string, groupID string, uc returnCompletionUseCase, ignored IgnoredEvents, logger *slog.Logger) *ReturnsConsumer {
	return &ReturnsConsumer{
		Reader:  kafkago.NewReader(readerConfig(brokers, CustomerReturnsTopic, groupID)),
		Returns: uc,
		Logger:  defaultLogger(logger),
		Ignored: ignored,
		DLQ:     newDomainDLQWriter(brokers),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset
// is committed only after HandleMessage returned nil.
func (c *ReturnsConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: func(ctx context.Context, msg kafkago.Message) error {
			return traceHandling(ctx, msg, CustomerReturnsTopic, c.HandleMessage)
		},
		logger: defaultLogger(c.Logger),
		name:   "customer-returns consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	if c.DLQ != nil {
		loop.maxAttempts = domainMaxHandlerAttempts
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return publishDeadLetter(ctx, c.DLQ, CustomerReturnsTopic+DLQSuffix, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *ReturnsConsumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage decodes one CloudEvents 1.0 message and applies it, with the
// same contract as SellerConsumer.HandleMessage: nil for everything that is
// not ours (a type of the topic this service does not consume is counted), an
// error wrapping errPoison for an event of a consumed type that can never be
// applied, and any other non-nil error ONLY for a transient failure.
func (c *ReturnsConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", CustomerReturnsTopic, "error", err)
		return nil
	}
	if e.Type() != typeReturnCompleted {
		if c.Ignored != nil {
			c.Ignored.Ignored(ctx, e.Type())
		}
		logger.DebugContext(ctx, "ignoring a type this service does not consume", "topic", CustomerReturnsTopic, "type", e.Type(), "event_id", e.ID())
		return nil
	}
	ev, err := returnCompletedFrom(e)
	if err == nil {
		var res usecases.ReturnCompletionResult
		res, err = c.Returns.Execute(ctx, ev)
		if err == nil {
			logReturnCompletion(ctx, logger, ev, res)
			return nil
		}
		if !errors.Is(err, shared.ErrValidation) {
			return err
		}
	}
	logger.WarnContext(ctx, "an event can never be applied; it goes to the dead-letter topic", "type", e.Type(), "event_id", e.ID(), "error", err)
	return fmt.Errorf("%w: %w", errPoison, err)
}

// returnData is the tolerant copy of the ReturnCompleted payload
// (ADR 0015): only the fields read are declared. `outcome` is not read (the
// type is published for one outcome only). Lines is a pointer so an absent
// list is told from an empty one.
type returnData struct {
	AuthorizationID string        `json:"authorizationId"`
	NodeID          string        `json:"nodeId"`
	ReceivedAt      time.Time     `json:"receivedAt"`
	Lines           *[]returnLine `json:"lines"`
}

type returnLine struct {
	CatalogItemID  string `json:"catalogItemId"`
	OwnerRef       string `json:"ownerRef"`
	QtyRestockable int    `json:"qtyRestockable"`
	QtyUnsellable  int    `json:"qtyUnsellable"`
}

// returnCompletedFrom maps a ReturnCompleted CloudEvent to the use-case
// input. An undecodable payload, or one without a usable authorizationId,
// nodeId or instant, is errBadPayload (poison). The restock instant is
// `receivedAt`, or the CloudEvents `time` when the payload carries none.
func returnCompletedFrom(e ce.Event) (usecases.ReturnCompletionEvent, error) {
	var d returnData
	if err := e.DataAs(&d); err != nil {
		return usecases.ReturnCompletionEvent{}, fmt.Errorf("%w: %w", errBadPayload, err)
	}
	at := d.ReceivedAt
	if at.IsZero() {
		at = e.Time()
	}
	if d.AuthorizationID == "" || d.NodeID == "" || at.IsZero() {
		return usecases.ReturnCompletionEvent{}, fmt.Errorf("%w: authorizationId, nodeId and an instant are required", errBadPayload)
	}
	ev := usecases.ReturnCompletionEvent{
		EventID: e.ID(), EventType: e.Type(), AuthorizationID: d.AuthorizationID,
		NodeID: listing.NodeID(d.NodeID), ReceivedAt: at.UTC(), LinesPresent: d.Lines != nil,
	}
	if d.Lines != nil {
		ev.Lines = make([]usecases.ReturnLine, 0, len(*d.Lines))
		for _, l := range *d.Lines {
			ev.Lines = append(ev.Lines, usecases.ReturnLine{
				CatalogItemID: catalog.ID(l.CatalogItemID), OwnerRef: listing.OwnerRef(l.OwnerRef),
				QtyRestockable: l.QtyRestockable, QtyUnsellable: l.QtyUnsellable,
			})
		}
	}
	return ev, nil
}

func logReturnCompletion(ctx context.Context, logger *slog.Logger, ev usecases.ReturnCompletionEvent, res usecases.ReturnCompletionResult) {
	attrs := []any{"type", ev.EventType, "event_id", ev.EventID, "authorization_id", ev.AuthorizationID, "node_id", string(ev.NodeID)}
	if res.Duplicate {
		logger.DebugContext(ctx, "event already processed", attrs...)
		return
	}
	if res.LinesAbsent {
		logger.InfoContext(ctx, "a return completion without lines restocked nothing", attrs...)
		return
	}
	for _, s := range res.Skipped {
		logger.WarnContext(ctx, "a return line restocked nothing; skipped",
			append(attrs, "catalog_item_id", string(s.CatalogItemID), "owner_ref", string(s.OwnerRef), "reason", s.Reason)...)
	}
	logger.InfoContext(ctx, "event processed", append(attrs, "listings_restocked", len(res.Restocked), "lines_skipped", len(res.Skipped))...)
}
