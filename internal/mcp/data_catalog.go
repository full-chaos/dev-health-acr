package mcp

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleDataCatalog implements the data_catalog tool: decode the optional
// sections, read the hosted catalogue for THIS caller's credential, and
// return the API JSON verbatim as structured content with a bounded,
// untrusted-marked text summary. It calls no model and reads no local state.
func handleDataCatalog(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolDataCatalog), nil
	}
	var input contractsv1.MCPDataCatalogRequest
	if err := decodeToolArguments(rawArgs(req), &input, false); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: toolArgumentsMessage(toolDataCatalog, err)}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "data_catalog arguments failed schema validation"}), nil
	}
	raw, err := caller.client.DataCatalog(ctx, input.Sections)
	if err != nil {
		return toolErrorResult(err), nil
	}
	return rawToolResult(raw, sidecar.RenderDataCatalogSummary(raw, sidecar.DataTextMaxBytes)), nil
}
