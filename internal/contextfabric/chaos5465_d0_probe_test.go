package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// D-0 PROBE 1 -- astra section 5465-design-r1, PROBLEM paragraph, falsifier #1:
//
//	"Falsify this seam attribution by driving a valid prior context and a
//	 deliberately conflicting fresh interpretation through the parent engine
//	 and observing that the parent already preserves the complete prior
//	 semantics."
//
// If the PARENT already preserves the carried reading under forced
// disagreement, F2 is wholly CHAOS-4835's and CHAOS-5465 has no independently
// demonstrated defect to close. This probe is written to PASS when the parent
// preserves; it is expected to FAIL, and the failure text is the measurement.
//
// It is a PROBE, not a pin: it makes no production change and runs on the
// unmodified parent tree.
type d0ForcedInterpreter struct {
	family    QuestionFamily
	groupKind SubjectKind
}

func (d d0ForcedInterpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: "status",
		TimeContext:       TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		Family: d.family,
		Source: QuestionFamilySourceModel,
		WinningSample: FamilySample{
			ModelFamily: d.family,
			GroupKind:   d.groupKind,
		},
		WinningSampleIndex: 0,
		Version:            "question-family.v2",
	}, nil
}

func TestWindowContinuation_D0Probe_RedAtParentGreenAtTip(t *testing.T) {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// TURN ONE: a valid, servable prior context. Family discovered_cohort_ranking
	// (the archive's most common turn-one reading for cv-discovered-team-series),
	// plus a real window offer for turn two to redeem.
	prior := validInvestigationResult()
	prior.ResultID = "result_d0_probe1_turn1"
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{
		Family:        QuestionFamilyDiscoveredCohortRanking,
		GroupKind:     "",
		FamilyVersion: QuestionFamilyTableVersion,
		Budget:        contractsv1.ContextFabricAnswerPlanBudget{MaxSerializedBytes: continuationCarrierBudgetBytes()},
	}
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{
		ReceiptID:  "winr_d0probe1aaaaaaaaaaaa",
		OptionID:   "opt_90d",
		Label:      "the last 90 days",
		RelativeID: RelativeWindowTrailing90D,
		Start:      &frozenStart,
		End:        &frozenEnd,
	}}}

	// TURN TWO: IDENTICAL question bytes, one valid window receipt, and NO other
	// prior-result reference -- exactly D-a's window-only continuation shape and
	// exactly the archived F2 request shape (probe 2: 38/38 requests).
	request := validInvestigationRequest()
	request.Question = prior.Question
	request.PriorWindowReceipts = []BoundSubjectReceipt{{
		ResultID: prior.ResultID, ReceiptID: "winr_d0probe1aaaaaaaaaaaa",
	}}

	if len(request.Question) == 0 || request.Question != prior.Question {
		t.Fatalf("probe fixture defect: turn-two question bytes must equal turn one's")
	}

	// Turn one saved its accepted reading, as every production turn does.
	store := withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}})
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		// FORCED DISAGREEMENT: turn two proposes the OTHER family, with a group
		// axis turn one never had.
		Interpreter: d0ForcedInterpreter{
			family:    QuestionFamilyGroupedCohortStatus,
			groupKind: contractsv1.ContextFabricSubjectTeam,
		},
		Results:   store,
		Telemetry: telemetry,
	})

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	t.Logf("PROBE READINGS -------------------------------------------------")
	t.Logf("  turn-one carried family ....... %q", prior.AnswerPlan.Family)
	t.Logf("  turn-two FORCED fresh family .. %q (group_kind=%q)", QuestionFamilyGroupedCohortStatus, contractsv1.ContextFabricSubjectTeam)
	if result.AnswerPlan != nil {
		t.Logf("  SERVED answer_plan.family ..... %q", servedPlanFamily(result))
		t.Logf("  SERVED family_source .......... %q", servedPlanSource(result))
		t.Logf("  SERVED group_kind ............. %q", servedPlanGroup(result))
	} else {
		t.Logf("  SERVED answer_plan ............ <nil>")
	}
	t.Logf("  plan-carry OUTCOME records .... %d", len(telemetry.planCarryOutcomes))
	for i, rec := range telemetry.planCarryOutcomes {
		t.Logf("    [%d] outcome=%q source_result_id=%q seed_source=%q", i, rec.outcome, rec.sourceResultID, rec.seedSource)
	}
	t.Logf("  APPLIED-carry records ......... %d  (the only emit that can say family_source=carried)", len(telemetry.planCarries))
	t.Logf("----------------------------------------------------------------")

	if result.AnswerPlan == nil {
		t.Fatalf("no answer plan served -- cannot read the probe's observable")
	}
	// THE FALSIFIER. Passes only if the parent already preserves turn one's
	// validated reading under forced disagreement.
	if servedPlanFamily(result) != QuestionFamilyDiscoveredCohortRanking ||
		servedPlanSource(result) != QuestionFamilySourceCarried {
		t.Fatalf(
			"D-0 PROBE 1: NOT FALSIFIED. The parent DISCARDED the validated turn-one context.\n"+
				"  want family=%q family_source=%q (parent preserves -> F2 wholly CHAOS-4835)\n"+
				"  got  family=%q family_source=%q\n"+
				"  plan-carry lookup outcome=%v, applied-carry emits=%d",
			QuestionFamilyDiscoveredCohortRanking, QuestionFamilySourceCarried,
			servedPlanFamily(result), servedPlanSource(result),
			func() []PlanCarryOutcome {
				out := make([]PlanCarryOutcome, 0, len(telemetry.planCarryOutcomes))
				for _, r := range telemetry.planCarryOutcomes {
					out = append(out, r.outcome)
				}
				return out
			}(),
			len(telemetry.planCarries),
		)
	}
}

// D-0 PROBE 1, NEGATIVE CONTROLS. TWO of them, and r3 is why there are two.
//
// THE ORIGINAL CONTROL STOPPED DISCRIMINATING WHEN THE FIX LANDED. It asserted
// `planCarries == 1` to prove the LEGACY applyCarriedPlan path still applies a
// carrier -- which is what makes the probe's red at the parent meaningful. But
// the r1 fix added a RecordPlanCarry emit inside applyAndRecordContinuation, so
// at the tip that same count of 1 is satisfied by the NEW emitter, and the
// control passed while proving nothing about the path it names. A control that
// cannot fail for the reason it exists is not a control.
//
// The two are separated by ATTRIBUTION, using the signal that already
// distinguishes them: an applied-carry emit is the CONTINUATION's when the
// decision for that turn says `applied`, and the LEGACY path's when it does
// not. Each control asserts one, and asserts the other is absent.

// legacyAppliedCarries counts applied-carry emits ATTRIBUTABLE TO THE LEGACY
// path -- i.e. emits on a turn where no continuation was applied. It returns 0
// when the continuation applied, because then the emit is the new one.
func legacyAppliedCarries(telemetry *recordingTelemetry) int {
	for _, d := range telemetry.windowContinuationDecisions {
		if d.Disposition == ContinuationApplied {
			return 0
		}
	}
	return len(telemetry.planCarries)
}

// continuationAppliedCarries is its twin: emits attributable to the NEW path.
func continuationAppliedCarries(telemetry *recordingTelemetry) int {
	for _, d := range telemetry.windowContinuationDecisions {
		if d.Disposition == ContinuationApplied {
			return len(telemetry.planCarries)
		}
	}
	return 0
}

// CONTROL A -- THE LEGACY PATH. A request that is NOT a window-only
// continuation (it also names a parent), whose turn classifies nothing, and
// whose carrier is valid. The continuation cannot apply here, so an applied
// carry can only have come from applyCarriedPlan.
func TestWindowContinuation_D0ControlA_TheLegacyCarryStillApplies(t *testing.T) {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	prior := validInvestigationResult()
	prior.ResultID = "result_d0_probe1_turn1"
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{Family: QuestionFamilyDiscoveredCohortRanking, FamilyVersion: QuestionFamilyTableVersion, Budget: contractsv1.ContextFabricAnswerPlanBudget{MaxSerializedBytes: continuationCarrierBudgetBytes()}}
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{
		ReceiptID: "winr_d0probe1aaaaaaaaaaaa", OptionID: "opt_90d", Label: "the last 90 days",
		RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd,
	}}}

	request := validInvestigationRequest()
	request.Question = prior.Question
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "winr_d0probe1aaaaaaaaaaaa"}}
	// The parent makes the shape NOT window-only, so admission refuses and the
	// legacy carry is the only route left.
	request.ParentResultID = prior.ResultID

	// Turn one saved its accepted reading, as every production turn does.
	store := withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}})
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	t.Logf("CONTROL A: served family=%q source=%q legacy_carries=%d continuation_carries=%d",
		servedPlanFamily(result), servedPlanSource(result),
		legacyAppliedCarries(telemetry), continuationAppliedCarries(telemetry))

	if servedPlanSource(result) != QuestionFamilySourceCarried {
		t.Fatalf("CONTROL A FAILED: served family_source=%q, want carried -- the legacy carry must still apply, or the probe's red at the parent proves nothing",
			servedPlanSource(result))
	}
	if legacyAppliedCarries(telemetry) != 1 {
		t.Fatalf("CONTROL A FAILED: legacy-attributed applied-carry emits = %d, want 1", legacyAppliedCarries(telemetry))
	}
	if continuationAppliedCarries(telemetry) != 0 {
		t.Fatalf("CONTROL A FAILED: %d emits attributed to the CONTINUATION on a turn it cannot have applied to", continuationAppliedCarries(telemetry))
	}
}

// CONTROL B -- THE NEW PATH, AND THE PROOF THAT IT CANNOT SATISFY CONTROL A.
// The window-only continuation shape, admitted. Its emit must attribute to the
// continuation and NOT to the legacy path, so a build that routed the new emit
// through the legacy attribution would fail here.
func TestWindowContinuation_D0ControlB_TheContinuationEmitCannotPassAsLegacy(t *testing.T) {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	prior := validInvestigationResult()
	prior.ResultID = "result_d0_probe1_turn1"
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{Family: QuestionFamilyDiscoveredCohortRanking, FamilyVersion: QuestionFamilyTableVersion, Budget: contractsv1.ContextFabricAnswerPlanBudget{MaxSerializedBytes: continuationCarrierBudgetBytes()}}
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{
		ReceiptID: "winr_d0probe1aaaaaaaaaaaa", OptionID: "opt_90d", Label: "the last 90 days",
		RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd,
	}}}

	request := validInvestigationRequest()
	request.Question = prior.Question
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "winr_d0probe1aaaaaaaaaaaa"}}

	// Turn one saved its accepted reading, as every production turn does.
	store := withCarrierStates(t, &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}})
	telemetry := &recordingTelemetry{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	fresh := validInvestigationResult()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			bases:      provenCommitBases(project),
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results:   store,
		Telemetry: telemetry,
	})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	t.Logf("CONTROL B: served family=%q source=%q legacy_carries=%d continuation_carries=%d",
		servedPlanFamily(result), servedPlanSource(result),
		legacyAppliedCarries(telemetry), continuationAppliedCarries(telemetry))

	if continuationAppliedCarries(telemetry) != 1 {
		t.Fatalf("CONTROL B FAILED: continuation-attributed emits = %d, want 1", continuationAppliedCarries(telemetry))
	}
	if legacyAppliedCarries(telemetry) != 0 {
		t.Fatalf("CONTROL B FAILED: the NEW emitter satisfied the LEGACY attribution (%d) -- control A would then pass on a build where applyCarriedPlan never ran",
			legacyAppliedCarries(telemetry))
	}
}
