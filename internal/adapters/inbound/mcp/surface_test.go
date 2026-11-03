// The MCP surface beyond tools (Agentic Standard v2, AS-3): one scoped
// read-model resource and one operator-SOP prompt, exercised through the
// in-memory client exactly as a host would discover and read them.
package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The catalogue-item resource template is discoverable via resources/list.
func TestMCP_CatalogItemResourceIsDiscoverable(t *testing.T) {
	h := seededWorld(t)
	templates, err := h.session.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	for _, tpl := range templates.ResourceTemplates {
		if tpl.URITemplate == "warehouse://offer-availability/catalog/{catalogItemId}" {
			if tpl.MIMEType != "application/json" {
				t.Errorf("template MIMEType = %q, want application/json", tpl.MIMEType)
			}
			return
		}
	}
	t.Fatalf("catalogue-item template not discoverable: %+v", templates.ResourceTemplates)
}

// Reading the resource with a seeded item's URI returns exactly that item as
// JSON text; an unknown item id is a not-found error, not empty content.
func TestMCP_CatalogItemResourceReadsOneItem(t *testing.T) {
	h := seededWorld(t)
	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{
		URI: "warehouse://offer-availability/catalog/" + h.itemID,
	})
	if err != nil {
		t.Fatalf("read catalogue item resource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d entries, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.MIMEType != "application/json" {
		t.Errorf("MIMEType = %q, want application/json", c.MIMEType)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(c.Text), &item); err != nil {
		t.Fatalf("resource text is not JSON: %v (%s)", err, c.Text)
	}
	if item["catalogItemId"] != h.itemID {
		t.Errorf("catalogItemId = %v, want %q", item["catalogItemId"], h.itemID)
	}
	if _, ok := item["title"]; !ok {
		t.Errorf("resource omits the item title: %s", c.Text)
	}
}

// A URI outside the template — wrong scheme, or a nested path — is rejected
// as a protocol error, and an unknown item id is the use case's not-found.
func TestMCP_CatalogItemResourceRejectsBadURIsAndUnknownItems(t *testing.T) {
	h := seededWorld(t)
	for _, uri := range []string{
		"warehouse://offer-availability/catalog/",
		"warehouse://offer-availability/catalog/item-1/extra",
		"staffing://not-this-context",
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("reading %q must be rejected", uri)
		}
	}
	if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{
		URI: "warehouse://offer-availability/catalog/item-does-not-exist",
	}); err == nil {
		t.Error("reading an unknown catalogue item must fail with not-found")
	}
}

// The operator SOP prompt is listable, describes its arguments, and renders
// with the caller's arguments; an unknown prompt is rejected.
func TestMCP_InvestigateAvailabilityPrompt(t *testing.T) {
	h := seededWorld(t)

	prompts, err := h.session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	found := false
	for _, p := range prompts.Prompts {
		if p.Name != "investigate_availability" {
			continue
		}
		found = true
		if len(p.Arguments) != 2 {
			t.Fatalf("prompt declares %d arguments, want 2 (catalogItemId, nodeId): %+v", len(p.Arguments), p.Arguments)
		}
		for _, a := range p.Arguments {
			if a.Description == "" {
				t.Errorf("prompt argument %q has no description", a.Name)
			}
		}
	}
	if !found {
		t.Fatalf("investigate_availability not listed: %+v", prompts.Prompts)
	}

	got, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{
		Name:      "investigate_availability",
		Arguments: map[string]string{"catalogItemId": h.itemID, "nodeId": "node-a"},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("prompt messages = %+v, want one user message", got.Messages)
	}
	text := promptText(t, got)
	if !strings.Contains(text, h.itemID) || !strings.Contains(text, "node-a") {
		t.Errorf("prompt does not carry the caller's arguments: %s", text)
	}
	if _, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "no_such_prompt"}); err == nil {
		t.Error("getting an unknown prompt must be rejected")
	}
}

// The SOP names every read tool the procedure walks through — it prescribes
// behaviour, it never calls the tools itself.
func TestMCP_InvestigateAvailabilitySOPNamesTheReadOnlyTools(t *testing.T) {
	h := seededWorld(t)
	got, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "investigate_availability"})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	text := promptText(t, got)
	for _, tool := range []string{"browse_catalog", "get_catalog_item", "list_listings", "get_inventory_feed"} {
		if !strings.Contains(text, tool) {
			t.Errorf("SOP never names %s", tool)
		}
	}
}

// promptText extracts the text of the single user message of a prompt result.
func promptText(t *testing.T, res *sdk.GetPromptResult) string {
	t.Helper()
	tc, ok := res.Messages[0].Content.(*sdk.TextContent)
	if !ok {
		t.Fatalf("prompt message content = %T, want TextContent", res.Messages[0].Content)
	}
	return tc.Text
}
