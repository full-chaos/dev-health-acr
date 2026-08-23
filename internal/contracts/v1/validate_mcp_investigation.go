package v1

import (
	"fmt"
	"strings"
)

// MCP investigation tool bounds. Each mirrors the hosted contract's own
// bound for the same concept, so an MCP caller can never construct a
// request the hosted side would reject on size alone.
const (
	MCPInvestigationQuestionMaxLength   = 8000
	MCPInvestigationConversationMaxTurn = 50
	MCPInvestigationReceiptsMaxCount    = 20
	MCPInvestigationScopeMaxCount       = 200
	MCPInvestigationBudgetMinBytes      = 8192
	MCPInvestigationBudgetMaxBytes      = 1048576
)

// Validate checks the investigate_question tool input.
//
// Budget fields accept zero as "not supplied" and are otherwise range
// checked. A caller may narrow the answer, never widen it past what the
// hosted API grants -- the sidecar clamps to the advertised capability
// limits after this runs.
func (r MCPInvestigateQuestionRequest) Validate() error {
	if !rawBoundedText(r.Question, 1, MCPInvestigationQuestionMaxLength) {
		return fmt.Errorf("investigate_question requires a question within v1 bounds")
	}
	if len(r.Conversation) > MCPInvestigationConversationMaxTurn {
		return fmt.Errorf("investigate_question conversation exceeds v1 bounds")
	}
	for _, turn := range r.Conversation {
		if err := turn.Validate(); err != nil {
			return fmt.Errorf("conversation: %w", err)
		}
	}
	if len(r.PriorSubjectReceipts) > MCPInvestigationReceiptsMaxCount {
		return fmt.Errorf("investigate_question prior subject receipts exceed v1 bounds")
	}
	for _, receipt := range r.PriorSubjectReceipts {
		if err := receipt.Validate(); err != nil {
			return fmt.Errorf("prior_subject_receipts: %w", err)
		}
		if err := validateNoStructureReceiptPrefix("prior_subject_receipts", receipt.ReceiptID, ""); err != nil {
			return err
		}
	}
	// CHAOS-3972 P3+W2: the four structure-receipt fields, sharing the
	// SAME namespace-prefix-checked validator the hosted contract's own
	// Validate uses (validateStructureReceiptField) -- this tool's own
	// shape is ergonomic and question-first, but a malformed or
	// wrong-namespace receipt must fail HERE, at the tool boundary, not
	// only after mapping onto the hosted request.
	if err := validateStructureReceiptField("prior_kind_receipts", r.PriorKindReceipts, ContextFabricKindOptionReceiptPrefix); err != nil {
		return err
	}
	if err := validateStructureReceiptField("prior_anchor_receipts", r.PriorAnchorReceipts, ContextFabricAnchorOptionReceiptPrefix); err != nil {
		return err
	}
	if err := validateStructureReceiptField("prior_handle_receipts", r.PriorHandleReceipts, ContextFabricHandleOptionReceiptPrefix); err != nil {
		return err
	}
	if err := validateStructureReceiptField("prior_window_receipts", r.PriorWindowReceipts, ContextFabricWindowOptionReceiptPrefix); err != nil {
		return err
	}
	// CHAOS-4012: prior_candidate_receipts is this surface's own candr_
	// twin of the four fields above -- same tool-boundary validation
	// discipline.
	if err := validateStructureReceiptField("prior_candidate_receipts", r.PriorCandidateReceipts, ContextFabricCandidateOptionReceiptPrefix); err != nil {
		return err
	}
	if len(r.ExpectedKinds) > ContextFabricExpectedKindsMaxCount {
		return fmt.Errorf("investigate_question expected_kinds exceeds v1 bounds")
	}
	seenExpectedKinds := make(map[ContextFabricSubjectKind]struct{}, len(r.ExpectedKinds))
	for _, kind := range r.ExpectedKinds {
		if !validContextFabricSubjectKind(kind) {
			return fmt.Errorf("investigate_question expected_kinds entry is invalid")
		}
		if _, exists := seenExpectedKinds[kind]; exists {
			return fmt.Errorf("investigate_question expected_kinds entries must be unique")
		}
		seenExpectedKinds[kind] = struct{}{}
	}
	if len(r.SubjectHandles) > MCPInvestigationReceiptsMaxCount {
		return fmt.Errorf("investigate_question subject_handles exceeds v1 bounds")
	}
	seenHandles := make(map[ContextFabricRequestedHandle]struct{}, len(r.SubjectHandles))
	for _, handle := range r.SubjectHandles {
		if err := handle.Validate(); err != nil {
			return fmt.Errorf("subject_handles: %w", err)
		}
		// codex xhigh review, CHAOS-3972 round 1, finding 4: the published
		// schema declares subject_handles uniqueItems -- Go must enforce
		// the identical bound at this tool boundary too.
		if _, exists := seenHandles[handle]; exists {
			return fmt.Errorf("investigate_question subject_handles entries must be unique")
		}
		seenHandles[handle] = struct{}{}
	}
	if r.EvidenceWindow != nil {
		if err := r.EvidenceWindow.validate(); err != nil {
			return fmt.Errorf("evidence_window: %w", err)
		}
	}
	if !ValidContextFabricWindowConfirmationMode(r.WindowConfirmationMode) {
		return fmt.Errorf("investigate_question window_confirmation_mode is invalid")
	}
	if r.Scope != nil {
		if err := r.Scope.Validate(); err != nil {
			return fmt.Errorf("scope: %w", err)
		}
	}
	if r.Budget != nil {
		if err := r.Budget.Validate(); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
	}
	return nil
}

func (s MCPInvestigationScope) Validate() error {
	groups := [][]string{s.RepositorySlugs, s.ProjectIDs, s.TeamIDs}
	for _, group := range groups {
		if len(group) > MCPInvestigationScopeMaxCount || !uniqueTrimmedStrings(group, 512) {
			return fmt.Errorf("investigation scope violates v1 bounds")
		}
		// '|' is reserved by delimited-string encodings a graph backend
		// adapter may use for a list-valued field; the hosted contract
		// rejects it in scope values, so reject it here rather than
		// letting the request fail later with a vaguer error.
		if containsSeparatorCharacter(group) {
			return fmt.Errorf("investigation scope values must not contain '|'")
		}
	}
	return nil
}

// Validate range-checks any budget field the caller actually set. Zero
// means "not supplied": the sidecar fills its own default, so zero must not
// be treated as a request for a zero-sized answer.
func (b MCPInvestigationBudget) Validate() error {
	if b.MaxDrivers < 0 || b.MaxDrivers > ContextFabricProjectedDriversMaxCount {
		return fmt.Errorf("investigation budget max_drivers violates v1 bounds")
	}
	if b.MaxCohortMembers < 0 || b.MaxCohortMembers > ContextFabricProjectedCohortMaxCount {
		return fmt.Errorf("investigation budget max_cohort_members violates v1 bounds")
	}
	if b.MaxEvidenceRefs < 0 || b.MaxEvidenceRefs > ContextFabricProjectedEvidenceMaxCount {
		return fmt.Errorf("investigation budget max_evidence_refs violates v1 bounds")
	}
	if b.MaxSerializedBytes != 0 && (b.MaxSerializedBytes < MCPInvestigationBudgetMinBytes || b.MaxSerializedBytes > MCPInvestigationBudgetMaxBytes) {
		return fmt.Errorf("investigation budget max_serialized_bytes violates v1 bounds")
	}
	return nil
}

func (r MCPInvestigationResultRequest) Validate() error {
	if !stringLengthBetween(r.ResultID, 8, 256) || strings.TrimSpace(r.ResultID) != r.ResultID {
		return fmt.Errorf("investigation_result requires a result_id within v1 bounds")
	}
	return nil
}

func (r MCPInvestigateQuestionResponse) Validate() error {
	if r.SchemaVersion != MCPInvestigateQuestionResponseSchema {
		return fmt.Errorf("investigate_question response schema version must be %q", MCPInvestigateQuestionResponseSchema)
	}
	if err := r.Structured.Validate(); err != nil {
		return fmt.Errorf("structured: %w", err)
	}
	if r.FullResult != nil {
		// Lenient: with CHAOS-3782 answer reuse a "fresh" answer can BE a
		// stored row, so the attached canonical result may predate a
		// bound correction even though the response itself is new.
		if err := ValidateStoredResult(*r.FullResult); err != nil {
			return fmt.Errorf("full_result: %w", err)
		}
		// The two views must describe the same investigation. A response
		// pairing a projection with an unrelated result would let a
		// consumer cite evidence from one answer while quoting the
		// judgment of another.
		if r.FullResult.ResultID != r.Structured.ResultID {
			return fmt.Errorf("investigate_question full_result must be the same result the projection describes")
		}
		// Carrying a full result while claiming it was omitted is
		// incoherent in the direction that matters: a caller acting on
		// the flag would fetch a result it already holds.
		if r.Structured.ProjectionBudget.FullResultOmitted {
			return fmt.Errorf("investigate_question full_result is present but declared omitted")
		}
	}
	if err := validateUntrustedContent(r.UntrustedContent, MCPInvestigateQuestionUntrustedFields); err != nil {
		return err
	}
	return validateMCPRenderedMarkdown(r.RenderedMarkdown)
}

func (r MCPInvestigationResultResponse) Validate() error {
	if r.SchemaVersion != MCPInvestigationResultResponseSchema {
		return fmt.Errorf("investigation_result response schema version must be %q", MCPInvestigationResultResponseSchema)
	}
	// This tool returns a RETRIEVED, immutable row, so it is validated
	// leniently for the same reason the store and the client are: a result
	// that predates a bound correction must stay reachable (codex round-4
	// F1). The answer tool above stays strict, because its payload was
	// produced now.
	if err := ValidateStoredResult(r.Structured); err != nil {
		return fmt.Errorf("structured: %w", err)
	}
	if err := validateUntrustedContent(r.UntrustedContent, MCPInvestigationResultUntrustedFields); err != nil {
		return err
	}
	return validateMCPRenderedMarkdown(r.RenderedMarkdown)
}

// validateUntrustedContent enforces the declaration exactly, not merely its
// presence. The declared field list must equal the contract's own list:
// a response free to shorten it could quietly drop a field from the
// untrusted set, and a consumer trusting the declaration would then treat
// model-derived text as safe.
func validateUntrustedContent(declaration MCPUntrustedContent, expected []string) error {
	if !declaration.Untrusted {
		return fmt.Errorf("structured payload must declare its content untrusted")
	}
	if strings.TrimSpace(declaration.Notice) == "" {
		return fmt.Errorf("untrusted content declaration requires a notice")
	}
	if len(declaration.Fields) != len(expected) {
		return fmt.Errorf("untrusted content declaration must enumerate exactly the contract's untrusted fields")
	}
	for i, field := range expected {
		if declaration.Fields[i] != field {
			return fmt.Errorf("untrusted content declaration field %d is %q, want %q", i, declaration.Fields[i], field)
		}
	}
	return nil
}

// validateMCPRenderedMarkdown enforces the shared rendering envelope. The
// untrusted flag is a const true in every MCP response schema: retrieved
// content is data, never instructions, and a rendering that could claim
// otherwise would undermine that guarantee for every consumer.
func validateMCPRenderedMarkdown(rendered MCPRenderedMarkdown) error {
	if !rendered.Untrusted {
		return fmt.Errorf("rendered markdown must be marked untrusted")
	}
	if !stringLengthBetween(rendered.Markdown, 1, 24000) {
		return fmt.Errorf("rendered markdown violates v1 bounds")
	}
	return nil
}
