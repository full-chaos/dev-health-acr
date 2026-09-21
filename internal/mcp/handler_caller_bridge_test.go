package mcp

import (
	"context"
	"io"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The helpers below drive a tool handler the way a transport does: split a
// boot bundle into its process and caller halves, bind the caller to the
// request context, and call the handler. They add no behaviour of their
// own -- in particular they never bypass CallerFromContext, so a handler
// that stopped reading its identity from the context would fail here the
// same way it fails in production.

// bootHandlerHalvesConfig returns the two halves a boot bundle splits into,
// for the helpers that take them directly rather than through a request
// context.
func bootHandlerHalvesConfig(boot *Bootstrap) (*ProcessConfig, *CallerContext) {
	return boot.split(io.Discard)
}

func callerContextFor(ctx context.Context, boot *Bootstrap) (*ProcessConfig, context.Context) {
	cfg, caller := boot.split(io.Discard)
	return cfg, ContextWithCaller(ctx, caller)
}

func invokeContextForTask(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	cfg, callerCtx := callerContextFor(ctx, boot)
	return handleContextForTask(callerCtx, cfg, req)
}

func invokeSourceEvidence(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	cfg, callerCtx := callerContextFor(ctx, boot)
	return handleSourceEvidence(callerCtx, cfg, req)
}

func invokeInvestigateQuestion(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	cfg, callerCtx := callerContextFor(ctx, boot)
	return handleInvestigateQuestion(callerCtx, cfg, req)
}

func invokeInvestigationResult(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	cfg, callerCtx := callerContextFor(ctx, boot)
	return handleInvestigationResult(callerCtx, cfg, req)
}

func invokeRecordEpisode(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	cfg, callerCtx := callerContextFor(ctx, boot)
	return handleRecordEpisode(callerCtx, cfg, req)
}
