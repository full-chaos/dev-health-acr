package v1

// AllSchemaVersions is the single canonical source of truth for every
// schema_version literal this platform issues or accepts, spanning both
// the HTTP contract (ContextPacket*, EvidenceRef, ExpandedEvidence,
// Capabilities, AgentEpisode*, ClientCredential, Error) and the MCP-facing
// contracts (MCPContextForTask*, MCPSourceEvidence*).
//
// The hosted API's capabilities handshake (cmd/acr-api) advertises exactly
// this list via Capabilities.SupportedSchemaVersions, and internal/mcp's
// startup compatibility gate enforces that its own required subset
// (ourSchemaVersions in internal/mcp/compat.go) is fully contained within
// it -- see internal/mcp's TestOurSchemaVersionsAreSubsetOfCanonicalSchemaVersions.
//
// This list previously existed as two independently hand-typed slices (one
// in cmd/acr-api/main.go, one in internal/mcp/compat.go) that silently drifted:
// the hosted API's list omitted all four MCP-only schema versions, so a
// real hosted deployment could never satisfy the MCP sidecar's startup
// compatibility check. Keeping one authoritative list closes that drift
// class permanently instead of only patching the immediate mismatch.
var AllSchemaVersions = []string{
	ContextPacketRequestSchema,
	ContextPacketSchema,
	ContextPacketItemSchema,
	EvidenceRefSchema,
	ExpandedEvidenceSchema,
	CapabilitiesSchema,
	AgentEpisodeCreateSchema,
	AgentEpisodeSchema,
	ClientCredentialSchema,
	ErrorSchema,
	MCPContextForTaskRequestSchema,
	MCPContextForTaskResponseSchema,
	MCPSourceEvidenceRequestSchema,
	MCPSourceEvidenceResponseSchema,
}
