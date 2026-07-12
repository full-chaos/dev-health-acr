package v1

import "time"

// MCP-specific read-tool contracts. These types are deliberately separate
// from the HTTP ContextPacketRequest/ExpandedEvidence shapes: MCP clients
// send an ergonomic, goal-only-by-default payload, and the sidecar maps it
// onto the hosted HTTP contract with fixed safe defaults for any field the
// caller omits. Never reuse an HTTP request schema directly for an MCP tool
// input; see mcp_contracts_test.go for the regression that pins this.
const (
	MCPContextForTaskRequestSchema  = "mcp_context_for_task_request.v1"
	MCPContextForTaskResponseSchema = "mcp_context_for_task_response.v1"
	MCPSourceEvidenceRequestSchema  = "mcp_source_evidence_request.v1"
	MCPSourceEvidenceResponseSchema = "mcp_source_evidence_response.v1"
)

// MCPContextForTaskRequest is the input contract for the context_for_task
// MCP tool. Only goal is required; repository, scope, and budget are
// optional ergonomic overrides. Unlike the HTTP contract, this request has
// no caller-supplied schema_version wire field: tool identity is
// established by the MCP call itself (the tool name), exactly like
// MCPSourceEvidenceRequest below. MCPContextForTaskRequestSchema still
// exists as a schema-identity constant consumed by the tools manifest and
// the startup compatibility negotiation in internal/mcp/compat.go -- it is
// never a field a caller sets. When repository/scope/budget are omitted,
// the sidecar fills fixed safe defaults before mapping to
// ContextPacketRequest.
type MCPContextForTaskRequest struct {
	Goal       string             `json:"goal"`
	Repository *MCPRepositoryRef  `json:"repository,omitempty"`
	Scope      *MCPRequestedScope `json:"scope,omitempty"`
	Budget     *MCPBudget         `json:"budget,omitempty"`
}

// MCPRepositoryRef accepts only an explicit "owner/repo" slug. Unlike the
// HTTP RepositoryRef, it never accepts repo_id or remote_url: MCP clients
// identify a repository by slug only.
type MCPRepositoryRef struct {
	Slug string `json:"slug"`
}

// MCPRequestedScope mirrors the retrieval-scoping fields of the HTTP
// RequestedScope. as_of lets a caller pin retrieval to a point in time,
// same as the HTTP contract's scope.as_of. include_changed_files is an
// MCP-only ergonomic addition with no HTTP analogue: a tri-state pointer
// bool so the sidecar can distinguish "caller did not say" (nil, sidecar
// applies its own default) from an explicit "yes, include bounded
// git-changed files when files is not already set" (true) or an explicit
// "never auto-include them" (false).
type MCPRequestedScope struct {
	Branch              string     `json:"branch,omitempty"`
	CommitSHA           string     `json:"commit_sha,omitempty"`
	TaskRef             string     `json:"task_ref,omitempty"`
	Files               []string   `json:"files,omitempty"`
	AsOf                *time.Time `json:"as_of,omitempty"`
	TimeWindowDays      int        `json:"time_window_days,omitempty"`
	IncludeChangedFiles *bool      `json:"include_changed_files,omitempty"`
}

// MCPBudget lets a client optionally narrow the packet budget. Any field
// left unset (zero value) is filled with the fixed safe HTTP default.
type MCPBudget struct {
	MaxItems           int `json:"max_items,omitempty"`
	MaxOutputTokens    int `json:"max_output_tokens,omitempty"`
	MaxSerializedBytes int `json:"max_serialized_bytes,omitempty"`
}

// MCPRenderedMarkdown is a bounded, human-readable rendering of a structured
// contract. It is always marked untrusted: the underlying content is
// retrieved third-party data, never instructions, per contracts/README.md
// rule 6.
type MCPRenderedMarkdown struct {
	Markdown  string `json:"markdown"`
	Untrusted bool   `json:"untrusted"`
	Truncated bool   `json:"truncated"`
}

// MCPContextForTaskResponse wraps the structured ContextPacket contract
// together with a bounded, explicitly untrusted markdown rendering for
// display-oriented MCP clients.
type MCPContextForTaskResponse struct {
	SchemaVersion    string              `json:"schema_version"`
	Structured       ContextPacket       `json:"structured"`
	RenderedMarkdown MCPRenderedMarkdown `json:"rendered_markdown"`
}

// MCPSourceEvidenceRequest is the input contract for the source_evidence
// MCP tool. It accepts exactly evidence_ref_id: no schema_version wire
// field, since the tool identity is established by the MCP call itself.
type MCPSourceEvidenceRequest struct {
	EvidenceRefID string `json:"evidence_ref_id"`
}

// MCPSourceEvidenceResponse wraps the structured ExpandedEvidence contract
// together with a bounded, explicitly untrusted markdown rendering.
type MCPSourceEvidenceResponse struct {
	SchemaVersion    string              `json:"schema_version"`
	Structured       ExpandedEvidence    `json:"structured"`
	RenderedMarkdown MCPRenderedMarkdown `json:"rendered_markdown"`
}
