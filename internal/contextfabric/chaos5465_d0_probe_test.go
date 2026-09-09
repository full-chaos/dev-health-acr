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
		Family:    QuestionFamilyDiscoveredCohortRanking,
		GroupKind: "",
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

	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
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
		t.Logf("  SERVED answer_plan.family ..... %q", result.AnswerPlan.Family)
		t.Logf("  SERVED family_source .......... %q", result.AnswerPlan.FamilySource)
		t.Logf("  SERVED group_kind ............. %q", result.AnswerPlan.GroupKind)
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
	if result.AnswerPlan.Family != QuestionFamilyDiscoveredCohortRanking ||
		result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
		t.Fatalf(
			"D-0 PROBE 1: NOT FALSIFIED. The parent DISCARDED the validated turn-one context.\n"+
				"  want family=%q family_source=%q (parent preserves -> F2 wholly CHAOS-4835)\n"+
				"  got  family=%q family_source=%q\n"+
				"  plan-carry lookup outcome=%v, applied-carry emits=%d",
			QuestionFamilyDiscoveredCohortRanking, QuestionFamilySourceCarried,
			result.AnswerPlan.Family, result.AnswerPlan.FamilySource,
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

// D-0 PROBE 1, NEGATIVE CONTROL. Same fixture, same window-only receipt, same
// carrier -- but this turn resolves NOTHING of its own. applyCarriedPlan's
// documented condition is then satisfied and the carry MUST apply.
//
// Without this arm the probe above is vacuous: "family_source was never
// carried" would be indistinguishable from "the fixture's carrier was not
// carriable at all" or "this harness cannot observe an applied carry".
func TestWindowContinuation_D0ProbeControl_TheSameCarrierAppliesWhenThisTurnClassifiesNothing(t *testing.T) {
	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	prior := validInvestigationResult()
	prior.ResultID = "result_d0_probe1_turn1"
	prior.ConfirmedStructure = nil
	prior.AnswerPlan = &contractsv1.ContextFabricAnswerPlan{Family: QuestionFamilyDiscoveredCohortRanking}
	prior.WindowClarification = &WindowClarification{Options: []WindowOption{{
		ReceiptID: "winr_d0probe1aaaaaaaaaaaa", OptionID: "opt_90d", Label: "the last 90 days",
		RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd,
	}}}

	request := validInvestigationRequest()
	request.Question = prior.Question
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "winr_d0probe1aaaaaaaaaaaa"}}

	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
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
		// interpreterFunc's adapter reports unclassified/none -- the ONE
		// condition under which applyCarriedPlan applies today.
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
	if result.AnswerPlan == nil {
		t.Fatalf("control served no answer plan")
	}
	t.Logf("CONTROL: served family=%q family_source=%q applied-carry emits=%d carry-outcomes=%d",
		result.AnswerPlan.Family, result.AnswerPlan.FamilySource, len(telemetry.planCarries), len(telemetry.planCarryOutcomes))
	if result.AnswerPlan.Family != QuestionFamilyDiscoveredCohortRanking || result.AnswerPlan.FamilySource != QuestionFamilySourceCarried {
		t.Fatalf("CONTROL FAILED (probe above is vacuous): want family=%q source=%q, got family=%q source=%q",
			QuestionFamilyDiscoveredCohortRanking, QuestionFamilySourceCarried, result.AnswerPlan.Family, result.AnswerPlan.FamilySource)
	}
	if len(telemetry.planCarries) != 1 {
		t.Fatalf("CONTROL FAILED: want exactly 1 applied-carry emit, got %d", len(telemetry.planCarries))
	}
}
