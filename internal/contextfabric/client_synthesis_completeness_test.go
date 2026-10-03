package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func clientResultWithRows(status InvestigationStatus, rows ...RequirementOutcomeRow) InvestigationResult {
	result := InvestigationResult{
		Status: status, DirectJudgment: "x",
		Completeness: contractsv1.ContextFabricAnswerCompleteness{Outcomes: rows},
	}
	result.Versions.SynthesisSource = SynthesisSourceClient
	return result
}

func TestServerCompletenessAuthorityDegradesAPartialClientSynthesisResultOnlyWithTheSymmetricFlag(t *testing.T) {
	t.Parallel()
	result := clientResultWithRows(InvestigationPartial,
		outcomeRow(contractsv1.ContextFabricOutcomeStageAssembledResult, "evidence/subject/team", "evidence", contractsv1.ContextFabricRequirementUnavailable))
	observation := DeriveCompletenessAuthority(result)
	if !observation.Derived || observation.ServerState != contractsv1.ContextFabricAnswerCompletenessDegraded {
		t.Fatalf("fixture observation = %+v, want a derived degraded state", observation)
	}
	for _, enabled := range []bool{false, true} {
		if got := ApplyServerCompletenessAuthority(result, enabled, true, observation); got.Status != InvestigationDegraded {
			t.Errorf("symmetric on, complete flag %v: status = %q, want degraded", enabled, got.Status)
		}
		if got := ApplyServerCompletenessAuthority(result, enabled, false, observation); got.Status != InvestigationPartial {
			t.Errorf("symmetric off, complete flag %v: status = %q, want partial", enabled, got.Status)
		}
	}
}

func TestServerCompletenessAuthorityNeverMakesAClientSynthesisResultComplete(t *testing.T) {
	t.Parallel()
	result := clientResultWithRows(InvestigationPartial, satisfiedRow("evidence/subject/team", "evidence"))
	observation := DeriveCompletenessAuthority(result)
	if !observation.Derived || observation.ServerState != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("fixture observation = %+v, want a derived complete state", observation)
	}
	for _, enabled := range []bool{false, true} {
		for _, symmetric := range []bool{false, true} {
			if got := ApplyServerCompletenessAuthority(result, enabled, symmetric, observation); got.Status != InvestigationPartial {
				t.Errorf("flags %v/%v: status = %q, want partial", enabled, symmetric, got.Status)
			}
		}
	}
}

func TestServerCompletenessAuthorityLeavesANoMatchClientSynthesisResultUnchanged(t *testing.T) {
	t.Parallel()
	result := clientResultWithRows(InvestigationNoMatch)
	derived := DeriveCompletenessAuthority(result)
	observations := map[string]CompletenessAuthorityObservation{"derived from the result": derived}
	for _, state := range []contractsv1.ContextFabricAnswerCompletenessState{
		contractsv1.ContextFabricAnswerCompletenessComplete,
		contractsv1.ContextFabricAnswerCompletenessPartial,
		contractsv1.ContextFabricAnswerCompletenessDegraded,
	} {
		forced := derived
		forced.Derived, forced.ServerState = true, state
		observations["forced "+string(state)] = forced
	}
	for name, observation := range observations {
		for _, enabled := range []bool{false, true} {
			for _, symmetric := range []bool{false, true} {
				if got := ApplyServerCompletenessAuthority(result, enabled, symmetric, observation); got.Status != InvestigationNoMatch {
					t.Errorf("%s, flags %v/%v: status = %q, want no_match", name, enabled, symmetric, got.Status)
				}
			}
		}
	}
}

// A fresh client turn whose required sources are unavailable derives degraded
// through the engine; the symmetric flag decides whether that reaches the status.
func TestClientSynthesisTurnWithAnUnavailableRequiredSourceIsDegradedOnlyWithTheSymmetricFlag(t *testing.T) {
	t.Parallel()
	for _, symmetric := range []bool{true, false} {
		frame := frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))
		engine, runtime, store := newFrameTurnEngine(t, true, frame, symmetric)
		result, input := runTurnWithInput(t, engine, true)
		want := InvestigationPartial
		if symmetric {
			want = InvestigationDegraded
		}
		if result.Completeness.Outcomes == nil || len(result.Completeness.Outcomes) == 0 {
			t.Fatalf("symmetric %v: the turn derived no outcome rows", symmetric)
		}
		for name, got := range map[string]InvestigationResult{"served": result, "saved": store.saved} {
			if got.Status != want {
				t.Errorf("symmetric %v, %s: status = %q, want %q", symmetric, name, got.Status, want)
			}
			if got.Status == InvestigationComplete {
				t.Errorf("symmetric %v, %s: status is complete", symmetric, name)
			}
			if got.Versions.SynthesisSource != SynthesisSourceClient {
				t.Errorf("symmetric %v, %s: synthesis_source = %q, want client", symmetric, name, got.Versions.SynthesisSource)
			}
			for field, text := range map[string]string{"direct_judgment": got.DirectJudgment, "current_state": got.CurrentState, "deterministic_answer": got.DeterministicAnswer} {
				if text != contractsv1.ContextFabricClientSynthesisAnswer {
					t.Errorf("symmetric %v, %s: %s = %q, want the fixed client synthesis answer", symmetric, name, field, text)
				}
			}
		}
		if runtime.synthCalls != 0 {
			t.Errorf("symmetric %v: synthesize calls = %d, want 0", symmetric, runtime.synthCalls)
		}
		if input == nil {
			t.Errorf("symmetric %v: no synthesis input was delivered", symmetric)
		}
		if symmetric && result.Completeness.State != contractsv1.ContextFabricAnswerCompletenessDegraded {
			t.Errorf("served completeness state = %q, want degraded", result.Completeness.State)
		}
	}
}
