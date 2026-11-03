package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// CHAOS-12642: a pull_request_repo_reference value (owner/repo#N) counts the pull
// request with that number in the GitHub repository of that name, and nothing
// else: not the same number in another repository, not a GitLab merge request
// (GitLab writes "#" for an issue), and not another organization's row.
func TestAPullRequestReferenceCountsOneRepositoryRowAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	query, direct := orgIsolationClickHouseFixture(t)
	orgA, orgB := sharedTestOrgID(t)+"-a", sharedTestOrgID(t)+"-b"
	repo := func(org, name, provider string) string {
		t.Helper()
		id := o3UUID(org + "-12642-" + name)
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, id, org, name, provider, now); err != nil {
			t.Fatalf("seed repo %s: %v", name, err)
		}
		return id
	}
	pullRequest := func(org, repoID string, number uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, repoID, org, number, "pr", "open", now); err != nil {
			t.Fatalf("seed pull request: %v", err)
		}
	}
	widget := repo(orgA, "Acme/Widget", "github")
	pullRequest(orgA, widget, 8)
	pullRequest(orgA, repo(orgA, "acme/gadget", "github"), 8)
	pullRequest(orgA, repo(orgA, "group/project", "gitlab"), 8)
	pullRequest(orgB, repo(orgB, "acme/widget", "github"), 8)

	census := devhealthsource.NewCensusFunc(query)
	for _, tc := range []struct {
		value string
		count int
	}{
		{"acme/widget#8", 1},
		{"ACME/widget#8", 1},
		{"acme/widget#7", 0},
		{"acme/elsewhere#8", 0},
		{"group/project#8", 0},
		{"8", 3},
	} {
		outcome, err := census(ctx, orgA, contextfabric.SubjectPullRequest, tc.value, true, "", "", false)
		if err != nil {
			t.Fatalf("census %q: %v", tc.value, err)
		}
		if outcome.Count != tc.count || outcome.ClosureMismatch {
			t.Fatalf("census %q: count=%d mismatch=%v, want %d", tc.value, outcome.Count, outcome.ClosureMismatch, tc.count)
		}
		if want := "pull_request:" + widget + ":8"; tc.count == 1 && outcome.SatisfierCanonicalID != want {
			t.Fatalf("census %q satisfier = %q, want %q", tc.value, outcome.SatisfierCanonicalID, want)
		}
	}
}
