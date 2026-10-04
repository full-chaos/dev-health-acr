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

func TestRestateServedStatusHeadFindsOnlyAnOpeningStatusSentence(t *testing.T) {
	t.Parallel()
	complete := statusSentence(InvestigationComplete, SubjectResolution{})
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	degraded := statusSentence(InvestigationDegraded, SubjectResolution{})
	for _, row := range []struct {
		name, head, want string
	}{
		{"opening sentence is replaced, the rest kept", complete + " Principal driver: A.", partial + " Principal driver: A."},
		{"no recognisable status sentence is left alone", "Available work items.", "Available work items."},
		{"empty head is left alone", "", ""},
		{"a status sentence that is not the opening is left alone", "Note. " + complete, "Note. " + complete},
		{"two status sentences: only the opening one is replaced", complete + " " + degraded, partial + " " + degraded},
		{"already the served sentence is unchanged", partial + " Principal driver: A.", partial + " Principal driver: A."},
	} {
		got := restateHeadSentence(row.head, partial, directJudgmentMaxLength)
		if got != row.want {
			t.Errorf("%s: got %q, want %q", row.name, got, row.want)
		}
	}
}

func TestServedStatusHeadKeepsTheAnswerInsideTheBudgetItIsMeasuredAgainst(t *testing.T) {
	t.Parallel()
	partial := statusSentence(InvestigationPartial, SubjectResolution{})
	run := func(maxBytes int64) (InvestigationResult, error) {
		calls := 0
		shape := chaos6558ProdShape
		shape.maxBytes = maxBytes
		shape.composedHead = true
		engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, shape)
		return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	}
	served, err := run(chaos6558MaxBytes)
	if err != nil {
		t.Fatalf("premise: Investigate() error = %v", err)
	}
	if served.Status != InvestigationPartial || !strings.HasPrefix(served.DirectJudgment, partial) {
		t.Fatalf("premise: status %q head %q, want the lever's partial answer stating partial (draft was complete, a shorter head)", served.Status, served.DirectJudgment)
	}
	measured, err := contractsv1.MeasureContextFabricResponse(served)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	for ceiling := measured.Bytes; ceiling > measured.Bytes-400; ceiling-- {
		result, err := run(ceiling)
		if err != nil {
			t.Fatalf("ceiling %d: refused (%v); a one-row-per-table cut fits far below it, so the lever must measure the answer with its served-status head and serve", ceiling, err)
		}
		got, err := contractsv1.MeasureContextFabricResponse(result)
		if err != nil {
			t.Fatalf("ceiling %d: measure: %v", ceiling, err)
		}
		if got.Bytes > ceiling {
			t.Fatalf("ceiling %d: served %d bytes", ceiling, got.Bytes)
		}
		if !strings.HasPrefix(result.DirectJudgment, statusSentence(result.Status, SubjectResolution{})) {
			t.Fatalf("ceiling %d: head %q does not state served status %q", ceiling, result.DirectJudgment, result.Status)
		}
	}
}
