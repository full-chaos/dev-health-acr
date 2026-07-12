package mcp

import (
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleSourceEvidence implements the source_evidence tool: decode and
// validate the evidence_ref_id, call the hosted evidence endpoint, and
// return both a structured wrapper and a bounded, explicitly untrusted
// markdown rendering of the citation/excerpt.
func handleSourceEvidence(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var input contractsv1.MCPSourceEvidenceRequest
	if err := json.Unmarshal(rawArgs(req), &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "source_evidence arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "source_evidence arguments failed schema validation"}), nil
	}

	evidence, err := boot.Client.Evidence(ctx, input.EvidenceRefID)
	if err != nil {
		return toolErrorResult(err), nil
	}

	rendered, truncated := sidecar.RenderEvidenceMarkdown(evidence, renderedMarkdownMaxBytes)
	response := contractsv1.MCPSourceEvidenceResponse{
		SchemaVersion: contractsv1.MCPSourceEvidenceResponseSchema,
		Structured:    evidence,
		RenderedMarkdown: contractsv1.MCPRenderedMarkdown{
			Markdown:  rendered,
			Untrusted: true,
			Truncated: truncated,
		},
	}
	if err := response.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the assembled response failed contract validation"}), nil
	}

	return buildToolResult(response, response.RenderedMarkdown.Markdown)
}
