package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Merge-gate round 3 (CHAOS-4763 PR 1b): sanitizeFrameOutput discarded the
// `unrecognized`/`dropped` signal from six of eleven Sanitize* call sites
// it makes -- Temporal, Emphasis, Dimensions, and every MemberKind/
// GroupKind site across every subject-expression variant, including
// operands. Only Goals, the discriminator Kind, and Terms/AnchorTerms were
// tracked. The organization_scope case is the sharpest instance: its
// MemberKind is entirely OPTIONAL (no invariant requires it), so a model
// emitting an unrecognized value there produced a frame that validated
// `valid` with NO signal anywhere that the model said anything at all --
// indistinguishable from a model that said nothing.
//
// RED ON PARENT (the tip before this fix): every assertion in this file
// that checks a Frame*Unrecognized/Frame*Dropped receipt field fails,
// because sanitizeFrameOutput/sanitizeOperandOutput discarded the bool at
// the call site before it ever reached the capture.

// TestSanitizeFrameOutputCountsUnrecognizedMemberKindOnOrganizationScope is
// codex's own repro, executed here rather than taken on its ARGUED claim:
// organization_scope + an out-of-vocabulary member_kind must now be
// countable on the receipt even though ValidateFrame has no invariant that
// would ever refuse it.
func TestSanitizeFrameOutputCountsUnrecognizedMemberKindOnOrganizationScope(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals:             []string{"assess_state"},
		SubjectExpression: &subjectExpressionOutput{Kind: "organization_scope", MemberKind: "squad"},
		Temporal:          "current",
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	if receipt.QuestionFrame == nil {
		t.Fatal("receipt.QuestionFrame = nil, want a proposal")
	}
	if !receipt.FrameMemberKindUnrecognized {
		t.Error("receipt.FrameMemberKindUnrecognized = false, want true -- the model emitted an out-of-vocabulary member_kind and it must be countable")
	}
	if receipt.QuestionFrame.SubjectExpression.Org == nil || receipt.QuestionFrame.SubjectExpression.Org.MemberKind != nil {
		t.Error("Org.MemberKind should still be dropped to nil (unchanged, correct sanitize behavior) -- only the countability signal is new")
	}

	// The part that makes this the sharpest instance of the class: no
	// invariant governs Org.MemberKind, so the frame validates cleanly
	// regardless of this fix. FrameMemberKindUnrecognized is the ONLY
	// signal that will ever exist for this case -- it does not, and
	// should not, change the validation outcome.
	result := contextfabric.ValidateFrame(*receipt.QuestionFrame, nil, contextfabric.ShapeSingleSubject)
	if result.Outcome != contextfabric.FrameValidationOutcomeValid {
		t.Errorf("ValidateFrame outcome = %v, want valid -- org.member_kind is optional by design; the countability fix must not invent a new refusal for a field no invariant reads", result.Outcome)
	}
}

// TestSanitizeFrameOutputRecoversTheInvalidReasonOnDiscoveredKind covers
// the sweep's second, more subtle instance (team-lead's explicit ask:
// verify whether a downstream invariant catches each site, and treat
// "caught downstream" as not sufficient on its own).
//
// discovered_kind's MemberKind IS required -- invariant I4 fails the frame
// either way. But I4 distinguishes member_kind_unset (the model said
// nothing) from member_kind_invalid (the model said something wrong), and
// because sanitizeFrameOutput already collapsed "invalid" to "" before I4
// ever saw it, EVERY invalid member_kind on this variant was misattributed
// as member_kind_unset -- the outcome was already non-valid, but the
// TELEMETRY REASON was already wrong before this fix, and remains the
// invariant's to report; this fix adds the one signal that recovers the
// true cause for anyone reading the receipt instead of trusting the
// invariant's (necessarily blind) attribution.
func TestSanitizeFrameOutputRecoversTheInvalidReasonOnDiscoveredKind(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals:             []string{"assess_state"},
		SubjectExpression: &subjectExpressionOutput{Kind: "discovered_kind", MemberKind: "squad"},
		Temporal:          "current",
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.QuestionFrame == nil {
		t.Fatal("receipt.QuestionFrame = nil, want a proposal")
	}
	if !receipt.FrameMemberKindUnrecognized {
		t.Error("receipt.FrameMemberKindUnrecognized = false, want true")
	}

	result := contextfabric.ValidateFrame(*receipt.QuestionFrame, nil, contextfabric.ShapeSingleSubject)
	if result.Outcome != contextfabric.FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("ValidateFrame outcome = %v, want refused_invalid -- discovered_kind.member_kind IS required, so this case was already caught before this fix", result.Outcome)
	}
	if result.Failure.Invariant != contextfabric.FrameInvariantI4 {
		t.Errorf("ValidateFrame failed invariant = %v, want I4", result.Failure.Invariant)
	}
	// DOCUMENTS THE PRE-EXISTING MISATTRIBUTION, not a claim this fix
	// changes it: the invariant still reports "unset" because sanitization
	// still collapses invalid-to-empty (correctly, per its own doc
	// comment -- I1's identical reasoning). FrameMemberKindUnrecognized on
	// the receipt is what tells a reader the true story; the invariant's
	// own detail code is a KNOWN, ACCEPTED gap, recorded here so it reads
	// as documented behavior rather than an unnoticed one.
	if result.Failure.Detail != contextfabric.FrameFailureMemberKindUnset {
		t.Errorf("ValidateFrame failure detail = %v, want member_kind_unset (the pre-existing, now-DOCUMENTED misattribution -- test needs updating if this ever changes)", result.Failure.Detail)
	}
}

// TestSanitizeFrameOutputCountsEveryOtherDroppedAxis closes the sweep for
// the three simpler, non-subject-expression axes: an unrecognized/dropped
// value on any of Temporal, Emphasis or Dimensions must be countable, the
// identical class as Goals already was.
func TestSanitizeFrameOutputCountsEveryOtherDroppedAxis(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals:             []string{"assess_state"},
		SubjectExpression: &subjectExpressionOutput{Kind: "named_subject", Terms: []string{"team a"}},
		Temporal:          "not_a_real_temporal_axis",
		Emphasis:          []string{"not_a_real_emphasis", "also_not_real"},
		Dimensions:        []string{"not_a_real_dimension"},
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.QuestionFrame == nil {
		t.Fatal("receipt.QuestionFrame = nil, want a proposal")
	}
	if !receipt.FrameTemporalUnrecognized {
		t.Error("receipt.FrameTemporalUnrecognized = false, want true")
	}
	if receipt.QuestionFrame.Temporal != "" {
		t.Errorf("QuestionFrame.Temporal = %q, want dropped to empty (unchanged sanitize behavior)", receipt.QuestionFrame.Temporal)
	}
	if receipt.FrameEmphasisDropped != 2 {
		t.Errorf("receipt.FrameEmphasisDropped = %d, want 2", receipt.FrameEmphasisDropped)
	}
	if receipt.FrameDimensionsDropped != 1 {
		t.Errorf("receipt.FrameDimensionsDropped = %d, want 1", receipt.FrameDimensionsDropped)
	}
}

// TestSanitizeFrameOutputCountsUnrecognizedGroupKindOnGroupedMembers is the
// grouped_members sibling of the discovered_kind test above: GroupKind is
// its own axis (I6), separate from MemberKind, and needs its own flag --
// the fix does not accidentally fold the two together.
func TestSanitizeFrameOutputCountsUnrecognizedGroupKindOnGroupedMembers(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals:             []string{"assess_state"},
		SubjectExpression: &subjectExpressionOutput{Kind: "grouped_members", GroupKind: "not_a_real_kind", MemberKind: "team"},
		Temporal:          "current",
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.QuestionFrame == nil {
		t.Fatal("receipt.QuestionFrame = nil, want a proposal")
	}
	if !receipt.FrameGroupKindUnrecognized {
		t.Error("receipt.FrameGroupKindUnrecognized = false, want true")
	}
	if receipt.FrameMemberKindUnrecognized {
		t.Error("receipt.FrameMemberKindUnrecognized = true, want false -- member_kind (\"team\") was valid; the two flags must not cross-contaminate")
	}
}

// TestSanitizeFrameOutputCountsUnrecognizedOperandMemberKind is the third
// call site codex named (sanitizeOperandOutput, the explicit_set operand
// loop) -- structurally distinct from the other two because it is inside
// a loop and must OR into the same receipt-level flag as the variant-level
// site, not overwrite it.
func TestSanitizeFrameOutputCountsUnrecognizedOperandMemberKind(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.QuestionFrame = &questionFrameOutput{
		Goals: []string{"compare"},
		SubjectExpression: &subjectExpressionOutput{
			Kind: "explicit_set",
			Operands: []subjectOperandOutput{
				{Kind: "named_subject", Terms: []string{"team a"}},
				{Kind: "children_of_scope", AnchorTerms: []string{"team b"}, MemberKind: "not_a_real_kind"},
			},
		},
		Temporal: "current",
	}

	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.QuestionFrame == nil {
		t.Fatal("receipt.QuestionFrame = nil, want a proposal")
	}
	if !receipt.FrameMemberKindUnrecognized {
		t.Error("receipt.FrameMemberKindUnrecognized = false, want true -- the second operand's member_kind was invalid")
	}
}
