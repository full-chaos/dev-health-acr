package mcp

import (
	"encoding/json"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// renderedMarkdownMaxBytes matches the rendered_markdown.markdown maxLength
// bound shared by mcp_context_for_task_response.v1.schema.json and
// mcp_source_evidence_response.v1.schema.json.
const renderedMarkdownMaxBytes = 24000

// rawArgs returns the raw JSON arguments for a tool call, defaulting to an
// empty object so a client that omits "arguments" entirely decodes
// cleanly instead of failing on a nil/empty byte slice.
func rawArgs(req *mcpsdk.CallToolRequest) []byte {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return []byte("{}")
	}
	return req.Params.Arguments
}

// buildToolResult marshals a validated MCP response struct into
// StructuredContent and attaches the already-rendered, already-bounded
// untrusted markdown as the human-readable Content block. response's
// rendered_markdown.truncated field is trusted verbatim here: callers
// (handleContextForTask, handleSourceEvidence) set it directly from the
// sidecar renderer's own byte-budget bookkeeping
// (RenderContextPacketMarkdown/RenderEvidenceMarkdown's truncated return
// value), never re-derived by pattern-matching the untrusted rendered
// text, so hosted content that happens to contain the renderer's
// truncation-notice wording can never be mistaken for actual truncation.
func buildToolResult(response any, markdown string) (*mcpsdk.CallToolResult, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the response could not be encoded"}), nil
	}
	return &mcpsdk.CallToolResult{
		Content:           []mcpsdk.Content{&mcpsdk.TextContent{Text: markdown}},
		StructuredContent: json.RawMessage(encoded),
	}, nil
}
