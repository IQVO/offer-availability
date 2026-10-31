package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/offer-availability/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card).
const (
	skillCheckAvailability = "check_availability"
	skillFeedOutcome       = "feed_outcome"
)

// skillRequest is the wire contract of every skill call: the first message
// part carries (as a Data part, or a Text part holding the same JSON) the
// skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored, and a
// misspelled name like "nodeID2" must not silently drop a filter. (Go's
// encoding/json matches field names case-insensitively, so "nodeid" still
// reaches nodeId; only genuinely unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// checkAvailabilityArgs and feedOutcomeArgs are the per-skill argument
// shapes. The optional page size is a pointer so an explicit 0 reaches the
// use case (and is refused there) instead of silently meaning "default".
type checkAvailabilityArgs struct {
	CatalogItemID string `json:"catalogItemId"`
	NodeID        string `json:"nodeId"`
	Limit         *int   `json:"limit"`
	After         string `json:"after"`
}

type feedOutcomeArgs struct {
	FeedID string `json:"feedId"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: a message-only agent, so a skill run
// is exactly one request mapped to one answer.
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, prob := parseSkillRequest(ec)
	if prob != nil {
		return reply(ec, envelope{Problem: prob}, prob.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	dto, prob := run(ctx, deps, req)
	if prob != nil {
		span.SetStatus(codes.Error, prob.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: prob}, prob.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: &dto}, dto.summary)
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (skillDTO, *problem) {
	switch req.Skill {
	case skillCheckAvailability:
		return deps.checkAvailability(ctx, req.Arguments)
	case skillFeedOutcome:
		return deps.feedOutcome(ctx, req.Arguments)
	default:
		return skillDTO{}, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first part.
// A Data part (or a Text part holding the same JSON object) is accepted;
// anything else is a request-shape problem, answered rather than raised
// because a malformed delegation is a business answer for the calling agent,
// not a transport failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own required-field checks produce the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// checkAvailability is the composed read skill: the listings of an item
// and/or a node with their availability, plus the catalogue item itself when
// one was named. It goes through the same read use cases as GET /catalog/{id}
// and GET /listings.
func (d Deps) checkAvailability(ctx context.Context, rawArgs json.RawMessage) (skillDTO, *problem) {
	var args checkAvailabilityArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return skillDTO{}, problemInvalidArgument(fmt.Sprintf("arguments: %v", err))
	}
	itemID, nodeID := strings.TrimSpace(args.CatalogItemID), strings.TrimSpace(args.NodeID)
	if itemID == "" && nodeID == "" {
		return skillDTO{}, problemInvalidArgument("catalogItemId or nodeId is required (or both)")
	}
	var item *views.CatalogItem
	if itemID != "" {
		found, err := d.GetCatalogItem.Execute(ctx, catalog.ID(itemID))
		if err != nil {
			return skillDTO{}, problemFromUseCase(ctx, err)
		}
		item = &found
	}
	page, err := d.ListListings.Execute(ctx, usecases.ListListingsQuery{
		NodeID:        listing.NodeID(nodeID),
		CatalogItemID: listing.ItemID(itemID),
		Page:          usecases.PageQuery{Limit: args.Limit, After: args.After},
	})
	if err != nil {
		return skillDTO{}, problemFromUseCase(ctx, err)
	}
	return toAvailability(item, page), nil
}

// feedOutcome reads one vendor inventory feed through GET /inventory-feeds/{feedId}'s
// use case.
func (d Deps) feedOutcome(ctx context.Context, rawArgs json.RawMessage) (skillDTO, *problem) {
	var args feedOutcomeArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return skillDTO{}, problemInvalidArgument(fmt.Sprintf("arguments: %v", err))
	}
	feedID := strings.TrimSpace(args.FeedID)
	if feedID == "" {
		return skillDTO{}, problemInvalidArgument("feedId is required")
	}
	f, err := d.GetInventoryFeed.Execute(ctx, feed.ID(feedID))
	if err != nil {
		return skillDTO{}, problemFromUseCase(ctx, err)
	}
	return toFeedOutcome(f), nil
}

// problemFromUseCase maps a use case error to a problem answer with the SAME
// classification as the REST adapter (internal/adapters/inbound/http
// problemCatalogue), restricted to what a READ can raise: a miss is 404
// not-found and a bad page size is 422 validation-failed, exactly the REST
// statuses. The write-side rejections (duplicate-listing, feed-already-applied,
// ...) are unreachable from these skills. Anything unrecognised is an internal
// failure, still answered as a problem (500) so the calling agent always
// receives a structured result; infrastructure details never reach it.
func problemFromUseCase(ctx context.Context, err error) *problem {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		return &problem{Type: "not-found", Title: "Resource not found", Detail: err.Error(), Status: 404}
	case errors.Is(err, shared.ErrValidation):
		return &problem{Type: "validation-failed", Title: "Validation failed", Detail: err.Error(), Status: 422}
	default:
		slog.ErrorContext(ctx, "a2a skill failed with an unexpected error", "error", err)
		return &problem{Type: "internal-error", Title: "Internal server error", Detail: "the skill failed; see server logs (trace id in the response metadata)", Status: 500}
	}
}
