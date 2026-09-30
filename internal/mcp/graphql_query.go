package mcp

import (
	"bytes"
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleGraphQLQuery implements the graphql_query tool (CHAOS-7075): one
// validated free-form query over the allowed schema, run through the hosted
// data route with the caller's own bearer. Same rules as run_operation: the
// route needs data:read (a credential without it gets a typed "entitlement"
// tool error), and a refusal, an unavailable query class or an upstream
// error is an ANSWER returned unchanged. Numbers in the variables keep
// their exact text (UseNumber). No model is called.
func handleGraphQLQuery(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolGraphQLQuery), nil
	}
	var input contractsv1.MCPGraphQLQueryRequest
	decoder := json.NewDecoder(bytes.NewReader(rawArgs(req)))
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "graphql_query arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "graphql_query arguments failed schema validation"}), nil
	}
	raw, err := caller.client.GraphQLQuery(ctx, input)
	if err != nil {
		return toolErrorResult(err), nil
	}
	return rawToolResult(raw, sidecar.RenderGraphQLSummary(raw, sidecar.DataTextMaxBytes)), nil
}
