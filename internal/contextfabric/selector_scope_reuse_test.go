package contextfabric

import (
	"context"
	"testing"
	"time"
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

func TestRequestScopeTimeAxisKeyFramesSelectorsWithoutCollision(t *testing.T) {
	t.Parallel()
	key := func(projects, teams []string) string {
		request := validInvestigationRequestWithConfirmedWindow()
		request.RequestedScope.ProjectIDs = projects
		request.RequestedScope.TeamIDs = teams
		return RequestScopeTimeAxisKey(request, "current")
	}
	if key([]string{"a\x00t:b"}, nil) == key([]string{"a"}, []string{"b"}) {
		t.Fatal("a delimiter inside an id collides with a project plus a team")
	}
	if key([]string{"a", "b"}, nil) == key([]string{"a\x00b"}, nil) {
		t.Fatal("two project ids collide with one id holding the separator")
	}
}

func TestAnswerReuseTupleProbeSurvivesAnOversizedScopedKey(t *testing.T) {
	t.Parallel()
	store := newScopedReuseStore()
	telemetry := &recordingTelemetry{}
	engine, _, _ := scopedReuseEngine(t, store, telemetry)
	// A restricted caller's scoped key carries a grant digest too, which is what
	// pushes a repository, project and team scoped key past the column bound.
	principal := acceptancePrincipal()
	principal.RepositoryScopes = []string{"acme/repo-25"}
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow()); err != nil {
		t.Fatalf("Investigate error = %v", err)
	}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	request := validInvestigationRequestWithConfirmedWindow()
	request.TimeContext = TimeContext{Axis: TemporalRange, Start: &start, End: &end}
	request.RequestedScope.RepositorySlugs = []string{"acme/repo-25"}
	request.RequestedScope.ProjectIDs = []string{"proj-a"}
	request.RequestedScope.TeamIDs = []string{"team-a"}
	axisKey := composeTimeAxisKey(TimeAxisKeyFor(request.TimeContext), "")
	if got := GrantScopedTimeAxisKey(principal, RequestScopeTimeAxisKey(request, axisKey)); got != "" {
		t.Fatalf("scoped key fits the bound (%d): the row does not exercise the oversize path", len(got))
	}
	tupleKey := GrantScopedTimeAxisKey(principal, axisKey)
	if tupleKey == "" {
		t.Fatal("the work-item tuple key does not fit the bound")
	}
	for _, saved := range store.rows {
		store.rows[tupleKey] = saved
		break
	}
	store.tuple[tupleKey] = true
	telemetry.storedResultAuthorizations = nil
	_, _ = engine.Investigate(context.Background(), principal, request)
	if len(telemetry.storedResultAuthorizations) == 0 {
		t.Fatalf("an oversized scoped key hid the work-item tuple row from its own gate; lookups = %v", store.lookups)
	}
}

func TestAnswerReuseScopeIdentityAttributionReadsEachKeyOnce(t *testing.T) {
	t.Parallel()
	store := newScopedReuseStore()
	engine, _, _ := scopedReuseEngine(t, store)
	selectorAsk(t, engine, nil, nil)
	store.lookups = nil
	selectorAsk(t, engine, []string{"proj-a"}, nil)
	seen := map[string]int{}
	for _, key := range store.lookups {
		seen[key]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Fatalf("key %q read %d times in one lookup: %v", key, n, store.lookups)
		}
	}
	if len(store.lookups) < 2 {
		t.Fatalf("lookups = %v, want the scoped key and the unscoped probe", store.lookups)
	}
}
