package mcp

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handleSourceEvidence implements the source_evidence tool: decode and
// validate the evidence_ref_id, call the hosted evidence endpoint, and
// return both a structured wrapper and a bounded, explicitly untrusted
// markdown rendering of the citation/excerpt.
func handleSourceEvidence(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolSourceEvidence), nil
	}

	var input contractsv1.MCPSourceEvidenceRequest
	if err := decodeToolArguments(rawArgs(req), &input, false); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: toolArgumentsMessage(toolSourceEvidence, err)}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "source_evidence arguments failed schema validation"}), nil
	}

	// CHAOS-6563: a Context Fabric ref (acr:v1:...) is keyed by its subject and
	// many results cite it, so it expands only in the scope of the answer that
	// returned it. An unscoped one is refused here, by name, rather than
	// resolving to the citation of some other result.
	if strings.HasPrefix(input.EvidenceRefID, contractsv1.ContextFabricEvidenceRefPrefix) && strings.TrimSpace(input.ResultID) == "" {
		return toolErrorResult(&classifiedError{category: "validation", message: "evidence_ref_unscoped: this evidence reference names its subject, not its answer; pass the result_id of the investigate_question answer that returned it as result_id"}), nil
	}

	var evidence contractsv1.ExpandedEvidence
	if strings.HasPrefix(input.EvidenceRefID, localEvidencePrefix) {
		routeHosted := caller.hostedRoutes != nil && caller.hostedRoutes.has(input.EvidenceRefID)
		if !routeHosted && cfg.local != nil && caller.localCache != nil {
			cached, found := caller.localCache.get(input.EvidenceRefID)
			if found {
				evidence = contractsv1.ExpandedEvidence{SchemaVersion: contractsv1.ExpandedEvidenceSchema, Evidence: cached.ref, ResolvedAt: cfg.local.clock().UTC(), Availability: cached.ref.Availability, Excerpt: boundedText(cached.evidence.Excerpt, 1000), Structured: map[string]any{}}
			}
		}
		if evidence.SchemaVersion == "" && !routeHosted {
			return toolErrorResult(&classifiedError{category: "no_data", message: "local evidence is unavailable"}), nil
		}
	}
	if evidence.SchemaVersion == "" {
		var err error
		evidence, err = caller.client.EvidenceInResult(ctx, input.EvidenceRefID, input.ResultID)
		if err != nil {
			return toolErrorResult(err), nil
		}
	}

	rendered, truncated := sidecar.RenderEvidenceMarkdown(evidence, renderedMarkdownMaxBytes)
	response := contractsv1.MCPSourceEvidenceResponse{
		SchemaVersion: contractsv1.MCPSourceEvidenceResponseSchema,
		Structured:    evidence,
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
