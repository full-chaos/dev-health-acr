package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A team that owns a project and no repository reaches pull requests through
// the project's linked pull request rows; every repo-level kind names the
// project-reached repositories it has no rows for instead of serving a zero.
func projectReachFake() *fakeClient {
	const reachedRepo = "00000000-0000-4000-8000-000000000042"
	return &fakeClient{tables: []fakeTable{
		{match: "uniqExact(lr.pr_number)", rows: [][]any{{"CHAOS", reachedRepo, "acme/linked", int64(2), int64(3)}}},
		{match: "INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id", rows: [][]any{{"CHAOS", reachedRepo, int64(3), int64(1), int64(0)}}},
	}}
}

func TestTeamRollupPullRequestsServeProjectReachedLinkedPullRequests(t *testing.T) {
	provider := findProvider(t, devhealthfacts.NewProviders(projectReachFake()), contextfabric.FactPullRequests)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactPullRequests,
		Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d (state %q, reason %q), want 1 for a team that reaches work through a project", len(result.Facts), result.State, result.Reason)
	}
	fields := result.Facts[0].Fields
	wantInt(t, fields, "owned_repository_count", 0)
	wantInt(t, fields, "project_reached_repository_count", 1)
	wantInt(t, fields, "pull_requests_opened_window", 3)
	wantInt(t, fields, "pull_requests_merged_window", 1)
	if got := stringField(t, result.Facts[0], "repository_link_basis"); got != "explicit_text" {
		t.Fatalf("repository_link_basis = %q, want explicit_text (link rank 2)", got)
	}
}

func TestTeamRollupDeploymentsNameProjectReachedRepositoriesWithoutRows(t *testing.T) {
	provider := findProvider(t, devhealthfacts.NewProviders(projectReachFake()), contextfabric.FactDeployments)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactDeployments,
		Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if result.State != contextfabric.SourceNotApplicable {
		t.Fatalf("state = %q, want not_applicable", result.State)
	}
	if !strings.Contains(result.Reason, "1 repositories reached through owned projects") {
		t.Fatalf("reason = %q, want the cause naming the project-reached repositories", result.Reason)
	}
}
