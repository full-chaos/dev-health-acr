package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// linkSetFake is projectReachFake with a link set of n pull requests of team.
func linkSetFake(team string, n int) *fakeClient {
	const reachedRepo = "00000000-0000-4000-8000-000000000042"
	repos, numbers, teams, issues := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	ranks := make([]uint8, n)
	for i := range n {
		repos[i], numbers[i], teams[i], ranks[i] = reachedRepo, fmt.Sprint(i+1), team, 3
	}
	return &fakeClient{tables: []fakeTable{
		{match: "SELECT DISTINCT team_id FROM team_project_ownership", rows: [][]any{{team}}},
		{match: teamLinkedSetMarker, rows: [][]any{{repos, numbers, teams, issues, ranks}}},
		{match: "uniqExact(lr.pr_number)", rows: [][]any{{team, reachedRepo, "acme/linked", int64(3), int64(n)}}},
		{match: "AS lr ON pr.org_id", rows: [][]any{{team, reachedRepo, int64(n), int64(0), int64(0)}}},
	}}
}

func readTeamPullRequests(t *testing.T, client *fakeClient, teams ...string) (contextfabric.FactProviderResult, error) {
	t.Helper()
	subjects := make([]contextfabric.SubjectRef, 0, len(teams))
	for _, team := range teams {
		subjects = append(subjects, teamSubject(team))
	}
	return findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactPullRequests).ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactPullRequests, Subjects: subjects,
	})
}

// A team that reaches more pull requests than one sent set carries is served
// the existing unavailable state with the existing read-failure reason, never
// a partial count or an empty set; 143,000 linked pull requests, where main
// reaches the 256 MiB statement cap, are still served.
func TestCHAOS10430AnOverBoundLinkSetIsServedUnavailable(t *testing.T) {
	served, err := readTeamPullRequests(t, linkSetFake("CHAOS", 143_000), "CHAOS")
	if err != nil {
		t.Fatalf("WANT 143,000 linked pull requests served: %v: %s", err, readFailureCause(err))
	}
	wantInt(t, factFor(t, served, teamSubject("CHAOS")).Fields, "project_reached_repository_count", 1)

	result, err := readTeamPullRequests(t, linkSetFake("CHAOS", 400_001), "CHAOS")
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) {
		t.Fatalf("WANT a FactReadFailure past the 400,000-row bound, got %T %v", err, err)
	}
	if failure.State != contextfabric.SourceUnavailable || failure.Reason != "devhealthfacts: query team pull requests failed" || failure.Class != contextfabric.FactReadFailureReadFailed {
		t.Fatalf("WANT unavailable, the read-failure reason and class; got %v %q %v", failure.State, failure.Reason, failure.Class)
	}
	if !strings.Contains(readFailureCause(err), "over the 400000 rows one sent table carries") {
		t.Fatalf("cause = %q, want the named set bound", readFailureCause(err))
	}
	if len(result.Facts) != 0 {
		t.Fatalf("a failed read served facts: %#v", result.Facts)
	}
}

// A requested team with no linked pull request keeps its own documented state
// beside a team that has some: the team filter on the sent set drops no team.
func TestCHAOS10430ARequestedTeamWithoutLinkRowsKeepsItsEmptyState(t *testing.T) {
	result, err := readTeamPullRequests(t, linkSetFake("CHAOS", 3), "CHAOS", "EMPTY")
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	wantInt(t, factFor(t, result, teamSubject("CHAOS")).Fields, "project_reached_repository_count", 1)
	key := contextfabric.FactSubjectKey(teamSubject("EMPTY"))
	if _, ok := result.NotApplicableSubjects[key]; !ok {
		t.Fatalf("WANT team EMPTY not applicable (owns no repository), got %v", result.NotApplicableSubjects)
	}
	if !strings.Contains(result.SubjectReasons[key], "the team owns no repository") {
		t.Fatalf("WANT team EMPTY's documented reason, got %q", result.SubjectReasons[key])
	}
}
