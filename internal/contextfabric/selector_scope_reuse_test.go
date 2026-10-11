package contextfabric

import (
	"context"
	"testing"
)

func selectorAsk(t *testing.T, engine *Engine, projects, teams []string) InvestigationResult {
	t.Helper()
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.ProjectIDs = projects
	request.RequestedScope.TeamIDs = teams
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate error = %v", err)
	}
	return result
}

func TestAnswerReuseFollowsTheCallerProjectScope(t *testing.T) {
	t.Parallel()
	engine, _, _ := scopedReuseEngine(t, newScopedReuseStore())
	if selectorAsk(t, engine, []string{"proj-a"}, nil).Reused {
		t.Fatal("first ask reused")
	}
	if selectorAsk(t, engine, []string{"proj-b"}, nil).Reused {
		t.Fatal("project scope proj-b served from the proj-a answer")
	}
	if selectorAsk(t, engine, nil, nil).Reused {
		t.Fatal("unscoped ask served from a project-scoped answer")
	}
	if !selectorAsk(t, engine, []string{"proj-a"}, nil).Reused {
		t.Fatal("same project scope not reused")
	}
	if selectorAsk(t, engine, []string{"Proj-A"}, nil).Reused {
		t.Fatal("identifier case folded: Proj-A served from proj-a")
	}
}

func TestAnswerReuseFollowsTheCallerTeamScope(t *testing.T) {
	t.Parallel()
	engine, _, _ := scopedReuseEngine(t, newScopedReuseStore())
	if selectorAsk(t, engine, nil, []string{"team-a"}).Reused {
		t.Fatal("first ask reused")
	}
	if selectorAsk(t, engine, nil, []string{"team-b"}).Reused {
		t.Fatal("team-b served from the team-a answer")
	}
	if !selectorAsk(t, engine, nil, []string{"team-a"}).Reused {
		t.Fatal("same team scope not reused")
	}
	if selectorAsk(t, engine, []string{"team-a"}, nil).Reused {
		t.Fatal("project id team-a served from the team scope team-a")
	}
}

func TestAnswerReuseSelectorOrderAndEmptyListDoNotSplitTheKey(t *testing.T) {
	t.Parallel()
	engine, _, _ := scopedReuseEngine(t, newScopedReuseStore())
	selectorAsk(t, engine, []string{"p1", "p2"}, []string{"t1", "t2"})
	if !selectorAsk(t, engine, []string{"p2", "p1"}, []string{"t2", "t1"}).Reused {
		t.Fatal("selector order splits the reuse key")
	}
	if selectorAsk(t, engine, []string{"p1", "p2"}, []string{"t1"}).Reused {
		t.Fatal("a narrower team set served from the wider answer")
	}
	selectorAsk(t, engine, nil, nil)
	if !selectorAsk(t, engine, []string{}, nil).Reused {
		t.Fatal("an empty selector list splits the key from an absent scope")
	}
}

func TestRequestScopeTimeAxisKeyWithoutSelectorsIsUnchanged(t *testing.T) {
	t.Parallel()
	request := validInvestigationRequestWithConfirmedWindow()
	if got := RequestScopeTimeAxisKey(request, "current"); got != "current" {
		t.Fatalf("unscoped key = %q", got)
	}
	request.RequestedScope.RepositorySlugs = []string{"acme/repo-25"}
	repoOnly := RequestScopeTimeAxisKey(request, "current")
	if repoOnly != repositoryScopeTimeAxisKey(request, "current") || repoOnly == "current" {
		t.Fatalf("repository-only key changed: %q", repoOnly)
	}
	request.RequestedScope.ProjectIDs = []string{"p"}
	if RequestScopeTimeAxisKey(request, "current") == repoOnly {
		t.Fatal("project selector missing from the key")
	}
}

func TestAnswerReuseRecordsAScopeIdentityMissForARowKeyedWithoutSelectors(t *testing.T) {
	t.Parallel()
	store := newScopedReuseStore()
	telemetry := &recordingTelemetry{}
	engine, _, _ := scopedReuseEngine(t, store, telemetry)
	selectorAsk(t, engine, nil, nil)
	if got := telemetry.answerReuseOutcomes[len(telemetry.answerReuseOutcomes)-1]; got != AnswerReuseMissNoCandidate {
		t.Fatalf("first outcome = %q", got)
	}
	// The unscoped row is the repository-only key of a selector request.
	if selectorAsk(t, engine, []string{"proj-a"}, nil).Reused {
		t.Fatal("a selector request was served a row saved without selectors")
	}
	found := false
	for _, outcome := range telemetry.answerReuseOutcomes {
		found = found || outcome == AnswerReuseMissScopeIdentity
	}
	if !found {
		t.Fatalf("outcomes = %v, want %q", telemetry.answerReuseOutcomes, AnswerReuseMissScopeIdentity)
	}
}

// The request boundary rejects duplicate and blank ids, so the key's own
// de-duplication is the second line: it is pinned at the key function.
func TestRequestScopeTimeAxisKeyIgnoresSelectorDuplicatesAndBlanks(t *testing.T) {
	t.Parallel()
	key := func(projects, teams []string) string {
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestedScope.ProjectIDs = projects
		request.RequestedScope.TeamIDs = teams
		return RequestScopeTimeAxisKey(request, "current")
	}
	if key([]string{"b", "a", "b"}, []string{"y", "x"}) != key([]string{"a", "b"}, []string{"x", "y"}) {
		t.Fatal("order or duplicates split the key")
	}
	if key([]string{" ", ""}, []string{}) != "current" {
		t.Fatal("blank selectors changed an unscoped key")
	}
	if key([]string{"a"}, nil) == key(nil, []string{"a"}) {
		t.Fatal("project and team ids collide")
	}
}
