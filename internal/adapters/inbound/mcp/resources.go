package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// catalogItemResourceURI is the URI template of the one resource this server
// offers: a scoped read model this context owns, not a table dump. It answers
// "what is this sellable item?" for ONE catalogue item — its dimensions,
// weight, hazmat/fragile flags, GS1 identifier and packaging hierarchy — the
// same bounded payload get_catalog_item returns, built from the same
// GetCatalogItem read use case the REST adapter's GET /catalog/{id} uses.
const catalogItemResourceURI = "warehouse://offer-availability/catalog/{catalogItemId}"

// registerResources adds the scoped read-model resource (Agentic Standard v2,
// AS-3). Resources are read-only context data an agent can load without a
// tool call; by nature they are authority L1 and need no tool Meta.
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: catalogItemResourceURI,
		Name:        "catalogue item",
		Description: "One catalogue item of the retail network by id: title, dimensions in mm, weightG, hazmat and fragile flags, state (ACTIVE or DISCONTINUED), its GS1 identifier and its packaging hierarchy — the same read model as get_catalog_item.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		itemID, err := parseCatalogItemURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		item, err := d.GetCatalogItem.Execute(ctx, catalog.ID(itemID))
		if err != nil {
			return nil, mapError(ctx, err)
		}
		body, err := json.Marshal(toCatalogItem(item))
		if err != nil {
			return nil, fmt.Errorf("render catalogue item resource: %w", err)
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: mimeTypeJSON,
				Text:     string(body),
			}},
		}, nil
	})
}

// parseCatalogItemURI extracts the catalogue item id from a
// warehouse://offer-availability/catalog/{catalogItemId} URI. The segment is
// model-supplied and therefore untrusted; a malformed URI or an empty id is
// rejected rather than silently defaulted.
func parseCatalogItemURI(uri string) (string, error) {
	const prefix = "warehouse://offer-availability/catalog/"
	if len(uri) <= len(prefix) || uri[:len(prefix)] != prefix {
		return "", fmt.Errorf("resource uri %q must be %s", uri, catalogItemResourceURI)
	}
	itemID := uri[len(prefix):]
	if itemID == "" || containsSlash(itemID) {
		return "", fmt.Errorf("resource uri %q must carry exactly one catalogue item id after catalog/", uri)
	}
	return itemID, nil
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}
