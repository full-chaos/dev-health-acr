package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func servedHeadCandidate(modelStatus InvestigationStatus) (SubjectRef, InvestigationResult) {
	project, candidate := outcomeAuthorityCandidate(modelStatus, contractsv1.ContextFabricAnswerCompletenessDegraded)
	candidate.DirectJudgment = composeDirectJudgmentFrom(modelStatus, candidate.Drivers, candidate.SubjectResolution)
	candidate.DeterministicAnswer = composeDeterministicAnswerFrom(modelStatus, candidate.Drivers, candidate.ClaimedFacts, candidate.SubjectResolution)
	return project, candidate
}

func TestServedAnswerHeadStatesServedStatusThroughTheEngine(t *testing.T) {
	t.Parallel()
	degradedHead := statusSentence(InvestigationDegraded, SubjectResolution{})
	for _, modelStatus := range []InvestigationStatus{InvestigationComplete, InvestigationPartial} {
		modelStatus := modelStatus
		t.Run(string(modelStatus), func(t *testing.T) {
			t.Parallel()
			project, candidate := servedHeadCandidate(modelStatus)
			if !strings.HasPrefix(candidate.DirectJudgment, statusSentence(modelStatus, SubjectResolution{})) {
				t.Fatalf("premise: stored head = %q, want it to open with the %s sentence", candidate.DirectJudgment, modelStatus)
			}
			engine := completenessAuthorityTestEngine(t, EngineDependencies{
				Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}, true, true)
			result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != InvestigationDegraded {
				t.Fatalf("premise: served status = %q, want degraded", result.Status)
			}
			if !strings.HasPrefix(result.DirectJudgment, degradedHead) {
				t.Fatalf("DirectJudgment = %q, want it to state the served status: %q", result.DirectJudgment, degradedHead)
			}
			if !strings.HasPrefix(result.DeterministicAnswer, degradedHead) {
				t.Fatalf("DeterministicAnswer = %q, want it to state the served status: %q", result.DeterministicAnswer, degradedHead)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("restated result must validate: %v", err)
			}
		})
	}
}

func TestRestateServedStatusHeadEveryPair(t *testing.T) {
	t.Parallel()
	statuses := []InvestigationStatus{InvestigationComplete, InvestigationPartial, InvestigationDegraded}
	for _, draft := range statuses {
		for _, served := range statuses {
			draft, served := draft, served
			t.Run(string(draft)+"_to_"+string(served), func(t *testing.T) {
				t.Parallel()
				_, result := servedHeadCandidate(draft)
				result.Status = served
				drivers := result.Drivers
				got := restateServedStatusHead(result)
				if want := composeDirectJudgmentFrom(served, drivers, result.SubjectResolution); got.DirectJudgment != want {
					t.Fatalf("DirectJudgment = %q, want %q", got.DirectJudgment, want)
				}
				if want := composeDeterministicAnswerFrom(served, drivers, result.ClaimedFacts, result.SubjectResolution); got.DeterministicAnswer != want {
					t.Fatalf("DeterministicAnswer = %q, want %q", got.DeterministicAnswer, want)
				}
			})
		}
	}
}

func TestRestateServedStatusHeadLeavesOtherTerminalsAlone(t *testing.T) {
	t.Parallel()
	for _, status := range []InvestigationStatus{InvestigationNoMatch, InvestigationClarificationRequired} {
		_, result := servedHeadCandidate(InvestigationComplete)
		result.Status = status
		got := restateServedStatusHead(result)
		if got.DirectJudgment != result.DirectJudgment || got.DeterministicAnswer != result.DeterministicAnswer {
			t.Fatalf("status %s: head rewritten to %q", status, got.DirectJudgment)
		}
	}
}
