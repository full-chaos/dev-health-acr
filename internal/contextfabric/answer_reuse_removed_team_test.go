package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func reuseRemovedTeamCount(result InvestigationResult) int {
	n := 0
	for _, l := range result.Limitations {
		if l == contractsv1.ContextFabricRemovedTeamLimitation {
			n++
		}
	}
	return n
}

func reuseWithRemovedTeams(t *testing.T, stored InvestigationResult, removed []SubjectRef) InvestigationResult {
	t.Helper()
	graphContext := productionShapedGraphContext([]string{reuseCitationRef}, []string{reuseNodeRef})
	graphContext.RemovedTeams = removed
	engine := reuseDegradeEngine(t, stored, graphContext, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want a reuse hit")
	}
	return result
}

func TestAReusedAnswerGainsTheRemovedTeamLimitationTheGraphNowProves(t *testing.T) {
	t.Parallel()
	stored := storedResultWithCandidateEvidence()
	result := reuseWithRemovedTeams(t, stored, []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}})
	if got := reuseRemovedTeamCount(result); got != 1 {
		t.Fatalf("reused limitations = %q: removed-team sentence appears %d times, want once", result.Limitations, got)
	}
}

func TestAReusedAnswerDropsARemovedTeamLimitationTheGraphNoLongerProves(t *testing.T) {
	t.Parallel()
	stored := storedResultWithCandidateEvidence()
	stored.Limitations = append(append([]string{}, stored.Limitations...), contractsv1.ContextFabricRemovedTeamLimitation)
	result := reuseWithRemovedTeams(t, stored, nil)
	if got := reuseRemovedTeamCount(result); got != 0 {
		t.Fatalf("reused limitations = %q: the removed-team sentence is still served after the graph cleared the team", result.Limitations)
	}
}

func TestAReusedAnswerWithTheLimitationAlreadyStatedKeepsItOnce(t *testing.T) {
	t.Parallel()
	stored := storedResultWithCandidateEvidence()
	stored.Limitations = append(append([]string{}, stored.Limitations...), contractsv1.ContextFabricRemovedTeamLimitation)
	result := reuseWithRemovedTeams(t, stored, []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}})
	if got := reuseRemovedTeamCount(result); got != 1 {
		t.Fatalf("reused limitations = %q: want the sentence once, got %d", result.Limitations, got)
	}
	if !strings.Contains(strings.Join(result.Limitations, "\n"), contractsv1.ContextFabricRemovedTeamLimitation) {
		t.Fatal("sentence missing")
	}
}

func TestAReusedAnswerWithNoEvidenceRefsStillRechecksTheRemovedTeam(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}
	stored := validInvestigationResult()
	stored.ResultID = "result_reused_no_refs"
	stored.RequestID = "request_original_nr"
	stored.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}}
	stored.EvidenceRefIDs = []string{}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	if refs := reuseEvidenceRefsToRecheck(stored); len(refs) != 0 {
		t.Fatalf("fixture carries evidence refs %v; this test needs a stored answer with none", refs)
	}
	graphContext := GraphContext{Resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}}, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, RemovedTeams: []SubjectRef{team}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}},
			context:    graphContext,
		},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return stored, true, nil
		}),
	})
	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want a reuse hit")
	}
	if got := reuseRemovedTeamCount(result); got != 1 {
		t.Fatalf("reused limitations = %q: removed-team sentence appears %d times, want once even when the stored answer cites no evidence", result.Limitations, got)
	}
}
