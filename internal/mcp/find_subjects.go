package mcp

import (
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleFindSubjects implements the find_subjects tool: list subjects of one
// kind, or find them by exact name, through the hosted lookup. Every subject
// in the answer passed the hosted subject gate for the caller's credential
// in this call. The API JSON is the structured content, unchanged. No
// interpreter, synthesizer or embedding model is involved.
func handleFindSubjects(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolFindSubjects), nil
	}
	var input contractsv1.MCPFindSubjectsRequest
	if err := json.Unmarshal(rawArgs(req), &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "find_subjects arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "find_subjects arguments failed schema validation"}), nil
	}
	raw, err := caller.client.FindSubjects(ctx, input)
	if err != nil {
		return toolErrorResult(err), nil
	}
	return rawToolResult(raw, sidecar.RenderFindSubjectsSummary(raw, sidecar.DataTextMaxBytes)), nil
}
