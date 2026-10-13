package mcp

import (
	"context"
	"strconv"
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
	if len(input.EvidenceRefIDs) > 0 {
		return sourceEvidenceBatch(ctx, cfg, caller, input)
	}

	evidence, err := resolveSourceEvidence(ctx, cfg, caller, input.EvidenceRefID, input.ResultID)
	if err != nil {
		return toolErrorResult(err), nil
	}

	rendered, truncated := sidecar.RenderEvidenceMarkdown(evidence, renderedMarkdownMaxBytes)
	response := contractsv1.MCPSourceEvidenceResponse{
		SchemaVersion: contractsv1.MCPSourceEvidenceResponseSchema,
		Structured:    &evidence,
		RenderedMarkdown: &contractsv1.MCPRenderedMarkdown{
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

// sourceEvidenceBatchEntryMarkdownMaxBytes bounds one entry's rendering in a
// batch, so a full batch stays near the single-reference bound.
const sourceEvidenceBatchEntryMarkdownMaxBytes = 3000

// sourceEvidenceBatch answers each reference on its own, in request order.
// Every reference goes through the same live authorization as a single call;
// a refused or unknown one is its own refused entry, never a failed batch.
func sourceEvidenceBatch(ctx context.Context, cfg *ProcessConfig, caller *CallerContext, input contractsv1.MCPSourceEvidenceRequest) (*mcpsdk.CallToolResult, error) {
	entries := make([]contractsv1.MCPSourceEvidenceEntry, 0, len(input.EvidenceRefIDs))
	texts := make([]string, 0, len(input.EvidenceRefIDs))
	for _, ref := range input.EvidenceRefIDs {
		evidence, err := resolveSourceEvidence(ctx, cfg, caller, ref, input.ResultID)
		if err != nil {
			ce := classify(err)
			entries = append(entries, contractsv1.MCPSourceEvidenceEntry{EvidenceRefID: ref, Status: contractsv1.MCPSourceEvidenceEntryRefused, Reason: ce.category, Message: ce.message})
			texts = append(texts, "Evidence "+strconv.Quote(ref)+": refused ("+ce.category+"): "+ce.message)
			continue
		}
		rendered, truncated := sidecar.RenderEvidenceMarkdown(evidence, sourceEvidenceBatchEntryMarkdownMaxBytes)
		entries = append(entries, contractsv1.MCPSourceEvidenceEntry{
			EvidenceRefID:    ref,
			Status:           contractsv1.MCPSourceEvidenceEntryServed,
			Structured:       &evidence,
			RenderedMarkdown: &contractsv1.MCPRenderedMarkdown{Markdown: rendered, Untrusted: true, Truncated: truncated},
		})
		texts = append(texts, rendered)
	}
	response := contractsv1.MCPSourceEvidenceResponse{SchemaVersion: contractsv1.MCPSourceEvidenceResponseSchema, Entries: entries}
	if err := response.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the assembled response failed contract validation"}), nil
	}
	return buildToolResult(response, strings.Join(texts, "\n\n---\n\n"))
}

// resolveSourceEvidence expands one reference for the caller.
func resolveSourceEvidence(ctx context.Context, cfg *ProcessConfig, caller *CallerContext, refID, resultID string) (contractsv1.ExpandedEvidence, error) {
	// CHAOS-6563: a Context Fabric ref (acr:v1:...) is keyed by its subject and
	// many results cite it, so it expands only in the scope of the answer that
	// returned it. An unscoped one is refused here, by name, rather than
	// resolving to the citation of some other result.
	if strings.HasPrefix(refID, contractsv1.ContextFabricEvidenceRefPrefix) && strings.TrimSpace(resultID) == "" && !contractsv1.RowKeyedEvidenceRef(refID) {
		return contractsv1.ExpandedEvidence{}, &classifiedError{category: "validation", message: "evidence_ref_unscoped: this evidence reference names its subject, not its answer; pass the result_id of the investigate_question answer that returned it as result_id"}
	}

	var evidence contractsv1.ExpandedEvidence
	if strings.HasPrefix(refID, localEvidencePrefix) {
		routeHosted := caller.hostedRoutes != nil && caller.hostedRoutes.has(refID)
		if !routeHosted && cfg.local != nil && caller.localCache != nil {
			cached, found := caller.localCache.get(refID)
			if found {
				evidence = contractsv1.ExpandedEvidence{SchemaVersion: contractsv1.ExpandedEvidenceSchema, Evidence: cached.ref, ResolvedAt: cfg.local.clock().UTC(), Availability: cached.ref.Availability, Excerpt: boundedText(cached.evidence.Excerpt, 1000), Structured: map[string]any{}}
			}
		}
		if evidence.SchemaVersion == "" && !routeHosted {
			return contractsv1.ExpandedEvidence{}, &classifiedError{category: "no_data", message: "local evidence is unavailable"}
		}
	}
	if evidence.SchemaVersion == "" {
		return caller.client.EvidenceInResult(ctx, refID, resultID)
	}
	return evidence, nil
}
