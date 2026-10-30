package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/memory"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/feed"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// harness builds Deps over the in-memory repositories (the unit-test seam;
// cmd/a2a never imports them) and seeds through the real write use cases,
// the way the REST adapter does. The A2A surface itself has no write.
type harness struct {
	deps   Deps
	store  *memory.Store
	create *usecases.CreateCatalogItem
	list   *usecases.CreateListing
	apply  *usecases.ApplyInventoryFeed

	itemID, liveID, suppressedID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s := memory.NewStore()
	d := usecases.Deps{
		Catalog: s.Catalog(), Listings: s.Listings(), Feeds: s.Feeds(), Marks: s.Marks(), Nodes: s.Nodes(),
		Inbox: s.Inbox(), Outbox: s.Outbox(), UoW: s.UnitOfWork(), Clock: memory.NewClock(now), IDs: &memory.SequentialIDs{},
	}
	return &harness{
		deps: Deps{
			GetCatalogItem:   usecases.NewGetCatalogItem(d.Catalog),
			ListListings:     usecases.NewListListings(d.Listings),
			GetInventoryFeed: usecases.NewGetInventoryFeed(d.Feeds),
		},
		store:  s,
		create: usecases.NewCreateCatalogItem(d), list: usecases.NewCreateListing(d), apply: usecases.NewApplyInventoryFeed(d),
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (h *harness) createItem(t *testing.T) string {
	t.Helper()
	item, err := h.create.Execute(t.Context(), usecases.CreateCatalogItemCommand{
		Title: "Study kettle", LengthMm: 220, WidthMm: 160, HeightMm: 180, WeightG: 900,
	})
	must(t, err)
	return item.CatalogItemID
}

func (h *harness) listOn(t *testing.T, item, node string, active bool) string {
	t.Helper()
	if active {
		v, err := nodestatus.New(nodestatus.ID(node), nodestatus.StatusActive, now)
		must(t, err)
		must(t, h.store.Nodes().Save(t.Context(), v))
	}
	l, err := h.list.Execute(t.Context(), usecases.CreateListingCommand{CatalogItemID: catalog.ID(item), NodeID: listing.NodeID(node)})
	must(t, err)
	return l.ListingID
}

// seededWorld: one item, a LIVE listing with 12 units on node-a (set by
// feed-7) and a SUPPRESSED listing on node-b, which was never activated.
func seededWorld(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.itemID = h.createItem(t)
	h.liveID = h.listOn(t, h.itemID, "node-a", true)
	h.suppressedID = h.listOn(t, h.itemID, "node-b", false)
	_, err := h.apply.Execute(t.Context(), usecases.ApplyInventoryFeedCommand{
		FeedID: feed.ID("feed-7"), NodeID: "node-a", AsOf: now,
		Lines: []feed.Line{{ListingID: listing.ID(h.liveID), Qty: 12, Basis: listing.BasisPhysical}},
	})
	must(t, err)
	return h
}

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  *struct {
		Availability *struct {
			Item *struct {
				CatalogItemID string `json:"catalogItemId"`
				Title         string `json:"title"`
				State         string `json:"state"`
				WeightG       int    `json:"weightG"`
			} `json:"item"`
			Listings   []wireListing `json:"listings"`
			NextCursor string        `json:"nextCursor"`
		} `json:"availability"`
		FeedOutcome *struct {
			FeedID       string `json:"feedId"`
			NodeID       string `json:"nodeId"`
			State        string `json:"state"`
			LineCount    int    `json:"lineCount"`
			AppliedCount int    `json:"appliedCount"`
			AppliedAt    string `json:"appliedAt"`
			FailureCode  string `json:"failureCode"`
		} `json:"feedOutcome"`
	} `json:"data"`
	Problem *struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Status int    `json:"status"`
	} `json:"problem"`
}

// wireListing is the test-local copy of one listing of the answer.
type wireListing struct {
	ListingID         string `json:"listingId"`
	CatalogItemID     string `json:"catalogItemId"`
	NodeID            string `json:"nodeId"`
	State             string `json:"state"`
	SuppressionReason string `json:"suppressionReason"`
	Availability      struct {
		AvailableQty int    `json:"availableQty"`
		Basis        string `json:"basis"`
		AsOf         string `json:"asOf"`
		FeedID       string `json:"feedId"`
	} `json:"availability"`
}

type wire struct{ client *a2aclient.Client }

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	client, err := a2aclient.NewFromCard(context.Background(), Card(Config{AgentURL: srv.URL + "/", Version: "test"}))
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the decoded envelope and the
// one-line text summary of the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string) {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text)
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	return w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d %s problem, got data: %+v", status, slug, env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != slug {
		t.Fatalf("problem = %+v, want %d %s", env.Problem, status, slug)
	}
	if env.Data != nil {
		t.Fatalf("a problem answer must carry no data: %+v", env.Data)
	}
}

func TestWireCheckAvailabilityByItem(t *testing.T) {
	h := seededWorld(t)
	w := newWire(t, h.deps)

	env, text := w.skill(t, skillCheckAvailability, map[string]any{"catalogItemId": h.itemID})
	if env.Problem != nil || env.Data == nil || env.Data.Availability == nil || env.Skill != skillCheckAvailability {
		t.Fatalf("envelope = %+v", env)
	}
	a := env.Data.Availability
	if a.Item == nil || a.Item.CatalogItemID != h.itemID || a.Item.Title != "Study kettle" || a.Item.State != "ACTIVE" || a.Item.WeightG != 900 {
		t.Fatalf("item = %+v", a.Item)
	}
	if len(a.Listings) != 2 || a.NextCursor != "" {
		t.Fatalf("listings = %+v", a.Listings)
	}
	assertLiveAndSuppressed(t, a.Listings)
	want := h.itemID + " (ACTIVE): 2 listings (1 LIVE, 1 SUPPRESSED), 12 units reported on the LIVE ones"
	if text != want {
		t.Fatalf("summary = %q, want %q", text, want)
	}
}

// assertLiveAndSuppressed checks the two listings of seededWorld by node.
func assertLiveAndSuppressed(t *testing.T, listings []wireListing) {
	t.Helper()
	byNode := map[string]wireListing{}
	for _, l := range listings {
		byNode[l.NodeID] = l
	}
	live, suppressed := byNode["node-a"], byNode["node-b"]
	if live.State != "LIVE" || live.Availability.AvailableQty != 12 || live.Availability.Basis != "PHYSICAL" ||
		live.Availability.FeedID != "feed-7" || live.Availability.AsOf != "2026-10-23T12:00:00Z" {
		t.Fatalf("the live listing = %+v", live)
	}
	if suppressed.State != "SUPPRESSED" || suppressed.SuppressionReason != "NODE_NOT_SELLABLE" {
		t.Fatalf("the suppressed listing = %+v", suppressed)
	}
}

func TestWireCheckAvailabilityByNodeAndPaging(t *testing.T) {
	h := seededWorld(t)
	w := newWire(t, h.deps)

	env, _ := w.skill(t, skillCheckAvailability, map[string]any{"nodeId": "node-a"})
	a := env.Data.Availability
	if a == nil || a.Item != nil || len(a.Listings) != 1 || a.Listings[0].ListingID != h.liveID {
		t.Fatalf("nodeId filter = %+v (a node-only read carries no item)", env)
	}

	first, _ := w.skill(t, skillCheckAvailability, map[string]any{"catalogItemId": h.itemID, "limit": 1})
	cursor := first.Data.Availability.NextCursor
	if len(first.Data.Availability.Listings) != 1 || cursor == "" {
		t.Fatalf("first page = %+v", first.Data.Availability)
	}
	second, text := w.skill(t, skillCheckAvailability, map[string]any{"catalogItemId": h.itemID, "limit": 1, "after": cursor})
	if len(second.Data.Availability.Listings) != 1 || second.Data.Availability.NextCursor != "" ||
		second.Data.Availability.Listings[0].ListingID == first.Data.Availability.Listings[0].ListingID {
		t.Fatalf("second page = %+v", second.Data.Availability)
	}
	if strings.Contains(text, "next page") {
		t.Fatalf("the last page must not advertise a next one: %q", text)
	}

	env, _ = w.skill(t, skillCheckAvailability, map[string]any{"nodeId": "node-none"})
	if env.Data == nil || len(env.Data.Availability.Listings) != 0 {
		t.Fatalf("a node with no listings = %+v, want an empty, successful answer", env)
	}
}

func TestWireFeedOutcome(t *testing.T) {
	h := seededWorld(t)
	w := newWire(t, h.deps)

	env, text := w.skill(t, skillFeedOutcome, map[string]any{"feedId": "feed-7"})
	f := env.Data.FeedOutcome
	if env.Problem != nil || f == nil || f.FeedID != "feed-7" || f.NodeID != "node-a" || f.State != "APPLIED" ||
		f.LineCount != 1 || f.AppliedCount != 1 || f.AppliedAt != "2026-10-23T12:00:00Z" || f.FailureCode != "" {
		t.Fatalf("envelope = %+v", env)
	}
	if text != "feed-7 on node-a: APPLIED, 1 lines (1 applied, 0 stale, 0 zeroed)" {
		t.Fatalf("summary = %q", text)
	}
}

// Domain rejections are ANSWERED with the REST adapter's classification:
// a miss is 404 not-found, a bad page size 422 validation-failed.
func TestWireDomainRejectionsAreAnsweredNotRaised(t *testing.T) {
	h := seededWorld(t)
	w := newWire(t, h.deps)

	env, _ := w.skill(t, skillCheckAvailability, map[string]any{"catalogItemId": "item-nope"})
	wantProblem(t, env, 404, "not-found")

	env, _ = w.skill(t, skillFeedOutcome, map[string]any{"feedId": "feed-nope"})
	wantProblem(t, env, 404, "not-found")

	for _, limit := range []int{0, -1, 501} {
		env, _ = w.skill(t, skillCheckAvailability, map[string]any{"nodeId": "node-a", "limit": limit})
		wantProblem(t, env, 422, "validation-failed")
	}
}

func TestWireArgumentValidation(t *testing.T) {
	w := newWire(t, newHarness(t).deps)
	cases := map[string]struct {
		skill string
		args  map[string]any
	}{
		"check: no filter":            {skillCheckAvailability, map[string]any{}},
		"check: blank filters":        {skillCheckAvailability, map[string]any{"catalogItemId": "  ", "nodeId": ""}},
		"check: only paging":          {skillCheckAvailability, map[string]any{"limit": 5}},
		"check: unknown argument":     {skillCheckAvailability, map[string]any{"nodeId": "node-a", "sku": "x"}},
		"check: typo'd argument":      {skillCheckAvailability, map[string]any{"nodeId": "node-a", "limt": 5}},
		"check: wrong type for limit": {skillCheckAvailability, map[string]any{"nodeId": "node-a", "limit": "ten"}},
		"check: fractional limit":     {skillCheckAvailability, map[string]any{"nodeId": "node-a", "limit": 1.5}},
		"check: wrong type for id":    {skillCheckAvailability, map[string]any{"catalogItemId": 7}},
		"feed: missing feedId":        {skillFeedOutcome, map[string]any{}},
		"feed: blank feedId":          {skillFeedOutcome, map[string]any{"feedId": "  "}},
		"feed: unknown argument":      {skillFeedOutcome, map[string]any{"feedId": "feed-7", "node": "a"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, _ := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, 400, "invalid-argument")
		})
	}
}

func TestWireRequestShapeProblems(t *testing.T) {
	w := newWire(t, newHarness(t).deps)

	env, _ := w.call(t, a2a.NewTextPart("not json at all"))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewTextPart(`{"skill":"feed_outcome","arguments":{"feedId":"feed-7"}} {"trailing":1}`))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"skill": skillFeedOutcome, "arguments": map[string]any{}, "extra": 1}))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"skill": skillFeedOutcome, "arguments": "feed-7"}))
	wantProblem(t, env, 400, "invalid-argument")

	for _, write := range []string{"create_catalog_item", "create_listing", "apply_inventory_feed", "discontinue_catalog_item", "launch_rocket"} {
		env, _ = w.skill(t, write, map[string]any{})
		wantProblem(t, env, 400, "unknown-skill")
	}
}

// A text part holding the same JSON is accepted (documented contract).
func TestWireTextPartHoldingTheSkillJSON(t *testing.T) {
	h := seededWorld(t)
	w := newWire(t, h.deps)
	env, _ := w.call(t, a2a.NewTextPart(`{"skill":"feed_outcome","arguments":{"feedId":"feed-7"}}`))
	if env.Problem != nil || env.Data == nil || env.Data.FeedOutcome == nil {
		t.Fatalf("a text part holding the skill JSON must be accepted, got %+v", env)
	}
}

// An unexpected use case failure is answered as a 500 problem and never
// leaks the infrastructure error text.
func TestWireUnexpectedFailuresAreSanitised(t *testing.T) {
	h := seededWorld(t)
	deps := h.deps
	deps.GetInventoryFeed = usecases.NewGetInventoryFeed(brokenFeeds{memory: h.store.Feeds()})
	w := newWire(t, deps)

	env, text := w.skill(t, skillFeedOutcome, map[string]any{"feedId": "feed-7"})
	wantProblem(t, env, 500, "internal-error")
	for _, s := range []string{env.Problem.Detail, text} {
		if strings.Contains(s, "postgres://") || strings.Contains(s, "secret-dsn") {
			t.Fatalf("an answer leaks the infrastructure error: %q", s)
		}
	}
}

// Reads add no outbox row: the agent is read-only by construction.
func TestWireReadsEmitNothing(t *testing.T) {
	h := seededWorld(t)
	w := newWire(t, h.deps)
	before := h.store.Counts().OutboxEvents
	if before == 0 {
		t.Fatal("the seeded world published no event; the sensor below would pass vacuously")
	}
	w.skill(t, skillCheckAvailability, map[string]any{"catalogItemId": h.itemID})
	w.skill(t, skillCheckAvailability, map[string]any{"nodeId": "node-a"})
	w.skill(t, skillFeedOutcome, map[string]any{"feedId": "feed-7"})
	if after := h.store.Counts().OutboxEvents; after != before {
		t.Fatalf("outbox rows %d -> %d: a skill emitted an event", before, after)
	}
}

// TestCardIsServedAndDialable proves discovery end to end: the card handler
// serves the card at the well-known path and a client built from the FETCHED
// card reaches the agent.
func TestCardIsServedAndDialable(t *testing.T) {
	h := seededWorld(t)
	mux := http.NewServeMux()
	mux.Handle("/", Handler(NewExecutor(h.deps)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(Card(Config{AgentURL: srv.URL + "/", Version: "test"})))

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := (&wire{client: client}).skill(t, skillFeedOutcome, map[string]any{"feedId": "feed-7"})
	if env.Data == nil || env.Data.FeedOutcome == nil {
		t.Fatalf("a client built from the served card could not call the agent: %+v", env)
	}
}

// brokenFeeds fails every read with an infrastructure-looking error.
type brokenFeeds struct{ memory ports.FeedRepository }

func (brokenFeeds) Get(context.Context, feed.ID) (*feed.InventoryFeed, error) {
	return nil, errors.New("dial postgres://user:secret-dsn@db:5432/x: connection refused")
}

func (b brokenFeeds) Save(ctx context.Context, f *feed.InventoryFeed) error {
	return b.memory.Save(ctx, f)
}
