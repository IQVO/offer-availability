// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// Per docs/adr/0007-mcp-server-adoption.md the surface is READ-ONLY: every
// tool reads offer-availability data (browse_catalog, get_catalog_item,
// list_listings, get_inventory_feed); creating an item or a listing and
// applying an inventory feed stay on REST, and governance_test.go fails the
// build on a write-verb tool name. This package depends inward on the
// application layer and the domain only, never on an outbound adapter or the
// REST adapter, and nothing else may depend on it
// (internal/architecture's TestMCPAdapterDependencyRule). The composition
// root (cmd/mcp) wires concrete repositories into the use cases. There is no
// auth of any kind (fleet-wide revert 2026-09-11; the api router's
// TestNoAuthMiddlewareReintroduced scans this tree too).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "offer-availability-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Offer & Availability of the retail network (read-only): what the network can sell and from which node. " +
	"Page through the catalogue with browse_catalog (keyset pages: pass the previous page's nextCursor as after) or read " +
	"one item with get_catalog_item (its dimensions, weight, hazmat and fragile flags, and whether it is discontinued); " +
	"see which nodes sell an item with list_listings (filter by nodeId and/or catalogItemId; each listing says LIVE or " +
	"SUPPRESSED and why, and the availability a vendor's feed last gave it); and read one vendor inventory feed's outcome " +
	"with get_inventory_feed (applied, superseded or failed, with its counters and failure code). Nothing here changes " +
	"data: creating a catalogue item, listing an item on a node and applying an inventory feed are REST operations of " +
	"offer-availability."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
