package contextfabric

import (
	"context"
	"errors"
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

type removedTeamReuseGraph struct {
	graphReaderStub
	t       *testing.T
	removed []SubjectRef
	err     error
	reads   int
}

func (g *removedTeamReuseGraph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.t.Error("a stored answer citing no evidence ran a full discovery on reuse")
	return GraphContext{}, errors.New("unexpected discovery")
}

func (g *removedTeamReuseGraph) RemovedTeams(context.Context, storage.Principal, ResolvedGraphBinding, []SubjectRef) ([]SubjectRef, error) {
	g.reads++
	return g.removed, g.err
}

func reuseNoEvidenceTeamAnswer(t *testing.T, stored func(*InvestigationResult), graph *removedTeamReuseGraph) (InvestigationResult, error) {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}
	answer := validInvestigationResult()
	answer.ResultID = "result_reused_no_refs"
	answer.RequestID = "request_original_nr"
	answer.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}}
	answer.EvidenceRefIDs = []string{}
	answer.Completeness = ComputeAnswerCompleteness(answer)
	if stored != nil {
		stored(&answer)
	}
	if refs := reuseEvidenceRefsToRecheck(answer); len(refs) != 0 {
		t.Fatalf("fixture carries evidence refs %v; this test needs a stored answer with none", refs)
	}
	graph.t = t
	graph.graphReaderStub = graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graph,
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return answer, true, nil
		}),
	})
	return engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
}

func TestAReusedAnswerWithNoEvidenceRefsStillRechecksTheRemovedTeam(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}
	graph := &removedTeamReuseGraph{removed: []SubjectRef{team}}
	result, err := reuseNoEvidenceTeamAnswer(t, nil, graph)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("result.Reused = false, want a reuse hit")
	}
	if got := reuseRemovedTeamCount(result); got != 1 {
		t.Fatalf("reused limitations = %q: removed-team sentence appears %d times, want once even when the stored answer cites no evidence", result.Limitations, got)
	}
	if graph.reads != 1 {
		t.Fatalf("removed-team reads = %d, want exactly one bounded read", graph.reads)
	}
}

func TestAnEmptyRemovedTeamRecheckServesTheReusedAnswerUnchanged(t *testing.T) {
	t.Parallel()
	graph := &removedTeamReuseGraph{}
	result, err := reuseNoEvidenceTeamAnswer(t, nil, graph)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused || reuseRemovedTeamCount(result) != 0 || len(result.Limitations) != 0 {
		t.Fatalf("reused=%v limitations=%q: an empty recheck must serve the stored answer unchanged", result.Reused, result.Limitations)
	}
	if graph.reads != 1 {
		t.Fatalf("removed-team reads = %d, want exactly one", graph.reads)
	}
}

func TestAReusedNoEvidenceAnswerDropsTheSentenceWhenTheRecheckFindsNoRemovedTeam(t *testing.T) {
	t.Parallel()
	graph := &removedTeamReuseGraph{}
	result, err := reuseNoEvidenceTeamAnswer(t, func(a *InvestigationResult) {
		a.Limitations = append(append([]string{}, a.Limitations...), contractsv1.ContextFabricRemovedTeamLimitation)
	}, graph)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused || reuseRemovedTeamCount(result) != 0 {
		t.Fatalf("reused=%v limitations=%q: the sentence must go when the recheck finds no removed team", result.Reused, result.Limitations)
	}
}

func TestAFailedRemovedTeamRecheckRefusesTheReuseHit(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:tango", Label: "Tango"}
	graph := &removedTeamReuseGraph{t: t, err: errors.New("graph unavailable")}
	engine := mustReuseTestEngine(t, EngineDependencies{Graph: graph, Results: &resultStoreStub{}})
	verdict := engine.reuseRemovedTeamRecheck(context.Background(), reusePrincipal(), ResolvedGraphBinding{}, []SubjectRef{team})
	if !verdict.Refused || verdict.Outcome != AnswerReuseMissRecheckUnavailable {
		t.Fatalf("verdict = %+v, want a refusal with outcome %q: a failed read is never read as \"not removed\"", verdict, AnswerReuseMissRecheckUnavailable)
	}
}
