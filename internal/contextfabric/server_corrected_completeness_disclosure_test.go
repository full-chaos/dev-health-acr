package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The server replaces a model-authored completeness label: the served and
// the stored document must say what it was, what it is, and which outcome row
// decided it. Production composition: Engine.Investigate write-back turn, a
// supplied draft that claims complete, the authority flag on.
func TestServerCorrectedCompletenessLabelIsDisclosedOnTheDocument(t *testing.T) {
	t.Parallel()
	frame := frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))
	turn := newFrameWriteBackTurn(t, frame, countedWriteBackDraft(InvestigationComplete), false, true)
	result := turn.run(t, "count question")
	if result.Status != InvestigationDegraded {
		t.Fatalf("setup: served status = %q, want the server correction to degraded", result.Status)
	}
	// Oracle: the two planning rows of this turn are `unavailable`
	// (see the red run's document); the first is the deciding row.
	var wantRequirement string
	for _, row := range result.Completeness.Outcomes {
		if row.Outcome == contractsv1.ContextFabricRequirementUnavailable {
			wantRequirement = row.Requirement
			break
		}
	}
	if wantRequirement == "" {
		t.Fatal("setup: the turn derived no unavailable outcome row")
	}
	if n := len(turn.telemetry.completenessAuthorities); n == 0 || !turn.telemetry.completenessAuthorities[n-1].StatusCorrectionDisclosed {
		t.Fatalf("the completeness authority line must state status_correction_disclosed for a corrected served document: %+v", turn.telemetry.completenessAuthorities)
	}
	for name, got := range map[string]InvestigationResult{"served": result, "saved": turn.store.saved} {
		correction := got.StatusCorrection
		if correction == nil {
			t.Fatalf("%s: status=%q was corrected from the model's complete and the document carries no status_correction", name, got.Status)
		}
		if correction.ModelStatus != InvestigationComplete || correction.Direction != contractsv1.ContextFabricStatusCorrectionCompleteToDegraded {
			t.Errorf("%s: correction = %+v, want model_status complete, direction complete_to_degraded", name, *correction)
		}
		if correction.DecidingRequirement != wantRequirement || correction.DecidingOutcome != contractsv1.ContextFabricRequirementUnavailable {
			t.Errorf("%s: deciding = %q/%q, want %q/unavailable", name, correction.DecidingRequirement, correction.DecidingOutcome, wantRequirement)
		}
		if got.Completeness.StatusCorrection == nil || *got.Completeness.StatusCorrection != *correction {
			t.Errorf("%s: completeness.status_correction = %+v, want the result's %+v", name, got.Completeness.StatusCorrection, *correction)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s: corrected document must validate: %v", name, err)
		}
	}
}

// With the flags off the served status is the model's own label: no record.
func TestUncorrectedStatusCarriesNoStatusCorrection(t *testing.T) {
	t.Parallel()
	frame := frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))
	turn := newFrameWriteBackTurn(t, frame, countedWriteBackDraft(InvestigationComplete), false, false)
	result := turn.run(t, "count question")
	if result.Status != InvestigationComplete {
		t.Fatalf("setup: status = %q, want the model's complete with both flags off", result.Status)
	}
	if result.StatusCorrection != nil || result.Completeness.StatusCorrection != nil {
		t.Fatalf("status_correction = %+v / %+v, want absent on an uncorrected status", result.StatusCorrection, result.Completeness.StatusCorrection)
	}
}

// Every ordered pair against both flags, through the reuse-hit serving path:
// a record exists exactly when the served status differs from the model's,
// names the model's label and the served pair, and is never a promotion.
func TestStatusCorrectionIsPresentExactlyWhenTheServedStatusWasCorrected(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		model       InvestigationStatus
		server      contractsv1.ContextFabricAnswerCompletenessState
		enabled     bool
		symmetric   bool
		wantStatus  InvestigationStatus
		wantDir     contractsv1.ContextFabricStatusCorrectionDirection
		wantPresent bool
	}{
		{"complete_to_partial", InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessPartial, true, false, InvestigationPartial, contractsv1.ContextFabricStatusCorrectionCompleteToPartial, true},
		{"complete_to_degraded", InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessDegraded, true, false, InvestigationDegraded, contractsv1.ContextFabricStatusCorrectionCompleteToDegraded, true},
		{"partial_to_degraded", InvestigationPartial, contractsv1.ContextFabricAnswerCompletenessDegraded, false, true, InvestigationDegraded, contractsv1.ContextFabricStatusCorrectionPartialToDegraded, true},
		{"degraded_to_partial", InvestigationDegraded, contractsv1.ContextFabricAnswerCompletenessPartial, false, true, InvestigationPartial, contractsv1.ContextFabricStatusCorrectionDegradedToPartial, true},
		{"complete_flag_off", InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessDegraded, false, true, InvestigationComplete, "", false},
		{"lateral_flag_off", InvestigationPartial, contractsv1.ContextFabricAnswerCompletenessDegraded, true, false, InvestigationPartial, "", false},
		{"never_promoted", InvestigationPartial, contractsv1.ContextFabricAnswerCompletenessComplete, true, true, InvestigationPartial, "", false},
		{"agree", InvestigationPartial, contractsv1.ContextFabricAnswerCompletenessPartial, true, true, InvestigationPartial, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project, candidate := outcomeAuthorityCandidate(tc.model, tc.server)
			engine := completenessAuthorityTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}, tc.enabled, tc.symmetric)
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, tc.wantStatus)
			}
			if (result.StatusCorrection != nil) != tc.wantPresent {
				t.Fatalf("status_correction = %+v, present want %v", result.StatusCorrection, tc.wantPresent)
			}
			if (result.Completeness.StatusCorrection != nil) != tc.wantPresent {
				t.Fatalf("completeness.status_correction = %+v, present want %v", result.Completeness.StatusCorrection, tc.wantPresent)
			}
			if tc.wantPresent {
				if got := result.StatusCorrection; got.ModelStatus != tc.model || got.Direction != tc.wantDir {
					t.Fatalf("correction = %+v, want model %q direction %q", *got, tc.model, tc.wantDir)
				}
			}
			// Only the degraded-row fixture is a complete, valid
			// document (the partial-row fixture omits impact).
			if tc.server == contractsv1.ContextFabricAnswerCompletenessDegraded {
				if err := result.Validate(); err != nil {
					t.Fatalf("served document must validate: %v", err)
				}
			}
		})
	}
}

// A stored result re-applied under the same knobs is idempotent, and a second
// correction keeps naming what the MODEL authored.
func TestStatusCorrectionSurvivesReapplyAndNamesTheModelLabel(t *testing.T) {
	t.Parallel()
	_, candidate := outcomeAuthorityCandidate(InvestigationComplete, contractsv1.ContextFabricAnswerCompletenessPartial)
	once := ApplyServerCompletenessAuthority(candidate, true, true, DeriveCompletenessAuthority(candidate))
	if once.Status != InvestigationPartial || once.StatusCorrection == nil {
		t.Fatalf("setup: status %q correction %+v", once.Status, once.StatusCorrection)
	}
	twice := ApplyServerCompletenessAuthority(once, true, true, DeriveCompletenessAuthority(once))
	if twice.StatusCorrection == nil || *twice.StatusCorrection != *once.StatusCorrection || twice.Status != once.Status {
		t.Fatalf("re-apply changed the record: %+v -> %+v", once.StatusCorrection, twice.StatusCorrection)
	}
	// The rows now say degraded: a lateral correction of the already
	// corrected partial still names the model's complete.
	once.Completeness.Outcomes = []RequirementOutcomeRow{{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "evidence/subject/team",
		Obligation: "evidence", Outcome: contractsv1.ContextFabricRequirementUnavailable,
		Impact: contractsv1.ContextFabricAnswerImpactDimension, CauseCoverage: contractsv1.ContextFabricCoverageDetailFactUnconfigured, CauseObserved: true,
	}}
	third := ApplyServerCompletenessAuthority(once, true, true, DeriveCompletenessAuthority(once))
	if third.Status != InvestigationDegraded || third.StatusCorrection == nil ||
		third.StatusCorrection.ModelStatus != InvestigationComplete ||
		third.StatusCorrection.Direction != contractsv1.ContextFabricStatusCorrectionCompleteToDegraded {
		t.Fatalf("second correction = status %q record %+v, want degraded naming complete", third.Status, third.StatusCorrection)
	}
}

// The log line carries the disclosure bit, tied to the document served.
func TestCompletenessAuthorityLineStatesWhetherTheCorrectionWasDisclosed(t *testing.T) {
	t.Parallel()
	args := CompletenessAuthorityLogArgs(CompletenessAuthorityObservation{StatusCorrectionDisclosed: true}, "org")
	found := false
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "status_correction_disclosed" {
			found = args[i+1] == true
		}
	}
	if !found {
		t.Fatal("completeness authority line lacks status_correction_disclosed=true")
	}
}
