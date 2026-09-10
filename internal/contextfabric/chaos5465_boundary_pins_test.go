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

	got := composeAcceptedContext(compositionInput{
		Fresh: &fresh, FreshGate: refusing, FreshFamily: QuestionFamilyGroupedCohortStatus,
		CarriedFamily: QuestionFamilyGroupedCohortStatus, CarriedGroupKind: contractsv1.ContextFabricSubjectTeam,
		EmittedShape: ShapeOpen,
	})
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
	t.Logf("disposition=%q reason=%q composition=%q invariant=%q accepted_group=%q plan_family=%q plan_group=%q",
		d.Disposition, d.Reason, d.CompositionOutcome, d.CompositionFailedInvariant,
		d.AcceptedGroupKind(), result.AnswerPlan.Family, result.AnswerPlan.GroupKind)

	if result.AnswerPlan.Family == QuestionFamilyGroupedCohortStatus && result.AnswerPlan.GroupKind == "" {
		t.Fatalf("a grouped family was served with NO grouping axis -- the carried family moved and its axis did not")
	}
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
		t.Errorf("a DIFFERENT carried family reported %q -- `unchanged` must mean nothing was carried that was not already there",
			CompositionUnchanged)
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

// r1 F3, the other half: the membership checks have a PRODUCTION caller.
//
// Both vocabularies shipped with a `Valid…` function, a doc comment saying the
// emitter uses it so an unrecognised value cannot reach a log line, and no
// caller at all -- coverage measured them at 0.0%. This drives an out-of-
// vocabulary value through the real emitter and asserts what the line carries.
func TestBoundary_AnUnrecognisedClosedValueCannotReachTheLine(t *testing.T) {
	t.Parallel()

	d := newWindowContinuationDecision(continuationRequest(validInvestigationRequest().Question))
	d.Reason = ContinuationDecisionReason("a-site-invented-this")
	d.CompositionOutcome = CompositionOutcome("and-this")

	var buf bytes.Buffer
	SlogEngineTelemetry{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}.
		RecordWindowContinuationDecision(context.Background(), acceptancePrincipal(), d)
	line := buf.String()
	t.Logf("EMITTED %s", strings.TrimSpace(line))

	for _, leaked := range []string{"a-site-invented-this", "and-this"} {
		if strings.Contains(line, leaked) {
			t.Errorf("free text %q reached a CLOSED telemetry field -- no consumer can group on it", leaked)
		}
	}
	if strings.Count(line, "decision_reason="+continuationTelemetryUnrecognised) != 1 {
		t.Errorf("decision_reason does not report %q", continuationTelemetryUnrecognised)
	}
	if strings.Count(line, "composition_outcome="+continuationTelemetryUnrecognised) != 1 {
		t.Errorf("composition_outcome does not report %q", continuationTelemetryUnrecognised)
	}
	// The sentinel must not be mistakable for a member of either vocabulary.
	if ValidContinuationDecisionReason(ContinuationDecisionReason(continuationTelemetryUnrecognised)) ||
		ValidCompositionOutcome(CompositionOutcome(continuationTelemetryUnrecognised)) {
		t.Errorf("the unrecognised sentinel is itself a vocabulary member -- a bug would be counted as a legitimate bucket")
	}
}
