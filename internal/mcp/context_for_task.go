package mcp

import (
	"context"
	"encoding/json"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Safe default packet budget applied whenever the caller omits the
// optional "budget" object, chosen from the middle of the v1 contract's
// allowed ranges (max_items 1-50, max_output_tokens 500-16000,
// max_serialized_bytes 8192-1048576; see
// internal/contracts/v1/mcp_validate.go).
const (
	defaultMaxItems           = 20
	defaultMaxOutputTokens    = 4000
	defaultMaxSerializedBytes = 262144 // 256 KiB
)

// handleContextForTask implements the context_for_task tool: decode and
// validate the MCP request, resolve repository/scope via explicit-request >
// MCP-roots > cwd precedence, call the hosted context-packet endpoint, and
// return both a structured wrapper and a bounded, explicitly untrusted
// markdown rendering. Every returned error is a normal tool failure
// (CallToolResult.IsError), never a Go error that would crash the protocol
// session.
func handleContextForTask(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var input contractsv1.MCPContextForTaskRequest
	if err := json.Unmarshal(rawArgs(req), &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "context_for_task arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "context_for_task arguments failed schema validation"}), nil
	}

	repo, scope, err := resolveScope(ctx, req.Session, input)
	if err != nil {
		return toolErrorResult(err), nil
	}
	// Repository omission is resolvable only via successful local Git
	// workspace discovery (see resolveScope's own doc comment): the hosted
	// ContextPacketRequest contract requires a non-empty repository slug,
	// so an unresolved repository is never a valid hosted request. Catching
	// it here, as a typed validation failure, avoids relying on
	// ContextPacketRequest.Validate()'s own generic, unclassified error
	// surfacing through classify()'s generic "internal" fallback -- and
	// avoids ever constructing a hosted request that cannot succeed.
	if repo.Slug == "" {
		return toolErrorResult(&classifiedError{category: "validation", message: "context_for_task requires either an explicit repository or a discoverable local Git workspace to resolve one"}), nil
	}

	hostedReq := contractsv1.ContextPacketRequest{
		Goal:       input.Goal,
		Repository: repo,
		Scope:      scope,
		Options:    budgetOptions(input.Budget, boot.Capabilities.Limits),
	}

	packet, err := boot.Client.ContextPacket(ctx, hostedReq)
	if err != nil {
		return toolErrorResult(err), nil
	}

	rendered, truncated := sidecar.RenderContextPacketMarkdown(packet, renderedMarkdownMaxBytes)
	response := contractsv1.MCPContextForTaskResponse{
		SchemaVersion: contractsv1.MCPContextForTaskResponseSchema,
		Structured:    packet,
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

// budgetOptions maps an optional MCP budget override onto hosted
// PacketOptions, filling any omitted (zero-value) field with its safe
// default, then clamps every field (default or caller-supplied) to the
// hosted API's own advertised capabilities.limits: local defaults and
// caller requests are both bounded by what the hosted API actually
// grants this credential, never forwarded as an over-limit request the
// hosted side would have to reject anyway.
func budgetOptions(budget *contractsv1.MCPBudget, limits contractsv1.CapabilityLimits) contractsv1.PacketOptions {
	opts := contractsv1.PacketOptions{
		MaxItems:           defaultMaxItems,
		MaxOutputTokens:    defaultMaxOutputTokens,
		MaxSerializedBytes: defaultMaxSerializedBytes,
	}
	if budget != nil {
		if budget.MaxItems != 0 {
			opts.MaxItems = budget.MaxItems
		}
		if budget.MaxOutputTokens != 0 {
			opts.MaxOutputTokens = budget.MaxOutputTokens
		}
		if budget.MaxSerializedBytes != 0 {
			opts.MaxSerializedBytes = budget.MaxSerializedBytes
		}
	}
	opts.MaxItems = clampToHostedLimit(opts.MaxItems, limits.MaxItems)
	opts.MaxOutputTokens = clampToHostedLimit(opts.MaxOutputTokens, limits.MaxOutputTokens)
	opts.MaxSerializedBytes = clampToHostedLimit(opts.MaxSerializedBytes, limits.MaxSerializedBytes)
	return opts
}

// clampToHostedLimit bounds value to the hosted API's advertised
// capability limit for this field. A non-positive hostedLimit (never
// produced by a Capabilities value that passed Capabilities.Validate,
// which requires every limit >= 1, but possible from a hand-built test
// fixture) leaves value unclamped rather than zeroing out every request.
func clampToHostedLimit(value, hostedLimit int) int {
	if hostedLimit > 0 && value > hostedLimit {
		return hostedLimit
	}
	return value
}
