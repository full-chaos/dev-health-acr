package mcp

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleRunOperation implements the run_operation tool: one allowlisted
// operation and its variables, run through the hosted data route with the
// caller's own bearer. The route needs the data:read scope; a credential
// without it gets a typed "entitlement" tool error (the hosted 403
// insufficient_scope), never a raw body.
//
// A policy refusal, an unavailable operation and an upstream error are
// ANSWERS (call: refused | operation_unavailable | upstream_error |
// upstream_timeout), not tool failures: the API JSON is returned unchanged
// so the model reads call, completeness and result itself. Numbers in the
// variables keep their exact text (UseNumber).
func handleRunOperation(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolRunOperation), nil
	}
	var input contractsv1.MCPRunOperationRequest
	if err := decodeToolArguments(rawArgs(req), &input, true); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: toolArgumentsMessage(toolRunOperation, err)}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "run_operation arguments failed schema validation"}), nil
	}
	raw, err := caller.client.RunOperation(ctx, input)
	if err != nil {
		return toolErrorResult(err), nil
	}
	return rawToolResult(raw, sidecar.RenderOperationSummary(raw, sidecar.DataTextMaxBytes)), nil
}
