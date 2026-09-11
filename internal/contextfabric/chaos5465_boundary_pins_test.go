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

// KILLS ARM `fresh_refused_frame_composed_anyway` AND ITS NIL-FRAME TWIN.
//
// A 2x2 OVER {nil, non-nil} x {passing, refusing}, and it is a 2x2 because the
// cell that shipped a P1 was the one no pin and no arm touched. The refusal pin
// covered a REFUSING gate with a frame; the nil-frame cells covered a PASSING
// gate with no frame; nobody wrote nil-and-refusing, and there the boundary
// returned `no_fresh_frame` -- which `Usable()` accepts -- before it ever asked
// whether the gate refused. A turn the server had refused was then recorded as
// an applied continuation and served the carried family.
//
// The order is the fix: a refusal is about the EVALUATION, not about the frame,
// so it is answered before the frame is examined at all. Enumerating the cross
// product is what stops the next reordering from re-opening a corner.
func TestBoundary_ARefusingGateIsNeverUsable(t *testing.T) {
	t.Parallel()

	withFrame, passing := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository)
	if passing.Refuses() {
		t.Fatalf("fixture defect: the frame must pass its own gate first, got %q", passing.Outcome)
	}
	refusing := FrameGate{
		Outcome:            FrameGateRefusedBasis,
		RefuseBasis:        CohortMemberKindUnservable,
		DeclaredMemberKind: contractsv1.ContextFabricSubjectRepository,
	}

	for _, frameCase := range []struct {
		name  string
		frame *QuestionFrame
	}{
		{"no frame proposed", nil},
		{"frame proposed", &withFrame},
	} {
		for _, gateCase := range []struct {
			name string
			gate FrameGate
		}{
			{"gate passes", passing},
			{"gate refuses", refusing},
		} {
			t.Run(frameCase.name+"/"+gateCase.name, func(t *testing.T) {
				got := composeAcceptedContext(compositionInput{
					Fresh: frameCase.frame, FreshGate: gateCase.gate,
					FreshFamily:      QuestionFamilyDiscoveredCohortRanking,
					CarriedFamily:    QuestionFamilyGroupedCohortStatus,
					CarriedGroupKind: contractsv1.ContextFabricSubjectTeam,
					EmittedShape:     ShapeOpen,
				})
				t.Logf("frame_nil=%v gate=%q refuses=%v -> outcome=%q usable=%v group=%q",
					frameCase.frame == nil, gateCase.gate.Outcome, gateCase.gate.Refuses(),
					got.Outcome, got.Usable(), got.EffectiveGroupKind())

				if !gateCase.gate.Refuses() {
					if !got.Usable() {
						t.Errorf("a PASSING gate produced an unusable context (%q) -- refusal is reserved for a refused evaluation and a substitution that fails",
							got.Outcome)
					}
					return
				}
				// EVERY refusing cell, frame or no frame.
				if got.Outcome != CompositionFreshRefused {
					t.Errorf("outcome=%q want %q -- the gate had already refused this evaluation",
						got.Outcome, CompositionFreshRefused)
				}
				if got.Usable() {
					t.Errorf("a REFUSED fresh gate produced a USABLE context (%q) -- the refusal is laundered into a served continuation",
						got.Outcome)
				}
				if got.Frame != nil {
					t.Errorf("a refused composition handed back a frame")
				}
				if got.EffectiveGroupKind() != "" {
					t.Errorf("a refused composition published an effective group %q -- nothing executed under it",
						got.EffectiveGroupKind())
				}
				if got.Gate.Outcome != gateCase.gate.Outcome {
					t.Errorf("gate=%q want the ORIGINAL refusal %q", got.Gate.Outcome, gateCase.gate.Outcome)
				}
			})
		}
	}
}

// The same cell through the real engine: a refused evaluation with no proposed
// frame must not end the turn as an applied continuation serving the carrier.
func TestBoundary_ARefusedTurnWithNoFrameIsNotAContinuation(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	h := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		nilFrameRefusingGateInterpreter{family: QuestionFamilyDiscoveredCohortRanking})

	result := h.investigate(t, req)
	d := h.soleDecision(t)
	t.Logf("disposition=%q reason=%q composition=%q accepted=%v | status=%q plan_source=%q plan_family=%q plan_group=%q",
		d.Disposition, d.Reason, d.CompositionOutcome, d.Accepted != nil,
		result.Status, result.AnswerPlan.FamilySource, result.AnswerPlan.Family, result.AnswerPlan.GroupKind)

	if d.Disposition == ContinuationApplied {
		t.Fatalf("the fresh gate REFUSED and the continuation was applied anyway (composition=%q)", d.CompositionOutcome)
	}
	if d.CompositionOutcome != CompositionFreshRefused {
		t.Errorf("composition_outcome=%q, want %q", d.CompositionOutcome, CompositionFreshRefused)
	}
	if d.Accepted != nil {
		t.Errorf("a withheld turn published an accepted context")
	}
	if result.AnswerPlan.FamilySource == QuestionFamilySourceCarried {
		t.Errorf("the refused turn served the carried family anyway (family=%q group=%q)",
			result.AnswerPlan.Family, result.AnswerPlan.GroupKind)
	}
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

	// group=project member=team; carrying `team` makes group == member, which
	// invariant I6 forbids.
	fresh, gate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam)
	accepted := composeAcceptedContext(compositionInput{
		Fresh: &fresh, FreshGate: gate, FreshFamily: QuestionFamilyGroupedCohortStatus,
		CarriedFamily: QuestionFamilyGroupedCohortStatus, CarriedGroupKind: contractsv1.ContextFabricSubjectTeam,
		EmittedShape: ShapeOpen,
	})
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
// The previous build composed a grouped carrier onto a valid NON-grouped fresh
// frame, called the result `unchanged`, and served it: the grouped family was
// carried while the axis it groups by was silently dropped, so the answer was
// grouped-family data with no groups. The pin that existed asserted the empty
// axis as CORRECT, which is worse than no pin -- it froze the defect.
//
// A frame that cannot express the carried axis is a composition that cannot be
// honoured. It refuses, and names why.
func TestBoundary_AGroupedFamilyIsNeverServedWithoutItsAxis(t *testing.T) {
	req := continuationRequest(validInvestigationRequest().Question)
	prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	h := newContinuationHarness(t,
		&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
		r1UngroupedInterpreter{family: QuestionFamilyDiscoveredCohortRanking})

	result := h.investigate(t, req)
	d := h.soleDecision(t)
	planFamily, _, planGroup := servedPlanAxes(result)
	t.Logf("disposition=%q reason=%q composition=%q invariant=%q accepted_group=%q plan_family=%q plan_group=%q refusal_basis=%q",
		d.Disposition, d.Reason, d.CompositionOutcome, d.CompositionFailedInvariant,
		d.AcceptedGroupKind(), planFamily, planGroup, result.RefusalBasis)

	if planFamily == QuestionFamilyGroupedCohortStatus && planGroup == "" {
		t.Fatalf("a grouped family was served with NO grouping axis -- the carried family moved and its axis did not")
	}
	// The composition could not be honoured, so the turn refuses rather than
	// answering under the fresh reading the caller never confirmed.
	assertContinuationRefused(t, result)
	if d.Disposition != ContinuationWithheld {
		t.Errorf("disposition=%q, want %q: the carried axis cannot be expressed by a non-grouped frame",
			d.Disposition, ContinuationWithheld)
	}
	if d.Reason != ContinuationReasonCompositionInvalid {
		t.Errorf("reason=%q, want %q", d.Reason, ContinuationReasonCompositionInvalid)
	}
	if d.CompositionOutcome != CompositionInvalid {
		t.Errorf("composition_outcome=%q, want %q", d.CompositionOutcome, CompositionInvalid)
	}
	if d.CompositionFailedInvariant != CompositionInvariantCarriedAxisUnexpressible {
		t.Errorf("invariant=%q, want %q -- a refusal with no named cause is not actionable",
			d.CompositionFailedInvariant, CompositionInvariantCarriedAxisUnexpressible)
	}
}

// r1 F1, at the boundary itself: `unchanged` means the fresh reading ALREADY
// carries this family and this axis. Anything else either composes or refuses.
func TestBoundary_UnchangedMeansTheFreshReadingAlreadyMatches(t *testing.T) {
	t.Parallel()
	ungrouped := QuestionFrame{
		Goals:             []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{Kind: SubjectExpressionOrganizationScope, Org: &OrganizationScopeExpression{}},
		Temporal:          TemporalIntentCurrent,
	}
	v := ValidateFrame(ungrouped, nil, ShapeOpen)
	if v.Outcome != FrameValidationOutcomeValid {
		t.Fatalf("fixture defect: %v", v.Failure.Invariant)
	}
	gate := DecideFrameGate(v, true)

	got := composeAcceptedContext(compositionInput{
		Fresh: &v.Frame, FreshGate: gate, FreshFamily: QuestionFamilyDiscoveredCohortRanking,
		CarriedFamily: QuestionFamilyGroupedCohortStatus, CarriedGroupKind: contractsv1.ContextFabricSubjectTeam,
		EmittedShape: ShapeOpen,
	})
	t.Logf("non-grouped frame + carried team axis -> outcome=%q invariant=%q usable=%v",
		got.Outcome, got.FailedInvariant, got.Usable())
	if got.Outcome != CompositionInvalid || got.Usable() {
		t.Fatalf("outcome=%q usable=%v, want %q and unusable", got.Outcome, got.Usable(), CompositionInvalid)
	}

	// The axis-free carrier on the same frame IS unchanged only when the
	// family matches too.
	same := composeAcceptedContext(compositionInput{
		Fresh: &v.Frame, FreshGate: gate, FreshFamily: QuestionFamilyDiscoveredCohortRanking,
		CarriedFamily: QuestionFamilyDiscoveredCohortRanking, CarriedGroupKind: "",
		EmittedShape: ShapeOpen,
	})
	if same.Outcome != CompositionUnchanged {
		t.Errorf("same family and axis -> outcome=%q, want %q", same.Outcome, CompositionUnchanged)
	}
	diff := composeAcceptedContext(compositionInput{
		Fresh: &v.Frame, FreshGate: gate, FreshFamily: QuestionFamilyGroupedCohortStatus,
		CarriedFamily: QuestionFamilyDiscoveredCohortRanking, CarriedGroupKind: "",
		EmittedShape: ShapeOpen,
	})
	if diff.Outcome == CompositionUnchanged {
		t.Errorf("a DIFFERENT carried family on a non-grouped frame reported %q -- `unchanged` must mean nothing was carried that was not already there",
			CompositionUnchanged)
	}

	// AND THE SAME RULE ON THE GROUPED BRANCH. The two branches decide
	// `unchanged` independently, so a pin that exercises one of them leaves the
	// other free to call a real family carry "nothing changed" -- which a
	// mutation arm demonstrated it would.
	grouped, groupedGate := boundaryGroupedFrame(t, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectRepository)
	sameAxis := compositionInput{
		Fresh: &grouped, FreshGate: groupedGate,
		CarriedGroupKind: contractsv1.ContextFabricSubjectTeam,
		EmittedShape:     ShapeOpen,
	}
	sameAxis.FreshFamily, sameAxis.CarriedFamily = QuestionFamilyGroupedCohortStatus, QuestionFamilyGroupedCohortStatus
	if got := composeAcceptedContext(sameAxis); got.Outcome != CompositionUnchanged {
		t.Errorf("grouped frame, same family AND axis -> %q, want %q", got.Outcome, CompositionUnchanged)
	}
	sameAxis.FreshFamily, sameAxis.CarriedFamily = QuestionFamilyGroupedCohortStatus, QuestionFamilyDiscoveredCohortRanking
	carriedFamily := composeAcceptedContext(sameAxis)
	t.Logf("grouped frame, matching axis, DIFFERENT family -> outcome=%q group=%q", carriedFamily.Outcome, carriedFamily.EffectiveGroupKind())
	if carriedFamily.Outcome == CompositionUnchanged {
		t.Errorf("grouped frame with a DIFFERENT carried family reported %q -- a family WAS carried, so the composition is not `unchanged`",
			CompositionUnchanged)
	}
	if !carriedFamily.Usable() || carriedFamily.EffectiveGroupKind() != contractsv1.ContextFabricSubjectTeam {
		t.Errorf("outcome=%q group=%q -- carrying a family on a matching axis must stay usable and keep the axis",
			carriedFamily.Outcome, carriedFamily.EffectiveGroupKind())
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
		reason ContinuationDecisionReason
	}{
		{"interpreted axis veto", nil, ContinuationReasonInterpretedAxisVeto},
		{
			"explicit structure hint",
			func(r *InvestigationRequest) {
				r.ExpectedKinds = []SubjectKind{contractsv1.ContextFabricSubjectProject}
			},
			ContinuationReasonExplicitStructureHint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := continuationRequest(validInvestigationRequest().Question)
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			prior := r4CheckedPrior(t, continuationPriorID, req.Question, QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
			h := newContinuationHarness(t,
				&staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}},
				unclassifiedAxisMovingInterpreter{})

			result := h.investigate(t, req)
			d := h.soleDecision(t)
			t.Logf("disposition=%q reason=%q window_only=%v blocks_legacy=%v | SERVED family=%q family_source=%q group=%q",
				d.Disposition, d.Reason, d.WindowOnlyShape, d.BlocksLegacyCarry(),
				result.AnswerPlan.Family, result.AnswerPlan.FamilySource, result.AnswerPlan.GroupKind)
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
			if result.AnswerPlan.FamilySource == QuestionFamilySourceCarried {
				t.Errorf("the continuation was REFUSED and the legacy carry served the same carrier anyway (family=%q group=%q)",
					result.AnswerPlan.Family, result.AnswerPlan.GroupKind)
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
	if strings.Count(line, "result_5465") < 2 {
		t.Errorf("the sanitised ids no longer carry the caller's value: %s", strings.TrimSpace(line))
	}
	// Secondary, and true either way: the record is still one line.
	if got := strings.Count(strings.TrimRight(line, "\n"), "\n"); got != 0 {
		t.Errorf("the emitted record spans %d extra line(s)", got)
	}
}
