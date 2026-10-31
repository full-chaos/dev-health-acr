package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// censusTeamNarrowing decides only the census satisfiers (repositoriesOnly),
// so these tests isolate the census narrower from the pool re-decision: the
// pool is never decided here. in/out name the satisfiers inside and outside
// the requested teams; any other id has missing ownership. calls counts the
// census narrower's reads.
func censusTeamNarrowing(in, out map[string]bool, calls *int, failWith error) TeamNarrowingFunc {
	return func(_ context.Context, subjects []contextfabric.SubjectRef, repositoriesOnly bool) (TeamNarrowingDecision, error) {
		decision := TeamNarrowingDecision{In: map[string]bool{}, Out: map[string]bool{}}
		if !repositoriesOnly {
			return decision, nil
		}
		*calls++
		if failWith != nil {
			return TeamNarrowingDecision{}, failWith
		}
		for _, s := range subjects {
			switch {
			case in[s.CanonicalID]:
				decision.In[s.CanonicalID] = true
			case out[s.CanonicalID]:
				decision.Out[s.CanonicalID] = true
			}
		}
		return decision, nil
	}
}

func resolveTeamNarrowed(t *testing.T, principal storage.Principal, teams []string, ids []string, narrowing TeamNarrowingFunc) (contextfabric.SubjectResolution, *captureResolutionTracer, int) {
	t.Helper()
	backend := narrowingBackend()
	// As the graph projects them: a node with no team list carries "*".
	for _, nodes := range backend.searchResults {
		for _, node := range nodes {
			node.Attributes["authorization_teams"] = "*"
		}
	}
	for _, node := range backend.exactHints {
		node.Attributes["authorization_teams"] = "*"
	}
	deps := backend.deps()
	tracer := &captureResolutionTracer{}
	deps.ResolutionTracer = tracer
	calls := 0
	deps.CensusFunc = narrowingCensus(&calls, ids)
	deps.TeamNarrowing = narrowing
	request := testRequest()
	request.Question = narrowingQuestion
	request.RequestedScope.TeamIDs = teams
	resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), principal, request, testInterpreted("pull request 747"), deps, nil, nil, namedScopeAnchorFrame(nil), "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	return resolution, tracer, calls
}

func inTeam(ids ...string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// TestResolveSubjects_StalledHandleCensusCommitsInsideATeamNarrowing
// (CHAOS-12346 (ii)): an unrestricted principal's team narrowing runs the
// census, as a repository narrowing does, and its narrower keeps the one
// satisfier inside the requested teams' repositories; the trace line names the
// narrowing.
func TestResolveSubjects_StalledHandleCensusCommitsInsideATeamNarrowing(t *testing.T) {
	t.Parallel()
	narrowerCalls := 0
	narrowing := censusTeamNarrowing(inTeam(narrowingPRID(1)), inTeam(narrowingPRID(0), narrowingPRID(2)), &narrowerCalls, nil)
	resolution, tracer, calls := resolveTeamNarrowed(t, storage.Principal{OrgID: "org_1"}, []string{"team-b"}, allNarrowingIDs(), narrowing)
	if calls == 0 || narrowerCalls == 0 {
		t.Fatalf("census calls = %d, narrower calls = %d: want the census to run, narrowed by the team", calls, narrowerCalls)
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != narrowingPRID(1) {
		t.Fatalf("committed = %v, want only the pull request inside the team's repositories", ids)
	}
	rounds := narrowingEvents(tracer)
	if len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != "narrowed_to_one" || rounds[0].ShadowNarrowedFrom != 3 || rounds[0].ShadowNarrowedTo != 1 {
		t.Fatalf("evidence_round = %#v, want one event narrowed_to_one 3->1", rounds)
	}
}

// TestResolveSubjects_StalledHandleCensusUnderATeamNarrowingKeepsANamedSubject:
// a single satisfier outside the team still commits (Anchor B): a team
// narrowing has no in-query census filter, unlike a repository narrowing.
func TestResolveSubjects_StalledHandleCensusUnderATeamNarrowingKeepsANamedSubject(t *testing.T) {
	t.Parallel()
	narrowerCalls := 0
	narrowing := censusTeamNarrowing(nil, inTeam(narrowingPRID(2)), &narrowerCalls, nil)
	resolution, _, calls := resolveTeamNarrowed(t, storage.Principal{OrgID: "org_1"}, []string{"team-b"}, []string{narrowingPRID(2)}, narrowing)
	if calls == 0 {
		t.Fatal("census calls = 0, want the census to run")
	}
	if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 1 || ids[0] != narrowingPRID(2) {
		t.Fatalf("committed = %v, want the one satisfier, though outside the team: the subject is what the question names", ids)
	}
}

// TestResolveSubjects_StalledHandleCensusUnderATeamNarrowingKeepsTheClarification:
// no satisfier inside, several inside, a satisfier with missing ownership, or
// a failed ownership read: nothing commits, and the trace line names why.
func TestResolveSubjects_StalledHandleCensusUnderATeamNarrowingKeepsTheClarification(t *testing.T) {
	t.Parallel()
	all := inTeam(allNarrowingIDs()...)
	cases := []struct {
		name    string
		in, out map[string]bool
		fail    error
		narrow  string
	}{
		{"none inside", nil, all, nil, "narrowed_to_none"},
		{"two inside", inTeam(narrowingPRID(0), narrowingPRID(1)), inTeam(narrowingPRID(2)), nil, "narrowed_to_many"},
		{"one inside, one with missing ownership", inTeam(narrowingPRID(1)), inTeam(narrowingPRID(0)), nil, "satisfier_read_failed"},
		{"the ownership read fails", nil, nil, errors.New("graph unavailable"), "satisfier_read_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			narrowerCalls := 0
			resolution, tracer, _ := resolveTeamNarrowed(t, storage.Principal{OrgID: "org_1"}, []string{"team-b"}, allNarrowingIDs(), censusTeamNarrowing(tc.in, tc.out, &narrowerCalls, tc.fail))
			if ids := scopeAnchorCommittedIDs(resolution); len(ids) != 0 {
				t.Fatalf("committed = %v, want none", ids)
			}
			if rounds := narrowingEvents(tracer); len(rounds) != 1 || rounds[0].ShadowCallerNarrowing != tc.narrow {
				t.Fatalf("evidence_round = %#v, want one event with narrowing %q", rounds, tc.narrow)
			}
		})
	}
}

// TestResolveSubjects_StalledHandleCensusNeverRunsForARestrictedPrincipalWithATeamNarrowing:
// the census stays off where it is off today: a restricted principal, a team
// narrowing combined with a repository narrowing, or no team decider wired.
func TestResolveSubjects_StalledHandleCensusNeverRunsForARestrictedPrincipalWithATeamNarrowing(t *testing.T) {
	t.Parallel()
	narrowerCalls := 0
	narrowing := censusTeamNarrowing(inTeam(narrowingPRID(1)), nil, &narrowerCalls, nil)
	if _, _, calls := resolveTeamNarrowed(t, storage.Principal{OrgID: "org_1", RepositoryScopes: []string{narrowingRepos[1]}}, []string{"team-b"}, allNarrowingIDs(), narrowing); calls != 0 {
		t.Fatalf("restricted principal: census calls = %d, want 0", calls)
	}
	if _, _, calls := resolveTeamNarrowed(t, storage.Principal{OrgID: "org_1"}, []string{"team-b"}, allNarrowingIDs(), nil); calls != 0 {
		t.Fatalf("no team decider: census calls = %d, want 0", calls)
	}
}

// TestResolveSubjects_CallerHintRoundUsesTheTeamNarrowingVisibility: the
// caller-hint round passes the scoped-visibility gate for an unrestricted
// principal's team narrowing, as it does for a repository narrowing.
func TestResolveSubjects_CallerHintRoundUsesTheTeamNarrowingVisibility(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_explicit", Label: "Explicit"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, []string{"acme/repo-01"})
	node.Attributes["authorization_teams"] = "*"
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{SubjectKey(subject): node}}
	request := callerHintRequest(subject, "workbench")
	request.RequestedScope.TeamIDs = []string{"team-b"}
	tracer := &recordingTracer{}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{}, nil
	}
	calls := 0
	deps.TeamNarrowing = censusTeamNarrowing(nil, nil, &calls, nil)
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects error = %v", err)
	}
	events := tracer.evidenceRoundEvents()
	if len(events) != 1 || events[0].ShadowReason == string(ReasonScopedVisibility) {
		t.Fatalf("evidence_round = %+v, want the round past the scoped-visibility gate for a team-narrowed unrestricted principal", events)
	}
}
