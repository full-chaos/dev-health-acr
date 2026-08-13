package v1

// MCP Context Fabric answer contracts (CHAOS-3746).
//
// These expose the SAME investigation core the hosted API serves. They are
// deliberately separate shapes from the HTTP investigation contract for the
// reason mcp_types.go already records: an MCP caller sends an ergonomic,
// question-first payload, and the sidecar maps it onto the hosted contract
// with fixed safe defaults for everything omitted. An MCP tool input is
// never a hosted request schema reused verbatim.
//
// Neither request carries a schema_version wire field: tool identity is
// established by the MCP call itself. The constants below exist as schema
// identities for the tool manifest and the startup compatibility
// negotiation in internal/mcp/compat.go.
const (
	MCPInvestigateQuestionRequestSchema  = "mcp_investigate_question_request.v1"
	MCPInvestigateQuestionResponseSchema = "mcp_investigate_question_response.v1"
	MCPInvestigationResultRequestSchema  = "mcp_investigation_result_request.v1"
	MCPInvestigationResultResponseSchema = "mcp_investigation_result_response.v1"
)

// MCPInvestigateQuestionRequest is the input contract for the
// investigate_question MCP tool. Only question is required.
//
// Two omissions are deliberate.
//
// There is no time-axis field. The engine can only answer questions about
// current state today (contextfabric.ErrUnsupportedTimeAxis), so offering
// the knob would advertise a capability that does not exist and would be
// silently ignored -- an ignored option is a lie told in a schema.
// CHAOS-3781 owns the historical axis; the field arrives with it.
//
// There are no subject hints. Hints are a Workbench affordance for a UI
// that already resolved an entity the user clicked. An agent has no
// equivalent, and admitting hand-built hints here would let a caller push
// identity assertions into resolution rather than letting the graph resolve
// them. Scope IDs bound the search without asserting what anything is.
type MCPInvestigateQuestionRequest struct {
	Question             string                             `json:"question"`
	Conversation         []ContextFabricConversationTurn    `json:"conversation,omitempty"`
	PriorSubjectReceipts []ContextFabricBoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
	Scope                *MCPInvestigationScope             `json:"scope,omitempty"`
	Budget               *MCPInvestigationBudget            `json:"budget,omitempty"`
	// AllowClarification is a tri-state pointer so the sidecar can tell
	// "caller did not say" (nil, apply the default: allowed) from an
	// explicit false. An agent that cannot ask its user a follow-up
	// question may legitimately prefer a best-effort answer over a
	// clarification request it can do nothing with.
	AllowClarification *bool `json:"allow_clarification,omitempty"`
	// IncludeFullResult asks for the canonical result alongside the
	// bounded projection. It is bounded by the same byte budget as the
	// rest of the response: when the full result would exceed it, the
	// result is DROPPED and projection_budget.full_result_omitted is set,
	// rather than any document being truncated into invalid JSON. The
	// projection stays complete and the caller still holds result_id, so
	// investigation_result remains available for the full detail.
	IncludeFullResult bool `json:"include_full_result,omitempty"`
}

// MCPInvestigationScope bounds which repositories, projects, and teams the
// investigation may consider. It carries identifiers only: it narrows the
// search without asserting what any subject is.
type MCPInvestigationScope struct {
	RepositorySlugs []string `json:"repository_slugs,omitempty"`
	ProjectIDs      []string `json:"project_ids,omitempty"`
	TeamIDs         []string `json:"team_ids,omitempty"`
}

// MCPInvestigationBudget optionally narrows the answer budget. Any field
// left unset is filled with the sidecar's own agent-appropriate default and
// then clamped to what the hosted API advertises for this credential, so a
// caller can never request more than the service actually grants.
type MCPInvestigationBudget struct {
	MaxDrivers         int `json:"max_drivers,omitempty"`
	MaxCohortMembers   int `json:"max_cohort_members,omitempty"`
	MaxEvidenceRefs    int `json:"max_evidence_refs,omitempty"`
	MaxSerializedBytes int `json:"max_serialized_bytes,omitempty"`
}

// MCPInvestigateQuestionResponse wraps the bounded answer projection with a
// rendered, explicitly untrusted markdown view, and optionally the full
// canonical result.
//
// Structured is the projection rather than the canonical result on purpose:
// an agent needs an answer it can act on, and the projection is the shared
// narrowing both surfaces apply. FullResult is present only when the caller
// asked for it AND it fit the byte budget.
type MCPInvestigateQuestionResponse struct {
	SchemaVersion    string                            `json:"schema_version"`
	Structured       ContextFabricAnswerProjection     `json:"structured"`
	FullResult       *ContextFabricInvestigationResult `json:"full_result,omitempty"`
	RenderedMarkdown MCPRenderedMarkdown               `json:"rendered_markdown"`
}

// MCPInvestigationResultRequest is the input contract for the
// investigation_result MCP tool. It accepts exactly result_id, an opaque
// handle a prior answer returned. Nothing parses it.
type MCPInvestigationResultRequest struct {
	ResultID string `json:"result_id"`
}

// MCPInvestigationResultResponse carries the full canonical result. This
// tool exists precisely to supply the detail a bounded projection dropped,
// so it does not narrow anything.
type MCPInvestigationResultResponse struct {
	SchemaVersion    string                           `json:"schema_version"`
	Structured       ContextFabricInvestigationResult `json:"structured"`
	RenderedMarkdown MCPRenderedMarkdown              `json:"rendered_markdown"`
}
