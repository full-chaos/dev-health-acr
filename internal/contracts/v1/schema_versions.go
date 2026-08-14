package v1

// AllSchemaVersions is the single canonical source of truth for every
// schema_version literal this platform issues, accepts, or has reserved in a
// published contract bundle. It spans the HTTP contract, Context Fabric
// investigation/projection contracts, and MCP-facing contracts.
//
// The hosted API's capabilities handshake advertises exactly this list via
// Capabilities.SupportedSchemaVersions, and internal/mcp's startup
// compatibility gate enforces that its own required subset is fully contained
// within it. A reserved schema may be advertised before its route is enabled;
// endpoint availability remains an independent capability/entitlement gate.
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
	ContextFabricInvestigationRequestSchema,
	ContextFabricInvestigationResultSchema,
	ContextFabricAnswerProjectionSchema,
	ContextFabricProjectionBatchSchema,
	ContextFabricOrgModelConfigSchema,
	ContextFabricOrgModelConfigWriteRequestSchema,
	MCPContextForTaskRequestSchema,
	MCPContextForTaskResponseSchema,
	MCPSourceEvidenceRequestSchema,
	MCPSourceEvidenceResponseSchema,
	MCPInvestigateQuestionRequestSchema,
	MCPInvestigateQuestionResponseSchema,
	MCPInvestigationResultRequestSchema,
	MCPInvestigationResultResponseSchema,
	MCPRecordEpisodeRequestSchema,
	MCPRecordEpisodeResponseSchema,
}
