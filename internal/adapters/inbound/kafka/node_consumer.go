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
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// NodeTopic is the node-topology integration topic (its contract is restated in
// apis/asyncapi.yaml, never imported).
const NodeTopic = "retailnetwork.node-topology.events"

// The consumed CloudEvents types (full `type` strings).
const (
	typeNodeRegistered = "com.retailnetwork.logistics.node-topology.node.NodeRegistered"
	typeNodeActivated  = "com.retailnetwork.logistics.node-topology.node.NodeActivated"
	typeNodeSuspended  = "com.retailnetwork.logistics.node-topology.node.NodeSuspended"
	typeNodeRetired    = "com.retailnetwork.logistics.node-topology.node.NodeRetired"
)

// errBadPayload marks a CloudEvent whose `data` cannot be decoded: a
// deterministic problem, never retried.
var errBadPayload = errors.New("malformed event payload")

// errPoison marks a message the consumer can never apply (malformed payload,
// missing required field): the run loop dead-letters it at once, without
// retrying, and commits past it.
var errPoison = errors.New("poison message")

// nodeEventUseCase is what the consumer calls; *usecases.ApplyNodeEvent
// satisfies it.
type nodeEventUseCase interface {
	Execute(ctx context.Context, ev usecases.NodeEvent) error
}

// nodeData is the tolerant payload copy: only the fields read are declared,
// every other field of the producer's payload is ignored.
type nodeData struct {
	NodeID      string    `json:"nodeId"`
	ActivatedAt time.Time `json:"activatedAt"`
	SuspendedAt time.Time `json:"suspendedAt"`
	RetiredAt   time.Time `json:"retiredAt"`
}

// NodeConsumer reads NodeTopic under a stable consumer group and applies each
// node lifecycle event, dispatching on the FULL CloudEvents `type`. Types it
// does not consume are ignored.
type NodeConsumer struct {
	Reader  Reader
	UseCase nodeEventUseCase
	Logger  *slog.Logger
	Retry   RetryPolicy
	// DLQ is the dead-letter writer (topic NodeTopic + DLQSuffix): a transient
	// failure is retried domainMaxHandlerAttempts times before the message is
	// published there instead of blocking the partition forever; an event that
	// can never be applied goes there at once.
	DLQ DeadLetterWriter

	sleep sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewNodeConsumer builds the consumer reading NodeTopic from brokers under
// groupID (from an environment variable at the composition root, never a
// literal). Constructing the reader and the writer does not dial.
func NewNodeConsumer(brokers []string, groupID string, uc nodeEventUseCase, logger *slog.Logger) *NodeConsumer {
	return &NodeConsumer{
		Reader:  kafkago.NewReader(readerConfig(brokers, NodeTopic, groupID)),
		UseCase: uc,
		Logger:  defaultLogger(logger),
		DLQ:     newDomainDLQWriter(brokers),
	}
}

// Run consumes until ctx is cancelled or the reader fails. A message's offset is
// committed only after HandleMessage returned nil.
func (c *NodeConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: c.handleTraced,
		logger: defaultLogger(c.Logger),
		name:   "node-topology consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	if c.DLQ != nil {
		loop.maxAttempts = domainMaxHandlerAttempts
		loop.deadLetter = func(ctx context.Context, msg kafkago.Message, cause error) error {
			return publishDeadLetter(ctx, c.DLQ, NodeTopic+DLQSuffix, msg, cause)
		}
	}
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *NodeConsumer) Close() error {
	err := c.Reader.Close()
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage decodes one CloudEvents 1.0 message and applies it. It returns
// nil for everything that is not ours (logged at WARN when it is not a valid
// CloudEvent; silently for a type this consumer does not act on), an error
// wrapping errPoison for an event of a handled type that can never be applied,
// and any other non-nil error ONLY for a transient failure, after the unit of
// work rolled back.
func (c *NodeConsumer) HandleMessage(ctx context.Context, value []byte) error {
	logger := defaultLogger(c.Logger)
	e, err := cloudevents.Decode(value)
	if err != nil {
		logger.WarnContext(ctx, "skipping a message that is not a valid CloudEvent", "topic", NodeTopic, "error", err)
		return nil
	}
	ev, handled, err := nodeEventFrom(e)
	if err != nil {
		logger.WarnContext(ctx, "an event can never be applied; it goes to the dead-letter topic", "type", e.Type(), "event_id", e.ID(), "error", err)
		return fmt.Errorf("%w: %w", errPoison, err)
	}
	if !handled {
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

// nodeEventFrom maps a CloudEvent to the use-case input. handled is false for a
// type the service does not consume.
func nodeEventFrom(e ce.Event) (usecases.NodeEvent, bool, error) {
	status, ok := map[string]nodestatus.Status{
		typeNodeRegistered: nodestatus.StatusProposed,
		typeNodeActivated:  nodestatus.StatusActive,
		typeNodeSuspended:  nodestatus.StatusSuspended,
		typeNodeRetired:    nodestatus.StatusRetired,
	}[e.Type()]
	if !ok {
		return usecases.NodeEvent{}, false, nil
	}
	var d nodeData
	if err := e.DataAs(&d); err != nil {
		return usecases.NodeEvent{}, true, fmt.Errorf("%w: %v", errBadPayload, err)
	}
	at := e.Time()
	switch e.Type() {
	case typeNodeActivated:
		at = d.ActivatedAt
	case typeNodeSuspended:
		at = d.SuspendedAt
	case typeNodeRetired:
		at = d.RetiredAt
	}
	if d.NodeID == "" || at.IsZero() {
		return usecases.NodeEvent{}, true, fmt.Errorf("%w: nodeId and the event instant are required", errBadPayload)
	}
	return usecases.NodeEvent{
		EventID: e.ID(), EventType: e.Type(), NodeID: nodestatus.ID(d.NodeID), Status: status, At: at.UTC(),
	}, true, nil
}
