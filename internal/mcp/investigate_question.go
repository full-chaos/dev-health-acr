package mcp

import (
	"context"
	"encoding/json"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/answerprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Agent-appropriate answer defaults, applied whenever the caller omits the
// optional "budget" object. They are deliberately smaller than the hosted
// contract maxima and smaller than a Workbench would use: an agent reads an
// answer into a bounded context window, and a caller who genuinely wants
// everything should fetch the canonical result by ID instead of inflating
// every answer.
const (
	defaultMaxDrivers                = 5
	defaultMaxCohortMembers          = 20
	defaultMaxAnswerEvidenceRefs     = 25
	defaultAnswerMaxSerializedBytes  = 65536
	defaultMaxSubjectCandidates      = 10
	defaultMaxRelationshipPaths      = 25
	investigationRenderedMarkdownMax = renderedMarkdownMaxBytes
)

// handleInvestigateQuestion implements the investigate_question tool:
// decode and validate the arguments, map them onto the hosted investigation
// contract with safe defaults, call the SAME hosted investigation service
// the API serves, and return the shared bounded projection plus a bounded,
// explicitly untrusted markdown rendering.
//
// The narrowing is done by answerprojection.Project -- the identical
// function the hosted API side uses -- so MCP and the API cannot disagree
// about what the answer says. This handler never summarises anything
// itself.
//
// Every returned error is a normal tool failure (CallToolResult.IsError),
// never a Go error that would tear down the protocol session.
func handleInvestigateQuestion(ctx context.Context, boot *Bootstrap, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := json.Unmarshal(rawArgs(req), &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_question arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_question arguments failed schema validation"}), nil
	}

	budget := answerBudget(input.Budget, boot.Capabilities.Limits)
	hosted := contractsv1.ContextFabricInvestigationRequest{
		Question:             input.Question,
		Conversation:         input.Conversation,
		PriorSubjectReceipts: input.PriorSubjectReceipts,
		// PriorKindReceipts/PriorAnchorReceipts/PriorHandleReceipts/
		// PriorWindowReceipts and ExpectedKinds/SubjectHandles (CHAOS-3972
		// P3+W2) map straight through to the hosted contract's own fields
		// of the same name -- no translation needed, this tool's own
		// shape mirrors the hosted one exactly for these. Per DP12(b),
		// receipts are this surface's SOLE decisive transport for every
		// intent-frame member; the explicit fields enter at
		// inferred_default/explicit_unattributed (sidecar.Client.Investigate
		// stamps Consumer.Surface="mcp" below, which is what
		// structureExplicitAuthority/windowExplicitProvenance key on).
		PriorKindReceipts:   input.PriorKindReceipts,
		PriorAnchorReceipts: input.PriorAnchorReceipts,
		PriorHandleReceipts: input.PriorHandleReceipts,
		PriorWindowReceipts: input.PriorWindowReceipts,
		ExpectedKinds:       input.ExpectedKinds,
		SubjectHandles:      input.SubjectHandles,
		// Fixed rather than caller-driven because the tool schema
		// deliberately exposes no axis field -- a SURFACE decision, not a
		// capability one. CHAOS-3781 made all three historical axes
		// answerable (the engine, the providers and the route all stopped
		// refusing; only ErrInvalidTimeBound remains, for a future instant
		// or an over-wide range), so pinning current here is this tool
		// choosing its scope, not reporting a limit. See
		// contractsv1.MCPInvestigateQuestionRequest for what adding the
		// field would take.
		//
		// EvidenceWindow (CHAOS-3900 W2) is legal only on this fixed
		// current axis, which this tool always sends -- no conflict is
		// possible from this mapping.
		TimeContext: contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent, EvidenceWindow: input.EvidenceWindow},
		Options:     hostedOptions(budget, input.AllowClarification, input.WindowConfirmationMode),
	}
	if input.Scope != nil {
		hosted.RequestedScope = contractsv1.ContextFabricRequestedScope{
			RepositorySlugs: input.Scope.RepositorySlugs,
			ProjectIDs:      input.Scope.ProjectIDs,
			TeamIDs:         input.Scope.TeamIDs,
		}
	}

	result, err := boot.Client.Investigate(ctx, hosted)
	if err != nil {
		return toolErrorResult(err), nil
	}

	projection := answerprojection.Project(result, answerprojection.Budget{
		MaxDrivers:       budget.MaxDrivers,
		MaxCohortMembers: budget.MaxCohortMembers,
		MaxEvidenceRefs:  budget.MaxEvidenceRefs,
	})
	response := contractsv1.MCPInvestigateQuestionResponse{
		SchemaVersion: contractsv1.MCPInvestigateQuestionResponseSchema,
		Structured:    projection,
		// The structured payload carries the same untrusted signal the
		// markdown rendering does, machine-readably. A consumer reading
		// Structured must not have to infer safety from the absence of a
		// warning it only ever saw in prose.
		UntrustedContent: contractsv1.MCPUntrustedContent{
			Untrusted: true,
			Notice:    contractsv1.MCPUntrustedContentNotice,
			Fields:    contractsv1.MCPInvestigateQuestionUntrustedFields,
		},
	}
	if input.IncludeFullResult {
		attachFullResult(&response, result, budget.MaxSerializedBytes)
	}

	rendered, truncated := sidecar.RenderAnswerProjectionMarkdown(response.Structured, investigationRenderedMarkdownMax)
	response.RenderedMarkdown = contractsv1.MCPRenderedMarkdown{
		Markdown:  rendered,
		Untrusted: true,
		Truncated: truncated,
	}
	if err := response.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the assembled response failed contract validation"}), nil
	}
	return buildToolResult(response, response.RenderedMarkdown.Markdown)
}

// attachFullResult honors include_full_result within the byte budget.
//
// The budget bounds the TOTAL structured content. When the projection plus
// the canonical result would exceed it, the RESULT is dropped whole and the
// drop is declared through MarkFullResultOmitted -- never truncated into a
// partial document. Failing this way keeps every emitted payload a valid,
// complete contract: the projection still answers the question, and the
// caller still holds result_id to fetch the rest through
// investigation_result. A truncated JSON body would be worse than a missing
// one, because a consumer cannot tell a cut-off answer from a short one.
func attachFullResult(response *contractsv1.MCPInvestigateQuestionResponse, result contractsv1.ContextFabricInvestigationResult, maxSerializedBytes int) {
	candidate := *response
	candidate.FullResult = &result
	encoded, err := json.Marshal(candidate)
	if err != nil || len(encoded) > maxSerializedBytes {
		answerprojection.MarkFullResultOmitted(&response.Structured)
		return
	}
	response.FullResult = &result
}

// answerBudget fills every omitted field with its agent-appropriate
// default, then clamps to what the hosted API advertises for this
// credential. Both a caller's request and this sidecar's own defaults are
// bounded by what the service actually grants, so an over-limit request is
// never forwarded only to be rejected.
func answerBudget(requested *contractsv1.MCPInvestigationBudget, limits contractsv1.CapabilityLimits) contractsv1.MCPInvestigationBudget {
	budget := contractsv1.MCPInvestigationBudget{
		MaxDrivers:         defaultMaxDrivers,
		MaxCohortMembers:   defaultMaxCohortMembers,
		MaxEvidenceRefs:    defaultMaxAnswerEvidenceRefs,
		MaxSerializedBytes: defaultAnswerMaxSerializedBytes,
	}
	if requested != nil {
		if requested.MaxDrivers != 0 {
			budget.MaxDrivers = requested.MaxDrivers
		}
		if requested.MaxCohortMembers != 0 {
			budget.MaxCohortMembers = requested.MaxCohortMembers
		}
		if requested.MaxEvidenceRefs != 0 {
			budget.MaxEvidenceRefs = requested.MaxEvidenceRefs
		}
		if requested.MaxSerializedBytes != 0 {
			budget.MaxSerializedBytes = requested.MaxSerializedBytes
		}
	}
	budget.MaxSerializedBytes = clampToHostedLimit(budget.MaxSerializedBytes, limits.MaxSerializedBytes)
	return budget
}

// hostedOptions maps the MCP budget onto the hosted investigation options.
//
// The hosted contract requires every option field, so the ones MCP does not
// expose take fixed safe values rather than being left zero (which the
// hosted validator would reject).
func hostedOptions(budget contractsv1.MCPInvestigationBudget, allowClarification *bool, windowConfirmationMode contractsv1.ContextFabricWindowConfirmationMode) contractsv1.ContextFabricInvestigationOptions {
	// Clarification is allowed unless the caller explicitly opted out. An
	// agent that cannot ask its user a follow-up may prefer a
	// best-effort answer to a question it cannot relay.
	allow := true
	if allowClarification != nil {
		allow = *allowClarification
	}
	return contractsv1.ContextFabricInvestigationOptions{
		MaxSubjectCandidates: defaultMaxSubjectCandidates,
		MaxCohortMembers:     budget.MaxCohortMembers,
		MaxRelationshipPaths: defaultMaxRelationshipPaths,
		MaxDrivers:           budget.MaxDrivers,
		MaxEvidenceRefs:      budget.MaxEvidenceRefs,
		MaxSerializedBytes:   budget.MaxSerializedBytes,
		AllowClarification:   allow,
		IncludeDebug:         false,
		// WindowConfirmationMode (CHAOS-3900 W2) maps straight through --
		// empty means the DW3-ruled headless default (the caller's own
		// omitted-field state, mapped, not the tool choosing a mode).
		WindowConfirmationMode: windowConfirmationMode,
	}
}
