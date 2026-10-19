package contextfabric

import (
	"context"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
