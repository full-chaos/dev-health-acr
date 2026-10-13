package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// unlistedToolMiddleware turns the SDK's protocol error for a call to a tool
// this server knows but did not register for this caller into a tool result
// with isError set, so a client shows the reason instead of a bare failure.
// A name the manifest does not declare keeps the protocol error, and so does
// the write tool, which is off by default and is not a read-side listing.
func unlistedToolMiddleware(caller *CallerContext) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			result, err := next(ctx, method, req)
			if err == nil || method != "tools/call" {
				return result, err
			}
			call, ok := req.(*mcpsdk.CallToolRequest)
			if !ok || call.Params == nil {
				return result, err
			}
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams || !strings.HasPrefix(rpcErr.Message, "unknown tool") {
				return result, err
			}
			if call.Params.Name == toolRecordEpisode || !slices.Contains(manifestToolNames(), call.Params.Name) {
				return result, err
			}
			refused := &mcpsdk.CallToolResult{}
			refused.SetError(&classifiedError{category: "entitlement", message: unlistedToolMessage(caller, call.Params.Name)})
			return refused, nil
		}
	}
}

// unlistedToolMessage states the reason only from what the caller's own
// capability snapshot shows, then names the tools that are listed for it.
func unlistedToolMessage(caller *CallerContext, tool string) string {
	caps := caller.Capabilities()
	var b strings.Builder
	b.WriteString(tool)
	switch {
	case !caps.Entitlements.AgentContextRuntime:
		b.WriteString(" is not available: this organization is not entitled to the agent context runtime.")
	case !caps.Permissions.ContextRead && tool != toolRecordEpisode && tool != toolRunOperation && tool != toolGraphQLQuery:
		b.WriteString(" is not available: this credential lacks the context:read scope.")
	case tool == toolInvestigateQuestion && hostedToolEnabled(caller, toolInvestigateWithInterpretation):
		b.WriteString(" is not enabled for this organization: it runs on the service's model, and the organization has neither that in its plan nor a model configuration of its own.")
	default:
		b.WriteString(" is not listed for this credential on this server.")
	}
	var listed []string
	for _, name := range []string{toolDataCatalog, toolFindSubjects, toolReadFacts, toolReadRelationships, toolRunOperation, toolGraphQLQuery, toolInvestigateWithInterpretation} {
		if hostedToolEnabled(caller, name) {
			listed = append(listed, name)
		}
	}
	if len(listed) > 0 {
		b.WriteString(" Listed for you: " + strings.Join(listed, ", ") + ".")
		if hostedToolEnabled(caller, toolInvestigateWithInterpretation) {
			b.WriteString(" For a narrative answer use " + toolInvestigateWithInterpretation + " with synthesis \"client\".")
		}
	}
	return b.String()
}
