package mcp

import (
	"context"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// unlistedToolMiddleware answers a call to a tool this server has but did not
// list for this caller with a tool result with isError set, so a client shows
// the reason instead of a bare failure. The class is decided from the manifest
// and the caller's listed set before dispatch.
// Two classes keep the protocol error: a name the manifest does not declare,
// and a tool the manifest marks disabled by default (off at server level for
// every caller, so its existence is not disclosed). Every other manifest tool
// is one this server has and this caller was not given.
func unlistedToolMiddleware(caller *CallerContext) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/call" {
				if call, ok := req.(*mcpsdk.CallToolRequest); ok && call.Params != nil {
					if entry, known := lookupManifestEntry(call.Params.Name); known && !entry.DisabledByDefault && !hostedToolEnabled(caller, call.Params.Name) {
						refused := &mcpsdk.CallToolResult{}
						refused.SetError(&classifiedError{category: "entitlement", message: unlistedToolMessage(caller, call.Params.Name)})
						return refused, nil
					}
				}
			}
			return next(ctx, method, req)
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
