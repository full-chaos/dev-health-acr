package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// NAMED PINS FOR THE BOUNDARY'S OWN GUARDS.
//
// Each of these exists because a mutation battery arm SURVIVED without it. A
// guard with no pin is a line of code, not a property: the first battery over
// this branch weakened five of them and the suite stayed green. Every test
// below names the arm it kills.

func boundaryGroupedFrame(t *testing.T, group, member SubjectKind) (QuestionFrame, FrameGate) {
	t.Helper()
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind:    SubjectExpressionGroupedMembers,
			Grouped: &GroupedSetExpression{GroupKind: group, MemberKind: member},
		},
		Temporal: TemporalIntentCurrent,
	}
	result := ValidateFrame(frame, nil, ShapeOpen)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: frame group=%s member=%s is invalid (%v)", group, member, result.Failure.Invariant)
	}
	return result.Frame, DecideFrameGate(result, true)
}

// KILLS ARM `fresh_refused_frame_composed_anyway` (`if freshGate.Refuses()` -> `if false`).
//
// The frame here is perfectly VALID -- the gate refuses for a reason validation
// never asks about (the declared member kind cannot be discovered). That is the
// discriminating shape: a battery arm that deletes the refusal check still
// produces a frame that validates, so only an assertion about the OUTCOME can
// see the difference. Without this pin, composition laundered a refusal the
// server had already issued into an `accepted` context.
func TestBoundary_RefusedFreshGateIsNeverComposedOn(t *testing.T) {
	t.Parallel()

	fresh, passing := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository)
	if passing.Refuses() {
		t.Fatalf("fixture defect: the frame must pass its own gate first, got %q", passing.Outcome)
	}
	refusing := FrameGate{
		Outcome:            FrameGateRefusedBasis,
		RefuseBasis:        CohortMemberKindUnservable,
		DeclaredMemberKind: contractsv1.ContextFabricSubjectRepository,
	}

	got := composeAcceptedContext(&fresh, refusing, contractsv1.ContextFabricSubjectTeam, nil, ShapeOpen)
	t.Logf("fresh_valid=true fresh_gate=%q refuses=%v -> outcome=%q usable=%v frame_nil=%v gate=%q",
		refusing.Outcome, refusing.Refuses(), got.Outcome, got.Usable(), got.Frame == nil, got.Gate.Outcome)

	if got.Outcome != CompositionFreshRefused {
		t.Errorf("outcome=%q want %q -- composition ran on a frame the gate had already refused",
			got.Outcome, CompositionFreshRefused)
	}
	if got.Usable() {
		t.Errorf("a composition over a REFUSED fresh gate reported usable -- the refusal was laundered into a served context")
	}
	if got.Frame != nil {
		t.Errorf("a refused composition handed back a frame; downstream would execute on a frame no passing gate certifies")
	}
	if got.Gate.Outcome != refusing.Outcome {
		t.Errorf("gate=%q want the ORIGINAL refusal %q -- the refusal must survive composition unaltered",
			got.Gate.Outcome, refusing.Outcome)
	}
}

// KILLS ARM `apply_reads_sample_not_accessor`
// (`outcome.WinningSample.GroupKind = accepted.EffectiveGroupKind()` -> `= carried.GroupKind`).
//
// A NON-GROUPED fresh reading is the discriminating case, and the only one:
// wherever the fresh frame IS grouped and the composition succeeded, the
// accessor and the carried value agree by construction, so a suite built only
// on grouped shapes cannot tell the two sources apart. Here the fresh frame has
// no group axis at all, the carried reading has one, and the accessor answers
// with the frame's -- because that is the axis the planner and discovery will
// execute under. Reading the carried value instead republishes an axis nothing
// executed, which is the false-agreement defect in its original form.
func TestBoundary_ApplyReadsOnlyTheAccessor(t *testing.T) {
	t.Parallel()

	ungrouped := QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}},
		Temporal:          TemporalIntentCurrent,
	}
	result := ValidateFrame(ungrouped, nil, ShapeOpen)
	if result.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: ungrouped frame invalid (%v)", result.Failure.Invariant)
	}
	gate := DecideFrameGate(result, true)

	const carriedGroup = contractsv1.ContextFabricSubjectTeam
	accepted := composeAcceptedContext(&result.Frame, gate, carriedGroup, nil, ShapeOpen)
	if accepted.Outcome != CompositionUnchanged || !accepted.Usable() {
		t.Fatalf("fixture defect: an ungrouped fresh frame must compose as %q and stay usable; got %q usable=%v",
			CompositionUnchanged, accepted.Outcome, accepted.Usable())
	}
	if accepted.EffectiveGroupKind() == carriedGroup {
		t.Fatalf("fixture defect: the accessor and the carried value must DIFFER to discriminate; both are %q", carriedGroup)
	}

	decision := windowContinuationDecision{
		Observed: true, WindowOnlyShape: true, Disposition: ContinuationApplied,
		Accepted: &continuationCarriedContext{
			Family: QuestionFamilyGroupedCohortStatus, GroupKind: carriedGroup,
			FamilyVersion: QuestionFamilyTableVersion, SourceResultID: continuationPriorID,
		},
	}
	before := QuestionFamilyOutcome{
		Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel,
		WinningSample: FamilySample{GroupKind: contractsv1.ContextFabricSubjectProject},
	}

	after, applied := applyWindowContinuation(before, decision, accepted)
	t.Logf("carried_group=%q accessor=%q -> applied=%v served_sample_group=%q",
		carriedGroup, accepted.EffectiveGroupKind(), applied, after.WinningSample.GroupKind)

	if !applied {
		t.Fatalf("fixture defect: a usable context with an applying decision must apply")
	}
	if after.WinningSample.GroupKind != accepted.EffectiveGroupKind() {
		t.Errorf("served sample group=%q, accessor=%q -- apply read a SECOND source for the effective axis; the comparison and the planner would then answer differently for one turn",
			after.WinningSample.GroupKind, accepted.EffectiveGroupKind())
	}
	if after.WinningSample.GroupKind == carriedGroup {
		t.Errorf("served sample group=%q is the CARRIED value, and no grouped expression in the executed frame carries it", carriedGroup)
	}
}

// KILLS ARM `apply_ignores_unusable_context`
// (`if !decision.Applies() || !accepted.Usable()` -> `if !decision.Applies()`).
//
// Admission and composition are separate authorities on purpose: admission says
// this turn IS a continuation, composition says the carried reading CANNOT be
// expressed as a valid frame. When they disagree, nothing is installed. The
// battery arm that dropped the second half applied a continuation whose frame
// had failed an invariant, and the suite could not see it because every other
// pin fed apply a usable context.
func TestBoundary_ApplyInstallsNothingWhenUnusable(t *testing.T) {
	t.Parallel()

	// group=project member=team; carrying `team` makes group == member, which
	// invariant I6 forbids.
	fresh, gate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam)
	accepted := composeAcceptedContext(&fresh, gate, contractsv1.ContextFabricSubjectTeam, nil, ShapeOpen)
	if accepted.Outcome != CompositionInvalid || accepted.Usable() {
		t.Fatalf("fixture defect: this composition must be %q and unusable; got %q usable=%v",
			CompositionInvalid, accepted.Outcome, accepted.Usable())
	}

	decision := windowContinuationDecision{
		Observed: true, WindowOnlyShape: true, Disposition: ContinuationApplied,
		Accepted: &continuationCarriedContext{
			Family: QuestionFamilyGroupedCohortStatus, GroupKind: contractsv1.ContextFabricSubjectTeam,
			FamilyVersion: QuestionFamilyTableVersion, SourceResultID: continuationPriorID,
		},
	}
	before := QuestionFamilyOutcome{
		Family: QuestionFamilyDiscoveredCohortRanking, Source: QuestionFamilySourceModel,
		WinningSample: FamilySample{GroupKind: contractsv1.ContextFabricSubjectProject},
		Frame:         &fresh, Gate: gate,
	}

	after, applied := applyWindowContinuation(before, decision, accepted)
	t.Logf("composition=%q invariant=%q usable=%v decision_applies=%v -> applied=%v family=%s source=%s sample_group=%q gate=%q",
		accepted.Outcome, accepted.FailedInvariant, accepted.Usable(), decision.Applies(),
		applied, after.Family, after.Source, after.WinningSample.GroupKind, after.Gate.Outcome)

	if applied {
		t.Errorf("apply reported applied=true over an UNUSABLE context (%q, invariant %q)", accepted.Outcome, accepted.FailedInvariant)
	}
	if after.Family != before.Family {
		t.Errorf("family=%s want %s -- an unusable composition switched the served family", after.Family, before.Family)
	}
	if after.Source != before.Source {
		t.Errorf("source=%s want %s -- the turn was recorded as carried on a composition that failed an invariant", after.Source, before.Source)
	}
	if after.WinningSample.GroupKind != before.WinningSample.GroupKind {
		t.Errorf("sample group=%q want %q -- the effective axis was replaced from a refused composition",
			after.WinningSample.GroupKind, before.WinningSample.GroupKind)
	}
	if after.Gate.Outcome != before.Gate.Outcome {
		t.Errorf("gate=%q want %q -- the refused composition's gate was installed over the fresh one", after.Gate.Outcome, before.Gate.Outcome)
	}
	if after.Frame != before.Frame {
		t.Errorf("the frame pointer was replaced from an unusable composition")
	}
	if after.Route.Disposition == FamilyRouteCarried {
		t.Errorf("route recorded %q for a continuation that was never installed", after.Route.Disposition)
	}
}

// KILLS ARM `identity_weakened_to_hash` (the empty-canonical guard -> `if false`).
//
// Raw-byte equality alone is NOT sufficient for identity, and this is the case
// that proves it: two questions that are byte-identical but carry no
// canonicalisable content at all. Bytes agree, so a guard that only compares
// bytes admits a continuation of a question the server cannot even say it
// understood. The verdict must be INDETERMINATE, not `none`.
func TestBoundary_IdentityRefusesWhenCanonicalFormIsEmpty(t *testing.T) {
	t.Parallel()

	const contentless = "???"
	if CanonicalizeQuestion(contentless) != "" {
		t.Fatalf("fixture defect: %q must canonicalise to empty to discriminate; got %q",
			contentless, CanonicalizeQuestion(contentless))
	}

	got := continuationQuestionIdentity(contentless, contentless)
	t.Logf("raw_equal=true canonical=%q -> reason=%q", CanonicalizeQuestion(contentless), got)

	if got == ContinuationReasonNone {
		t.Fatalf("identity reason = %q for a question with NO canonical content -- byte equality alone admitted a continuation the server cannot establish identity for",
			got)
	}
	if got != ContinuationReasonIndeterminateIdentity {
		t.Fatalf("identity reason = %q, want %q", got, ContinuationReasonIndeterminateIdentity)
	}
}
