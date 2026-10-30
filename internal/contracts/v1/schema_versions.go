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
	// CHAOS-4042: the anchor membership-verify semantic major, reserved
	// alongside v1 -- a result issued under this schema_version is only
	// ever produced when the offer-generation/redemption wiring in the
	// follow-up PR actually mints one; advertising it here now keeps a
	// client's capability discovery ahead of that, not behind it.
	ContextFabricInvestigationResultSchemaV2,
	ContextFabricAnswerProjectionSchema,
	ContextFabricProjectionBatchSchema,
	// CHAOS-11909: one event of the investigation event stream
	// (Accept: text/event-stream on the create-investigation route).
	// Reserved ahead of the route writer; the stream flag gates it.
	ContextFabricInvestigationEventSchema,
	// CHAOS-11913: the caller's own submission record
	// (GET /api/v1/context-fabric/investigations/submissions/{submission_id}).
	ContextFabricSubmissionStatusSchema,
	// The Ask Dev conversation read and list
	// (GET /api/v1/context-fabric/threads/{thread_id} and /threads).
	// Published ahead of the routes that serve them.
	ContextFabricThreadSchema,
	ContextFabricThreadListSchema,
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
