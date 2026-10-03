package mcp

import (
	"bytes"
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleInvestigateWithInterpretation implements the
// investigate_with_interpretation tool: investigate_question with an
// interpretation the client ran on its own model. The arguments are decoded
// strictly, validated, and mapped onto the hosted investigation request by
// the same code investigate_question uses; the supplied interpretation rides
// in the one hosted field that differs.
func handleInvestigateWithInterpretation(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolInvestigateWithInterpretation), nil
	}

	args, refused := normalizedInvestigationArgs(ctx, cfg, req, toolInvestigateWithInterpretation)
	if refused != nil {
		return refused, nil
	}
	var input contractsv1.MCPInvestigateWithInterpretationRequest
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_with_interpretation arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_with_interpretation arguments failed schema validation"}), nil
	}

	supplied := input.Supplied()
	return investigateAndRender(ctx, cfg, caller, toolInvestigateWithInterpretation, input.MCPInvestigateQuestionRequest, &supplied)
}
