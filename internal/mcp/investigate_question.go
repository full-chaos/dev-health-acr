package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
	defaultMaxDrivers               = 5
	defaultMaxCohortMembers         = 20
	defaultMaxAnswerEvidenceRefs    = 25
	defaultAnswerMaxSerializedBytes = 65536
	// defaultMaxSubjectCandidates (CHAOS-4117): raised 10 -> 20, the
	// measured SAFE ceiling. At 10, subject search truncated on 90/90
	// decisive resolution arms in the CHAOS-4117 root-cause run, and
	// resolution.go's searchTruncated gate sits ABOVE lone_floor/
	// top_of_two, so truncation alone made both statistical commit gates
	// structurally dead. 20 is not an arbitrary widening: it is pinned to
	// falkorgraph.RetrievalPolicy.CalibratedTopK (retrieval_policy.go),
	// the depth CHAOS-3829's vector_margin_rescue margin M was calibrated
	// at -- resolve.go's effectiveSearchLimit/CalibratedTopK envelope
	// check requires the per-call search depth to stay <= CalibratedTopK
	// for that rescue to remain eligible, so raising past 20 would
	// silently disable it. Going beyond 20 needs a fresh tau/margin
	// re-calibration, not a constant edit.
	defaultMaxSubjectCandidates      = 20
	defaultMaxRelationshipPaths      = 25
	investigationRenderedMarkdownMax = renderedMarkdownMaxBytes
)

// writeBackNotHereMessage is the fixed refusal of a write-back argument sent to
// the tool that cannot take it.
const writeBackNotHereMessage = "investigate_question does not take synthesis_output or synthesis_contract; send the draft with investigate_with_interpretation, together with your interpretation"

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
func handleInvestigateQuestion(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	caller, callerErr := CallerFromContext(ctx)
	if callerErr != nil {
		return refuseWithoutCaller(ctx, cfg, toolInvestigateQuestion), nil
	}

	args, refused := normalizedInvestigationArgs(ctx, cfg, req, toolInvestigateQuestion)
	if refused != nil {
		return refused, nil
	}
	if contractsv1.MCPArgumentsCarrySynthesisWriteBack(args) {
		return toolErrorResult(&classifiedError{category: "validation", message: writeBackNotHereMessage}), nil
	}
	var input contractsv1.MCPInvestigateQuestionRequest
	if err := json.Unmarshal(args, &input); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_question arguments are not valid JSON for the declared schema"}), nil
	}
	if err := input.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "validation", message: "investigate_question arguments failed schema validation"}), nil
	}
	return investigateAndRender(ctx, cfg, caller, toolInvestigateQuestion, input, nil, nil)
}

// normalizedInvestigationArgs expands bare receipt ids in the raw arguments
// of an investigation tool. A refusal comes back as a ready tool result.
func normalizedInvestigationArgs(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest, surface string) ([]byte, *mcpsdk.CallToolResult) {
	args, receiptForm, normalizeErr := expandBareReceiptIDs(rawArgs(req), surface)
	if normalizeErr != nil {
		// Decision basis for the refusal: the field is one of the six fixed
		// names, never caller text, and no receipt id is logged.
		if cfg.diagnostics != nil {
			cfg.diagnostics.WarnContext(ctx, surface+" bare receipt refused", "reason", "parent_result_id_missing", "field", receiptForm.RefusedField, "surface", surface)
		}
		return nil, toolErrorResult(&classifiedError{category: "validation", message: normalizeErr.Error()})
	}
	if receiptForm.Bare > 0 && cfg.diagnostics != nil {
		// Decision basis for the normalization: how many receipts arrived bare
		// and were bound to parent_result_id versus already carried their own
		// result_id. Counts and a closed field list only; never ids.
		cfg.diagnostics.InfoContext(ctx, surface+" bare receipts expanded", "bare_receipts", receiptForm.Bare, "object_receipts", receiptForm.Object, "receipt_fields", receiptForm.Fields, "parent_bound", true, "surface", surface)
	}
	return args, nil
}

// clientSynthesisFlowLine is the fixed line a client synthesis answer adds to
// the rendered markdown. No value from the synthesis input is put in it.
const clientSynthesisFlowLine = "\n\nThe service wrote no answer text on this turn. Fetch the prompt " + promptSynthesizeAnswer +
	" (its schema is the resource " + uriSynthesisOutput + "), run it as the system message on your own model, and send synthesis_input.input as the user message.\n"

// investigateAndRender maps a validated request onto the hosted contract,
// calls the hosted investigation and renders the shared bounded projection.
// A non-nil supplied interpretation and supplied synthesis are the only
// differences between the tools that call it.
func investigateAndRender(ctx context.Context, cfg *ProcessConfig, caller *CallerContext, surface string, input contractsv1.MCPInvestigateQuestionRequest, supplied *contractsv1.ContextFabricSuppliedInterpretation, suppliedSynthesis *contractsv1.ContextFabricSuppliedSynthesis) (*mcpsdk.CallToolResult, error) {
	budget := answerBudget(input.Budget, caller.Capabilities().Limits)
	hosted := hostedInvestigationRequest(input, budget)
	hosted.SuppliedInterpretation = supplied
	hosted.SuppliedSynthesis = suppliedSynthesis

	hostedResponse, currentRequestID, err := caller.client.InvestigateWithSynthesisInput(ctx, hosted)
	if err != nil {
		return writeBackAwareErrorResult(err), nil
	}
	result := hostedResponse.ContextFabricInvestigationResult

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

	response.SynthesisInput = hostedResponse.SynthesisInput
	markdownMax := investigationRenderedMarkdownMax
	if response.SynthesisInput != nil {
		markdownMax -= len(clientSynthesisFlowLine)
	}
	rendered, truncated := sidecar.RenderAnswerProjectionMarkdown(response.Structured, markdownMax)
	if response.SynthesisInput != nil {
		rendered += clientSynthesisFlowLine
	}
	response.RenderedMarkdown = contractsv1.MCPRenderedMarkdown{
		Markdown:  rendered,
		Untrusted: true,
		Truncated: truncated,
	}
	if err := response.Validate(); err != nil {
		return toolErrorResult(&classifiedError{category: "internal", message: "the assembled response failed contract validation"}), nil
	}
	cfg.metrics.Answer(ctx, string(result.Status), surface)
	trace.SpanFromContext(ctx).SetAttributes(attribute.String(SpanAttributeQueryVersion, result.Versions.QueryVersion))
	if cfg.diagnostics != nil {
		args := answerprojection.DisplayLogArgs(result, response.Structured, answerprojection.Budget{MaxDrivers: budget.MaxDrivers, MaxCohortMembers: budget.MaxCohortMembers, MaxEvidenceRefs: budget.MaxEvidenceRefs}, true, truncated)
		args = append(args, "request_id", currentRequestID, "surface", surface)
		cfg.diagnostics.InfoContext(ctx, "context fabric answer display", args...)
	}
	return buildToolResult(response, response.RenderedMarkdown.Markdown)
}

// hostedInvestigationRequest maps the MCP investigation arguments onto the
// hosted investigation contract.
func hostedInvestigationRequest(input contractsv1.MCPInvestigateQuestionRequest, budget contractsv1.MCPInvestigationBudget) contractsv1.ContextFabricInvestigationRequest {
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
		// PriorCandidateReceipts (CHAOS-4012) is the candr_ twin of the four
		// fields above -- same straight-through mapping, no translation.
		// codex xhigh review (2026-08-23) caught this mapping missing on
		// first pass: the wire schema and MCPInvestigateQuestionRequest both
		// carried prior_candidate_receipts, but this handler still only
		// mapped the four older fields, so a valid candr_ receipt reached
		// here and was silently dropped rather than forwarded.
		PriorCandidateReceipts: input.PriorCandidateReceipts,
		// ParentResultID: the same straight-through mapping, and the SECOND
		// time this exact class has bitten -- the comment above records codex
		// catching prior_candidate_receipts dropped here in the same way. A
		// field can be on the tool schema, on MCPInvestigateQuestionRequest,
		// and validated, and still never reach the engine, because nothing
		// structurally connects "the type has a field" to "the conversion
		// copies it". The contract-parity gate cannot see this: it compares
		// schemas to Go types, not mapping functions.
		//
		// TestInvestigateQuestion_EveryRequestFieldIsForwarded now enumerates
		// the struct by reflection and fails on any unmapped field, so the
		// class is closed rather than this one instance.
		ParentResultID: input.ParentResultID,
		ExpectedKinds:  input.ExpectedKinds,
		SubjectHandles: input.SubjectHandles,
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
		// Synthesis "client" is the hosted synthesis_mode of the same name.
		SynthesisMode: input.Synthesis,
	}
	if input.Scope != nil {
		hosted.RequestedScope = contractsv1.ContextFabricRequestedScope{
			RepositorySlugs: input.Scope.RepositorySlugs,
			ProjectIDs:      input.Scope.ProjectIDs,
			TeamIDs:         input.Scope.TeamIDs,
		}
	}
	return hosted
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

// priorReceiptFields are the six receipt arrays of investigate_question.
var priorReceiptFields = []string{
	"prior_subject_receipts", "prior_kind_receipts", "prior_anchor_receipts",
	"prior_handle_receipts", "prior_window_receipts", "prior_candidate_receipts",
}

// expandBareReceiptIDs (CHAOS-7114) turns a bare receipt_id string in any
// prior_*_receipts array into the canonical {result_id, receipt_id} object,
// taking result_id from parent_result_id. A real client (Claude for Mac)
// sent the bare form. The object form is passed through unchanged. A bare
// string without parent_result_id is refused here with a message that names
// the field, because there is no result_id to give it. Everything else
// (bad JSON, wrong types, wrong prefixes) is left for the normal decode and
// Validate path, so this function widens the input and refuses nothing else.
func expandBareReceiptIDs(raw []byte, surface string) ([]byte, receiptFormSummary, error) {
	var summary receiptFormSummary
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return raw, summary, nil
	}
	var parent string
	if rawParent, ok := top["parent_result_id"]; ok {
		_ = json.Unmarshal(rawParent, &parent)
	}
	changed := false
	for _, field := range priorReceiptFields {
		rawField, ok := top[field]
		if !ok {
			continue
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(rawField, &entries); err != nil {
			continue
		}
		fieldChanged := false
		for i, entry := range entries {
			var bare string
			if err := json.Unmarshal(entry, &bare); err != nil {
				summary.Object++
				continue
			}
			if parent == "" {
				summary.RefusedField = field
				return nil, summary, fmt.Errorf("%s: %s has a bare receipt_id string, which needs parent_result_id (the result_id of the answer the receipt came from); pass parent_result_id, or pass {\"result_id\", \"receipt_id\"} objects", surface, field)
			}
			object, err := json.Marshal(map[string]string{"result_id": parent, "receipt_id": bare})
			if err != nil {
				return nil, summary, err
			}
			entries[i] = object
			summary.Bare++
			fieldChanged = true
		}
		if fieldChanged {
			encoded, err := json.Marshal(entries)
			if err != nil {
				return nil, summary, err
			}
			top[field] = encoded
			summary.Fields = append(summary.Fields, field)
			changed = true
		}
	}
	if !changed {
		return raw, summary, nil
	}
	encoded, err := json.Marshal(top)
	return encoded, summary, err
}

// receiptFormSummary is the decision basis of expandBareReceiptIDs, safe to
// log: counts and closed field names only, never a receipt or result id.
type receiptFormSummary struct {
	Bare         int      // bare receipt_id strings bound to parent_result_id
	Object       int      // entries that already carried their own result_id
	Fields       []string // fields that contained at least one bare receipt
	RefusedField string   // the field that made the call refuse (no parent)
}
