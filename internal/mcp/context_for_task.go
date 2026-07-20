package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

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

var validateFederatedResponse = func(response contractsv1.MCPContextForTaskResponse) error {
	return response.Validate()
}

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

	resolved, err := resolveTaskScope(ctx, req.Session, input)
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
	if resolved.Repository.Slug == "" {
		return toolErrorResult(&classifiedError{category: "validation", message: "context_for_task requires either an explicit repository or a discoverable local Git workspace to resolve one"}), nil
	}

	options := budgetOptions(input.Budget, input.RequestedCategories, boot.Capabilities.Limits)
	hostedOptions := options
	var local mappedLocalBundle
	var bundle sidecar.LocalEvidenceBundle
	localSucceeded := false
	if boot.local != nil && resolved.LocalEligible && boot.local.eligible(resolved.Workspace) {
		var localErr error
		bundle, localErr = boot.local.bundle(ctx, resolved, input, options)
		if localErr != nil && ctx.Err() != nil {
			return toolErrorResult(ctx.Err()), nil
		}
		if localErr == nil {
			if localErr = validateDistinctLocalEvidence(bundle); localErr == nil {
				localSucceeded = true
				reserve := localReservation(boot.local.config, options)
				hostedOptions.MaxItems -= reserve.MaxItems
				hostedOptions.MaxOutputTokens -= reserve.MaxOutputTokens
				hostedOptions.MaxSerializedBytes -= reserve.MaxSerializedBytes
			}
		}
	}

	hostedReq := contractsv1.ContextPacketRequest{
		Goal:       input.Goal,
		Repository: resolved.Repository,
		Scope:      resolved.Scope,
		Options:    hostedOptions,
	}

	packet, err := boot.Client.ContextPacket(ctx, hostedReq)
	if err != nil {
		return toolErrorResult(err), nil
	}
	if localSucceeded {
		local, err = boot.local.mapLocalBundle(resolved.Repository.Slug, bundle, occupiedPacketIDs(packet))
		if err != nil {
			return toolErrorResult(&classifiedError{category: "internal", message: "local federation finalization failed"}), nil
		}
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
	if localSucceeded {
		trimmed := local.trimTo(options.MaxItems-packet.Budget.ItemsUsed, options.MaxOutputTokens-packet.Budget.EstimatedTokens, options.MaxSerializedBytes-packet.Budget.SerializedBytes)
		context := localContext(local.bundle, local, trimmed)
		response.LocalContext = &context
		localTokens := localEstimatedTokens(local.evidence)
		localBytes := localJSONBytes(local.items, local.refs)
		response.FederatedBudget = &contractsv1.MCPFederatedBudget{
			MaxItems: options.MaxItems, MaxOutputTokens: options.MaxOutputTokens, MaxSerializedBytes: options.MaxSerializedBytes,
			HostedItemsUsed: packet.Budget.ItemsUsed, LocalItemsUsed: len(local.items), TotalItemsUsed: packet.Budget.ItemsUsed + len(local.items),
			HostedEstimatedTokens: packet.Budget.EstimatedTokens, LocalEstimatedTokens: localTokens, TotalEstimatedTokens: packet.Budget.EstimatedTokens + localTokens,
			HostedSerializedBytes: packet.Budget.SerializedBytes, LocalSerializedBytes: localBytes, TotalSerializedBytes: packet.Budget.SerializedBytes + localBytes,
			HostedTruncated: packet.Budget.Truncated, LocalTruncated: local.bundle.Truncated || trimmed, Truncated: packet.Budget.Truncated || local.bundle.Truncated || trimmed,
		}
	}
	if err := validateFederatedResponse(response); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the assembled response failed contract validation"}), nil
	}
	result, buildErr := buildToolResult(response, response.RenderedMarkdown.Markdown)
	if buildErr != nil {
		return result, buildErr
	}
	if boot.hostedRoutes != nil {
		for id := range occupiedPacketIDs(packet) {
			if strings.HasPrefix(id, localEvidencePrefix) {
				boot.hostedRoutes.put(id)
			}
		}
	}
	if localSucceeded && len(local.refs) > 0 {
		boot.local.cache.putBatch(cacheEntries(local))
	}
	return result, nil
}

func occupiedPacketIDs(packet contractsv1.ContextPacket) map[string]struct{} {
	encoded, err := json.Marshal(packet)
	if err != nil {
		return map[string]struct{}{}
	}
	var value any
	if json.Unmarshal(encoded, &value) != nil {
		return map[string]struct{}{}
	}
	ids := map[string]struct{}{}
	collectPacketIDs(value, ids)
	return ids
}

func collectPacketIDs(value any, ids map[string]struct{}) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "request_id" || key == "context_packet_id" || key == "evidence_ref_id" || key == "packet_item_id" || key == "check_id" || key == "step_id" {
				if id, ok := child.(string); ok {
					ids[id] = struct{}{}
				}
			}
			collectPacketIDs(child, ids)
		}
	case []any:
		for _, child := range node {
			collectPacketIDs(child, ids)
		}
	}
}

func appendDistinctWarning(warnings []string, warning string) []string {
	if slices.Contains(warnings, warning) {
		return warnings
	}
	return append(warnings, warning)
}

func appendDistinctWarnings(warnings []string, additions []string) []string {
	for _, warning := range additions {
		warnings = appendDistinctWarning(warnings, warning)
	}
	return warnings
}

func cacheEntries(mapped mappedLocalBundle) []cachedLocalEvidence {
	entries := make([]cachedLocalEvidence, 0, len(mapped.refs))
	for index := range mapped.refs {
		entries = append(entries, cachedLocalEvidence{evidence: mapped.evidence[index], ref: mapped.refs[index]})
	}
	return entries
}

// budgetOptions maps an optional MCP budget override onto hosted
// PacketOptions, filling any omitted (zero-value) field with its safe
// default, then clamps every field (default or caller-supplied) to the
// hosted API's own advertised capabilities.limits: local defaults and
// caller requests are both bounded by what the hosted API actually
// grants this credential, never forwarded as an over-limit request the
// hosted side would have to reject anyway.
func budgetOptions(budget *contractsv1.MCPBudget, requestedCategories []contractsv1.PacketCategory, limits contractsv1.CapabilityLimits) contractsv1.PacketOptions {
	opts := contractsv1.PacketOptions{
		RequestedCategories: append([]contractsv1.PacketCategory(nil), requestedCategories...),
		MaxItems:            defaultMaxItems,
		MaxOutputTokens:     defaultMaxOutputTokens,
		MaxSerializedBytes:  defaultMaxSerializedBytes,
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
