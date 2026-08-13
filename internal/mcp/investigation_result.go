package mcp

import (
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleInvestigationResult implements the investigation_result tool:
// decode and validate the opaque result_id, fetch the canonical result
// through the hosted retrieval endpoint, and return it whole with a short,
// explicitly untrusted markdown header.
//
// This tool narrows nothing. It exists precisely so a bounded answer can
// stay small: an agent that needs the detail a projection dropped asks for
// it here, rather than every answer being inflated for the rare caller that
// wants everything.
//
// Authorization is re-enforced on the hosted side for this call, exactly as
// it was for the investigation that produced the result. A result_id is a
// handle, never a capability: holding one grants nothing on its own.
func handleInvestigationResult(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var input contractsv1.MCPInvestigationResultRequest
	if err := json.Unmarshal(rawArgs(req), &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigation_result arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigation_result arguments failed schema validation"}), nil
	}

	result, err := boot.Client.InvestigationResult(ctx, input.ResultID)
	if err != nil {
		return toolErrorResult(err), nil
	}

	rendered, truncated := sidecar.RenderInvestigationResultMarkdown(result, renderedMarkdownMaxBytes)
	response := contractsv1.MCPInvestigationResultResponse{
		SchemaVersion: contractsv1.MCPInvestigationResultResponseSchema,
		Structured:    result,
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
