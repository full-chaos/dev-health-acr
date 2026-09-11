package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// carriedStateFor builds a carrier snapshot through the producer: a frame (or
// none), the gate decided on it, the plan's family and group axis.
func carriedStateFor(t *testing.T, family QuestionFamily, group SubjectKind, frame *QuestionFrame, gate FrameGate) *PersistedSemanticState {
	t.Helper()
	state := BuildSemanticState(SemanticStateInput{
		Outcome:       QuestionFamilyOutcome{Family: family, Source: QuestionFamilySourceModel, Frame: frame, Gate: gate},
		EmittedShape:  ShapeOpen,
		GroupKind:     group,
		FamilyVersion: QuestionFamilyTableVersion,
	})
	if _, err := EncodeSemanticState(state); err != nil {
		t.Fatalf("fixture defect: carrier snapshot does not validate: %v", err)
	}
	return state
}

// unservableDiscoveredFrame finds a discovered-kind frame the gate REFUSES
// (member kind unservable), read off the producer rather than hand-listed.
func unservableDiscoveredFrame(t *testing.T) (QuestionFrame, FrameGate) {
	t.Helper()
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		frame := QuestionFrame{
			Goals:             []InvestigationGoal{GoalAssessState},
			SubjectExpression: SubjectExpression{Kind: SubjectExpressionDiscoveredKind, Discovered: &DiscoveredSetExpression{MemberKind: kind}},
			Temporal:          TemporalIntentCurrent,
		}
		result := ValidateFrame(frame, nil, ShapeOpen)
		if result.Outcome != FrameValidationOutcomeValid {
			continue
		}
		if gate := DecideFrameGate(result, true); gate.Outcome == FrameGateRefusedBasis {
			return result.Frame, gate
		}
	}
	t.Fatalf("fixture defect: no discovered kind is refused by the gate")
	return QuestionFrame{}, FrameGate{}
}

// A REFUSING CARRIED READING IS NEVER USABLE, AND THE FRESH GATE IS NOT
// AUTHORITY OVER A USABLE ONE.
//
// A 2x2 over {carried frameless, carried framed} x {carried gate passes,
// carried gate refuses}, each cell run beside BOTH a passing and a refusing
// fresh gate. The carried gate decides; the fresh gate's verdict never
// changes the outcome. Before the reading persisted it was the other way
// round -- the fresh frame was the base the carried axis was substituted
// into, so a refused fresh evaluation had to refuse the composition.
func TestBoundary_ARefusingGateIsNeverUsable(t *testing.T) {
	t.Parallel()
	framed, passing := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository)
	unservable, refusing := unservableDiscoveredFrame(t)
	freshFramed, _ := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectRepository)
	for _, tc := range []struct {
		name       string
		carried    *PersistedSemanticState
		wantUsable bool
	}{
		{"carried frameless, gate not evaluated", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, nil, FrameGate{}), true},
		{"carried frameless, gate refused", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, nil, FrameGate{Outcome: FrameGateRefusedBasis, RefuseBasis: CohortMemberKindUnservable}), false},
		{"carried framed, gate passes", carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectProject, &framed, passing), true},
		{"carried framed, gate refuses", carriedStateFor(t, QuestionFamilyDiscoveredCohortRanking, "", &unservable, refusing), false},
	} {
		for _, fresh := range []struct {
			name  string
			frame *QuestionFrame
		}{{"fresh frame absent", nil}, {"fresh frame present", &freshFramed}} {
			t.Run(tc.name+"/"+fresh.name, func(t *testing.T) {
				got := composeAcceptedContext(compositionInput{Carried: tc.carried, Fresh: fresh.frame, FreshFamily: QuestionFamilyDiscoveredCohortRanking})
				t.Logf("carried_frame=%v carried_gate=%q -> outcome=%q usable=%v invariant=%q group=%q",
					tc.carried.FramePresent, tc.carried.Validation.GateOutcome, got.Outcome, got.Usable(), got.FailedInvariant, got.EffectiveGroupKind())
				if got.Usable() != tc.wantUsable {
					t.Fatalf("usable=%v, want %v (outcome %q, invariant %q)", got.Usable(), tc.wantUsable, got.Outcome, got.FailedInvariant)
				}
				if !tc.wantUsable {
					if got.Outcome != CompositionInvalid || got.FailedInvariant != CompositionInvariantCarriedFrameRefused {
						t.Errorf("outcome=%q invariant=%q, want %q/%q", got.Outcome, got.FailedInvariant, CompositionInvalid, CompositionInvariantCarriedFrameRefused)
					}
					if got.Frame != nil || got.EffectiveGroupKind() != "" {
						t.Errorf("a refused composition handed back frame=%v group=%q", got.Frame != nil, got.EffectiveGroupKind())
					}
					return
				}
				if got.EffectiveGroupKind() != tc.carried.GroupKind {
					t.Errorf("group=%q, want the CARRIED axis %q", got.EffectiveGroupKind(), tc.carried.GroupKind)
				}
				if (got.Frame != nil) != tc.carried.FramePresent {
					t.Errorf("frame present=%v, want the carried presence %v -- a frameless carrier continues frameless", got.Frame != nil, tc.carried.FramePresent)
				}
			})
		}
	}
}

// The same cell through the real engine: a refused evaluation with no proposed
// frame must not end the turn as an applied continuation serving the carrier.
func TestBoundary_ARefusedFreshGateDoesNotDecideAnEstablishedTransition(t *testing.T) {
	// FLIPPED by the axis-carry change, and this is the cell that flipped.
	//
	// It was written as "a refused turn with no frame is not a continuation",
	// which is right for a turn the caller has NOT already settled and wrong
	// for one they have. On an established transition -- identical question
	// bytes, exactly one valid window receipt, no explicit window, a readable
	// taint-valid carrier -- the caller has confirmed which reading they want
	// and that reading has already been served once under a gate of its own.
	// Consulting the fresh frame's gate there refuses the very turn the user
	// just confirmed, on a frame proposed for a question the receipt settled.
	// So the fresh proposal is dropped on this branch and the carried reading
	// is served. The control below is the same fixture with the transition
	// NOT established: there the fresh refusal still stands, unchanged.
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	h := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		nilFrameRefusingGateInterpreter{family: QuestionFamilyDiscoveredCohortRanking})

	result := h.investigate(t, req)
	d := h.soleDecision(t)
	t.Logf("disposition=%q reason=%q composition=%q accepted=%v | status=%q plan_source=%q plan_family=%q plan_group=%q",
		d.Disposition, d.Reason, d.CompositionOutcome, d.Accepted != nil,
		result.Status, servedPlanSource(result), servedPlanFamily(result), servedPlanGroup(result))

	if !d.TransitionEstablished {
		t.Fatalf("fixture did not establish the transition, so it pins nothing")
	}
	if d.Disposition != ContinuationApplied {
		t.Fatalf("disposition=%q, want %q -- the fresh gate refused a turn the receipt had already settled",
			d.Disposition, ContinuationApplied)
	}
	if d.CompositionOutcome == CompositionFreshRefused {
		t.Errorf("composition_outcome=%q -- the fresh gate was consulted on an established transition", d.CompositionOutcome)
	}
	if result.Status == InvestigationNoMatch {
		t.Errorf("the confirmed turn was refused: status=%q basis=%q", result.Status, result.RefusalBasis)
	}
	if servedPlanSource(result) != QuestionFamilySourceCarried {
		t.Errorf("served plan source=%q, want %q -- the carried reading is what the caller confirmed",
			servedPlanSource(result), QuestionFamilySourceCarried)
	}
	if result.RefusalBasis != "" {
		t.Errorf("the turn was refused (%q) on the fresh gate's verdict", result.RefusalBasis)
	}
	if !containsConflictField(d.ConflictFields, ContinuationConflictFieldFrameGate) {
		t.Errorf("conflict_fields=%v does not name frame_gate -- the fresh refusal must be disclosed as a disagreement", d.ConflictFieldTokens())
	}
}

// The control for the pin above: the SAME refusing fresh gate on a turn whose
// transition is NOT established (the question changed). Nothing about that turn
// was confirmed, so the fresh refusal stands and the carried family is not
// served -- the behaviour the original pin was written to protect, kept.
func TestBoundary_ARefusedFreshGateStillStandsWithoutAnEstablishedTransition(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	prior.Question = "What was the status of Ask Dev last spring and what drove it?"
	h := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		nilFrameRefusingGateInterpreter{family: QuestionFamilyDiscoveredCohortRanking})

	result := h.investigate(t, req)
	d := h.soleDecision(t)
	t.Logf("disposition=%q reason=%q composition=%q established=%v | status=%q plan_source=%q",
		d.Disposition, d.Reason, d.CompositionOutcome, d.TransitionEstablished,
		result.Status, servedPlanSource(result))

	if d.TransitionEstablished {
		t.Fatalf("the control established the transition, so it is not a control")
	}
	if d.Disposition == ContinuationApplied {
		t.Errorf("the fresh gate REFUSED on an unestablished turn and the continuation applied anyway (composition=%q)", d.CompositionOutcome)
	}
	if d.Accepted != nil {
		t.Errorf("a withheld turn published an accepted context")
	}
	if servedPlanSource(result) == QuestionFamilySourceCarried {
		t.Errorf("the refused turn served the carried family anyway (family=%q group=%q)",
			servedPlanFamily(result), servedPlanGroup(result))
	}
}

func containsConflictField(fields []ContinuationConflictField, want ContinuationConflictField) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

type nilFrameRefusingGateInterpreter struct{ family QuestionFamily }

func (i nilFrameRefusingGateInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		QuestionFamilyOutcome{
			Family: i.family, Source: QuestionFamilySourceModel,
			Frame: nil,
			Gate: FrameGate{
				Outcome:            FrameGateRefusedBasis,
				RefuseBasis:        CohortMemberKindUnservable,
				DeclaredMemberKind: contractsv1.ContextFabricSubjectRepository,
			},
			WinningSampleIndex: 0, WinningSample: FamilySample{ModelFamily: i.family},
			Version: QuestionFamilyTableVersion,
		}, nil
}

// KILLS ARM `apply_reads_sample_not_accessor`
// (`outcome.WinningSample.GroupKind = accepted.EffectiveGroupKind()` -> `= carried.GroupKind`).
//
// THE CONTEXT IS CONSTRUCTED, ON PURPOSE, and that is worth defending because
// an engine-driven version of this pin is now impossible. Since a carried family
// and its axis move together, every composition that comes back USABLE has an
// accepted axis equal to the carried one -- so no reachable turn makes the two
// sources disagree, and a pin built on one would be measuring nothing while
// looking thorough. The property here is not "these two values differ in
// production", it is "apply reads the accessor and not some other field", and
// that is exactly what a divergent constructed value can hold it to. If a later
// composition ever does normalise the axis, this pin already says which source
// wins.
func TestBoundary_ApplyReadsOnlyTheAccessor(t *testing.T) {
	t.Parallel()

	fresh, gate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository)
	const acceptedGroup = contractsv1.ContextFabricSubjectProject
	const carriedGroup = contractsv1.ContextFabricSubjectTeam
	accepted := AcceptedContext{
		Frame: &fresh, Gate: gate, Outcome: CompositionAccepted, GroupKind: acceptedGroup,
	}
	if accepted.EffectiveGroupKind() == carriedGroup {
		t.Fatalf("fixture defect: the accessor and the carried value must DIFFER to discriminate")
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
		WinningSample: FamilySample{GroupKind: contractsv1.ContextFabricSubjectRepository},
	}

	after, applied := applyWindowContinuation(before, decision, accepted)
	t.Logf("accessor=%q carried=%q -> applied=%v served_sample_group=%q",
		accepted.EffectiveGroupKind(), carriedGroup, applied, after.WinningSample.GroupKind)

	if !applied {
		t.Fatalf("fixture defect: a usable context with an applying decision must apply")
	}
	if after.WinningSample.GroupKind != accepted.EffectiveGroupKind() {
		t.Errorf("served sample group=%q, accessor=%q -- apply read a SECOND source for the effective axis; the comparison and the planner would then answer differently for one turn",
			after.WinningSample.GroupKind, accepted.EffectiveGroupKind())
	}
	if after.WinningSample.GroupKind == carriedGroup {
		t.Errorf("served sample group=%q is the CARRIED field, not the accessor", carriedGroup)
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

	// A carried frame today's validation would REPAIR (goals out of canonical
	// order, obligations not derived from them), so revalidation does not
	// return the recorded frame.
	fresh, gate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam)
	tampered := nonCanonicalFrame(fresh)
	carried := carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectProject, &tampered, gate)
	accepted := composeAcceptedContext(compositionInput{Carried: carried, Fresh: &fresh, FreshFamily: QuestionFamilyGroupedCohortStatus})
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

// ===========================================================================
// r1 (#492) FINDINGS. Five pins, each red before its fix.
// ===========================================================================

type r1UngroupedInterpreter struct{ family QuestionFamily }

func (i r1UngroupedInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	frame := QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}},
		Temporal:          TemporalIntentCurrent,
	}
	v := ValidateFrame(frame, nil, ShapeOpen)
	if v.Outcome != FrameValidationOutcomeValid {
		panic("fixture defect: ungrouped frame invalid " + string(v.Failure.Invariant))
	}
	return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		QuestionFamilyOutcome{
			Family: i.family, Source: QuestionFamilySourceModel, Frame: &v.Frame, Gate: DecideFrameGate(v, true),
			WinningSampleIndex: 0, WinningSample: FamilySample{ModelFamily: i.family},
			Version: QuestionFamilyTableVersion,
		}, nil
}

// r1 F1 — A CARRIED FAMILY AND ITS AXIS MOVE TOGETHER, OR NEITHER DOES.
//
// The first build composed a grouped carrier onto a valid NON-grouped fresh
// frame, called the result `unchanged`, and served the grouped family with no
// axis. The re-cut refused that composition. With the reading persisted, the
// carrier's OWN reading (family and axis together, and its own frame or its
// own absence of one) is what composition establishes, so a non-grouped fresh
// frame is simply a disagreement: the carried family is served WITH its axis,
// and the fresh frame's ungrouped expression is disclosed as a conflict.
func TestBoundary_AGroupedFamilyIsNeverServedWithoutItsAxis(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	h := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		r1UngroupedInterpreter{family: QuestionFamilyDiscoveredCohortRanking})

	result := h.investigate(t, req)
	d := h.soleDecision(t)
	planFamily, planSource, planGroup := servedPlanAxes(result)
	t.Logf("disposition=%q reason=%q composition=%q invariant=%q accepted_group=%q plan_family=%q plan_source=%q plan_group=%q conflicts=%v",
		d.Disposition, d.Reason, d.CompositionOutcome, d.CompositionFailedInvariant,
		d.AcceptedGroupKind(), planFamily, planSource, planGroup, d.ConflictFieldTokens())

	if planFamily == QuestionFamilyGroupedCohortStatus && planGroup == "" {
		t.Fatalf("a grouped family was served with NO grouping axis -- the carried family moved and its axis did not")
	}
	if d.Disposition != ContinuationApplied {
		t.Fatalf("disposition=%q/%q, want applied: the carrier's own reading is established whole", d.Disposition, d.Reason)
	}
	if planFamily != QuestionFamilyGroupedCohortStatus || planGroup != contractsv1.ContextFabricSubjectTeam || planSource != QuestionFamilySourceCarried {
		t.Errorf("served %q/%q/%q, want the carried grouped_cohort_status/team/carried", planFamily, planGroup, planSource)
	}
	if !containsConflictField(d.ConflictFields, ContinuationConflictFieldSubjectExpression) {
		t.Errorf("conflict_fields=%v does not name subject_expression -- the fresh ungrouped reading disagreed", d.ConflictFieldTokens())
	}
}

// r1 F1, at the boundary itself: `unchanged` means the fresh reading ALREADY
// carries this family and this frame (or this absence of one). Anything else
// the carrier establishes is `accepted`.
func TestBoundary_UnchangedMeansTheFreshReadingAlreadyMatches(t *testing.T) {
	t.Parallel()
	grouped, groupedGate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectRepository)
	other, _ := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository)
	framed := carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, &grouped, groupedGate)
	frameless := carriedStateFor(t, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam, nil, FrameGate{})
	for _, tc := range []struct {
		name        string
		carried     *PersistedSemanticState
		fresh       *QuestionFrame
		freshFamily QuestionFamily
		want        CompositionOutcome
	}{
		{"framed carrier, identical fresh frame and family", framed, &grouped, QuestionFamilyGroupedCohortStatus, CompositionUnchanged},
		{"framed carrier, identical frame, DIFFERENT family", framed, &grouped, QuestionFamilyDiscoveredCohortRanking, CompositionAccepted},
		{"framed carrier, DIFFERENT frame, same family", framed, &other, QuestionFamilyGroupedCohortStatus, CompositionAccepted},
		{"framed carrier, no fresh frame", framed, nil, QuestionFamilyGroupedCohortStatus, CompositionAccepted},
		{"frameless carrier, no fresh frame, same family", frameless, nil, QuestionFamilyGroupedCohortStatus, CompositionUnchanged},
		{"frameless carrier, no fresh frame, DIFFERENT family", frameless, nil, QuestionFamilyDiscoveredCohortRanking, CompositionAccepted},
		{"frameless carrier, a fresh frame", frameless, &grouped, QuestionFamilyGroupedCohortStatus, CompositionAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := composeAcceptedContext(compositionInput{Carried: tc.carried, Fresh: tc.fresh, FreshFamily: tc.freshFamily})
			t.Logf("-> outcome=%q usable=%v group=%q frame=%v", got.Outcome, got.Usable(), got.EffectiveGroupKind(), got.Frame != nil)
			if got.Outcome != tc.want {
				t.Errorf("outcome=%q, want %q", got.Outcome, tc.want)
			}
			if !got.Usable() || got.EffectiveGroupKind() != contractsv1.ContextFabricSubjectTeam {
				t.Errorf("usable=%v group=%q, want usable with the carried axis", got.Usable(), got.EffectiveGroupKind())
			}
		})
	}
}

// r1 F2 — AN ERROR RETURN KEEPS ITS OWN REASON.
//
// The save-time reversal read only "the served answer carries no window", which
// is trivially true of the zero result every error return produces. Every
// downstream failure in the engine was therefore published as a save-time
// window supersession, a save race that never happened.
func TestBoundary_AnErrorReturnIsNotAWindowSupersession(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	h := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		forcedFamilyInterpreter{family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectTeam})
	h.engine.graph = r1FailingGraph{graphReaderStub: h.engine.graph.(graphReaderStub)}

	_, err := h.engine.Investigate(context.Background(), acceptancePrincipal(), req)
	if err == nil {
		t.Fatalf("fixture defect: the injected resolution failure must make Investigate return an error")
	}
	d := h.soleDecision(t)
	t.Logf("error=%v disposition=%q reason=%q applied_window=%q", err, d.Disposition, d.Reason, d.AppliedWindowToken())
	if d.Reason == ContinuationReasonWindowSuperseded {
		t.Fatalf("an ordinary downstream failure was published as %q -- no save-time veto occurred, and an operator counting supersessions would be counting resolution errors",
			ContinuationReasonWindowSuperseded)
	}
}

type r1FailingGraph struct{ graphReaderStub }

func (g r1FailingGraph) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection, *QuestionFrame, SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, errR1InjectedResolution
}

var errR1InjectedResolution = errors.New("injected resolution failure")

// r1 F3 — THE COMPOSITION OUTCOME REACHES THE LINE, WITH A VALUE.
//
// The vocabulary, its membership check and the log key all existed; the only
// thing missing was a production writer on the successful path, so every
// applied continuation published the empty string and the two vocabulary
// functions had no caller at all (0.0% coverage). A field expected at its zero
// value pins nothing -- this asserts a NAMED member on an applied turn, and the
// distinct `not_evaluated` member on a turn where no composition ran.
func TestBoundary_TheCompositionOutcomeReachesTheLineWithAValue(t *testing.T) {
	emit := func(t *testing.T, d windowContinuationDecision) string {
		t.Helper()
		var buf bytes.Buffer
		SlogEngineTelemetry{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}.
			RecordWindowContinuationDecision(context.Background(), acceptancePrincipal(), d)
		return buf.String()
	}

	t.Run("applied", func(t *testing.T) {
		req := continuationRequest(validInvestigationRequest().Question)
		prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
		h := newContinuationHarness(t,
			&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
			frameBearingInterpreter{
				family: QuestionFamilyGroupedCohortStatus, groupKind: contractsv1.ContextFabricSubjectProject,
				frameGroup: contractsv1.ContextFabricSubjectProject,
			})
		h.investigate(t, req)
		d := h.soleDecision(t)
		line := emit(t, d)
		t.Logf("disposition=%q composition_outcome=%q", d.Disposition, d.CompositionOutcome)
		t.Logf("EMITTED %s", strings.TrimSpace(line))
		if d.Disposition != ContinuationApplied {
			t.Fatalf("fixture defect: wanted an applied continuation, got %q/%q", d.Disposition, d.Reason)
		}
		if d.CompositionOutcome == "" {
			t.Fatalf("an APPLIED continuation published composition_outcome=\"\"")
		}
		if !ValidCompositionOutcome(d.CompositionOutcome) {
			t.Fatalf("composition_outcome=%q is outside the closed vocabulary", d.CompositionOutcome)
		}
		if d.CompositionOutcome == CompositionNotEvaluated {
			t.Fatalf("an applied continuation reported %q -- a composition ran", CompositionNotEvaluated)
		}
		if !strings.Contains(line, "composition_outcome="+string(d.CompositionOutcome)) {
			t.Fatalf("the line does not carry the decision's own value %q", d.CompositionOutcome)
		}
	})

	t.Run("no composition ran", func(t *testing.T) {
		// A window receipt that is NOT a continuation: the decision is emitted,
		// and its composition outcome must say "not evaluated" rather than sit
		// at a zero value indistinguishable from a member.
		d := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
		line := emit(t, d)
		t.Logf("constructor composition_outcome=%q", d.CompositionOutcome)
		if d.CompositionOutcome != CompositionNotEvaluated {
			t.Fatalf("constructor composition_outcome=%q, want %q -- non-execution must be distinguishable from an evaluated verdict",
				d.CompositionOutcome, CompositionNotEvaluated)
		}
		if !strings.Contains(line, "composition_outcome="+string(CompositionNotEvaluated)) {
			t.Fatalf("the line does not carry %q", CompositionNotEvaluated)
		}
	})
}

// r1 F5 — THE CONTAINMENT'S SHAPE GUARD IS PINNED.
//
// BlocksLegacyCarry stops the old family-only carry serving the very carrier
// this gate refused, but ONLY for the window-only shape: a request that did
// something else must keep the legacy mechanism, which is correct there. The
// two containment pins that existed passed with the shape term deleted, because
// their arms were rejected by the old carry path for independent reasons.
func TestBoundary_ContainmentRequiresTheWindowOnlyShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		shape bool
		want  bool
	}{
		{"window-only shape blocks", true, true},
		{"another shape does not block", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := windowContinuationDecision{
				Observed: true, WindowOnlyShape: tc.shape,
				Disposition: ContinuationWithheld, Reason: ContinuationReasonContextVersionMismatch,
			}
			got := d.BlocksLegacyCarry()
			t.Logf("observed=true window_only_shape=%v disposition=%q -> blocks=%v", tc.shape, d.Disposition, got)
			if got != tc.want {
				t.Errorf("BlocksLegacyCarry()=%v, want %v -- containment is scoped to the window-only transition, and a build that blocked every shape would silently disable the family-only carry everywhere else",
					got, tc.want)
			}
		})
	}
}

// r2 F3 — EVERY CLOSED FIELD IS MEMBERSHIP-CHECKED, AND THE LIST COMES FROM
// THE PRODUCER.
//
// The r1 version of this pin guarded two fields and asserted those same two --
// an instrument enumerating only the inputs its author chose, which is one of
// the failures the review prompt names outright. Five other closed fields were
// reaching the line as free text: seed source, disposition, conflict reason,
// conflict fields, and the failed invariant.
//
// This walks `closedDecisionFields()` and, for each entry, seats an
// out-of-vocabulary value and asserts the line carries the sentinel and NOT the
// invented text. A field the emitter forgets to route through the registry
// fails here by leaking. A field with no driver has to justify itself: nil
// means "derived, no input can seat a non-member", and that is checked too.
func TestBoundary_EveryClosedFieldOnTheLineIsMembershipChecked(t *testing.T) {
	t.Parallel()

	emit := func(d windowContinuationDecision) string {
		var buf bytes.Buffer
		SlogEngineTelemetry{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}.
			RecordWindowContinuationDecision(context.Background(), acceptancePrincipal(), d)
		return buf.String()
	}

	fields := closedDecisionFields()
	if len(fields) < 2 {
		t.Fatalf("the closed-field registry has %d entries -- it is not describing this line", len(fields))
	}

	for _, field := range fields {
		t.Run(field.Key, func(t *testing.T) {
			base := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
			if !strings.Contains(emit(base), field.Key+"=") {
				t.Fatalf("registry names %q but the emitted line has no such key -- the registry and the line disagree", field.Key)
			}
			if field.Invent == nil {
				// The claim is that no input can seat a non-member. Check it by
				// driving the states the decision can actually be in.
				for _, d := range []windowContinuationDecision{
					base,
					func() windowContinuationDecision {
						withAccepted := base
						withAccepted.Accepted = &continuationCarriedContext{Family: QuestionFamilyGroupedCohortStatus}
						return withAccepted
					}(),
				} {
					if got := field.Token(d); got == continuationTelemetryUnrecognised {
						t.Errorf("%q is declared derived, but a reachable state produced %q", field.Key, got)
					}
				}
				return
			}

			mutated := base
			field.Invent(&mutated)
			token := field.Token(mutated)
			line := emit(mutated)
			t.Logf("%s -> token=%q", field.Key, token)
			if token != continuationTelemetryUnrecognised {
				t.Errorf("%q accepted an out-of-vocabulary value and reported %q", field.Key, token)
			}
			if !strings.Contains(line, field.Key+"="+continuationTelemetryUnrecognised) {
				t.Errorf("the line does not carry %s=%s; got:\n%s", field.Key, continuationTelemetryUnrecognised, strings.TrimSpace(line))
			}
			if strings.Contains(line, "invented-") {
				t.Errorf("free text reached a CLOSED field -- the emitter does not route %q through the registry; line:\n%s",
					field.Key, strings.TrimSpace(line))
			}
		})
	}

	// The sentinel must not be mistakable for a member of any of them.
	t.Run("sentinel is not a member", func(t *testing.T) {
		if ValidContinuationDecisionReason(ContinuationDecisionReason(continuationTelemetryUnrecognised)) ||
			ValidCompositionOutcome(CompositionOutcome(continuationTelemetryUnrecognised)) ||
			ValidContinuationDisposition(ContinuationDisposition(continuationTelemetryUnrecognised)) ||
			ValidContinuationConflictReason(ContinuationConflictReason(continuationTelemetryUnrecognised)) ||
			ValidContinuationConflictField(ContinuationConflictField(continuationTelemetryUnrecognised)) ||
			ValidCarrySeedSource(CarrySeedSource(continuationTelemetryUnrecognised)) {
			t.Errorf("the unrecognised sentinel is a vocabulary member -- a bug would be counted as a legitimate bucket")
		}
	})
}

// r2 F1 — A REFUSED WINDOW-ONLY CARRIER IS SERVED BY NOTHING, AT EVERY
// DISQUALIFIER EXIT.
//
// The containment has lost this three times, at a different exit each time, so
// this pin walks the exits rather than naming one. The discriminating fixture
// is the one the review had to build: an interpreter that leaves the family
// UNCLASSIFIED. Every earlier pin used an interpreter that classifies one,
// which independently disables the old family-only carry -- so the guard was
// wide open and no arm walked through it.
//
// A disqualifier says this turn may not CONTINUE. It does not say the request
// arrived in some other shape, and the containment keys on the shape.
func TestBoundary_ARefusedWindowOnlyCarrierIsServedByNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*InvestigationRequest)
		prior  func(InvestigationResult) InvestigationResult
		reason ContinuationDecisionReason
	}{
		// CHAOS-5582: the fresh-axis disqualifier is retired. The axis exit
		// this pin now walks is the CARRIER's recorded axis -- a carrier that
		// did not record `current` is refused, and under an axis-moving
		// unclassified turn the legacy carry must not serve it either.
		{
			"carrier records a non-current axis",
			nil,
			func(p InvestigationResult) InvestigationResult {
				p.Interpretation.TimeContext = TimeContext{Axis: TemporalValidTime, AsOf: &r2AsOf}
				return p
			},
			ContinuationReasonInvalidContext,
		},
		{
			"explicit structure hint",
			func(r *InvestigationRequest) {
				r.ExpectedKinds = []SubjectKind{contractsv1.ContextFabricSubjectProject}
			},
			nil,
			ContinuationReasonExplicitStructureHint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := continuationRequest(validInvestigationRequest().Question)
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			if tc.prior != nil {
				prior = tc.prior(prior)
			}
			h := newContinuationHarness(t,
				&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
				unclassifiedAxisMovingInterpreter{})

			result := h.investigate(t, req)
			d := h.soleDecision(t)
			t.Logf("disposition=%q reason=%q window_only=%v blocks_legacy=%v | SERVED family=%q family_source=%q group=%q",
				d.Disposition, d.Reason, d.WindowOnlyShape, d.BlocksLegacyCarry(),
				servedPlanFamily(result), servedPlanSource(result), servedPlanGroup(result))
			for _, o := range h.telemetry.planCarryOutcomes {
				t.Logf("plan carry: outcome=%q source=%q seed=%q", o.outcome, o.sourceResultID, o.seedSource)
			}

			if d.Reason != tc.reason {
				t.Fatalf("fixture defect: wanted %q, got %q/%q", tc.reason, d.Disposition, d.Reason)
			}
			if !d.WindowOnlyShape {
				t.Errorf("window_only_shape=false at the %q exit -- the request IS the window-only shape; a disqualifier does not change the shape it arrived in", tc.reason)
			}
			if !d.BlocksLegacyCarry() {
				t.Errorf("blocks_legacy=false on a refused window-only carrier")
			}
			if servedPlanSource(result) == QuestionFamilySourceCarried {
				t.Errorf("the continuation was REFUSED and the legacy carry served the same carrier anyway (family=%q group=%q)",
					servedPlanFamily(result), servedPlanGroup(result))
			}
		})
	}
}

type unclassifiedAxisMovingInterpreter struct{}

func (unclassifiedAxisMovingInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status",
		TimeContext: TimeContext{Axis: TemporalValidTime, AsOf: &r2AsOf},
	}, QuestionFamilyOutcome{
		Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceNone,
		WinningSampleIndex: 0, WinningSample: FamilySample{},
		Version: QuestionFamilyTableVersion,
	}, nil
}

// EVERY REQUEST-DERIVED VALUE ON THIS LINE IS STRIPPED OF CONTROL BYTES.
//
// The context ids on this event come from the caller: a window receipt names a
// prior result id, and that id is echoed back as `source_result_id`,
// `carried_context_id` and `accepted_context_id`. The closed fields are safe by
// construction -- they can only be a vocabulary member or the unrecognised
// sentinel -- so it is the free-text ones that need this.
//
// WHAT THIS PIN DOES NOT ASSERT, and the first version of it got this wrong:
// "the record is one line". slog's handlers already escape control characters
// inside a structured attribute value, so that property holds with or without
// any sanitisation here, and a pin resting on it passes on a build that strips
// nothing. It could not fail for the defect it was written for.
//
// What it asserts instead is that the control byte is GONE, not escaped: no
// `\n` or `\r` sequence survives inside a request-derived value. That is our
// behaviour, not the handler's, and it is what a static analyser can follow --
// which is the actual reason the strip exists, the runtime vector being already
// closed by the handler.
func TestBoundary_NoRequestDerivedValueCanForgeALogLine(t *testing.T) {
	t.Parallel()

	const forged = "result_5465\nlevel=ERROR msg=\"fabricated by the caller\""
	const carriage = "result_5465\rmsg=also-fabricated"

	d := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
	d.Disposition = ContinuationApplied
	d.Carried = &continuationCarriedContext{
		Family: QuestionFamilyGroupedCohortStatus, GroupKind: contractsv1.ContextFabricSubjectTeam,
		SourceResultID: forged,
	}
	d.Accepted = &continuationCarriedContext{
		Family: QuestionFamilyGroupedCohortStatus, GroupKind: contractsv1.ContextFabricSubjectTeam,
		SourceResultID: carriage,
	}
	// The receipt's own result id, published on every window-only decision.
	d.ReferencedResultID = "result_5465\nlevel=ERROR msg=\"forged referenced id\""

	principal := acceptancePrincipal()
	principal.OrgID = "org\nlevel=ERROR msg=\"forged org line\""

	var buf bytes.Buffer
	SlogEngineTelemetry{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}.
		RecordWindowContinuationDecision(context.Background(), principal, d)
	line := buf.String()
	t.Logf("EMITTED %s", strings.TrimSpace(line))

	// THE LOAD-BEARING ASSERTION: the control byte is removed, not merely
	// escaped by the handler. `\\n` here is the two-character escape slog
	// writes for a real newline -- its presence means the value still contained
	// one when it reached the sink.
	for _, escape := range []string{`\n`, `\r`} {
		if strings.Contains(line, escape) {
			t.Errorf("a request-derived value still carried a control byte (%s survived as an escape) -- it reached the sink unstripped: %s",
				escape, strings.TrimSpace(line))
		}
	}
	// And the value stays USEFUL: the id survives, minus the control bytes.
	if strings.Count(line, "result_5465") < 3 {
		t.Errorf("the sanitised ids no longer carry the caller's value: %s", strings.TrimSpace(line))
	}
	// Secondary, and true either way: the record is still one line.
	if got := strings.Count(strings.TrimRight(line, "\n"), "\n"); got != 0 {
		t.Errorf("the emitted record spans %d extra line(s)", got)
	}
}
