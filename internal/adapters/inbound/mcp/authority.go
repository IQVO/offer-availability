package mcp

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Authority levels (Agentic Standard v2, AS-1; contracts.md G1/G2). Every
// registered tool declares exactly one of these in its Meta under
// authorityMetaKey, so a caller knows what gating to apply before letting a
// model use the tool. The fleet rule is agents/fleet/agent-authority.md in
// IQVO/warehouse-docs; the pinned declaration shape is checked statically by
// internal/architecture's mcp_authority_fitness_test.go (copied from the
// template), which requires these exact names.
const (
	// authorityMetaKey is the tools/list _meta key carrying a tool's level.
	authorityMetaKey = "com.warehouse/authority"
	// authorityL1 reads facts and changes nothing.
	authorityL1 = "L1"
	// authorityL2 is a pure recommendation: computes a proposal, changes nothing.
	authorityL2 = "L2" //nolint:unused // pinned shape (contracts G1): the fleet declares all four; this read-only context grants only L1 today.
	// authorityL3 mutates state; the caller is accountable.
	authorityL3 = "L3" //nolint:unused // pinned shape (contracts G1); an ADR would have to amend the read-only charter (ADR 0007) before any L3 tool exists here.
	// authorityL4 mutates state autonomously within an ADR-granted envelope.
	authorityL4 = "L4" //nolint:unused // pinned shape (contracts G1); no ADR in this context grants L4.
)

// authorityMeta is the Meta a tool registers to declare its authority level
// (returned verbatim in tools/list _meta, contract S4).
func authorityMeta(level string) mcp.Meta { return mcp.Meta{authorityMetaKey: level} } //nolint:unparam // pinned shape (contracts G2): every tool here is L1, but the helper keeps the fleet's level parameter.
