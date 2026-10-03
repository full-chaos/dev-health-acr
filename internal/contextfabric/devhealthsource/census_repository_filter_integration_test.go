package devhealthsource_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

func TestCensusRepositoryFilterAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Hour)
	query, direct := orgIsolationClickHouseFixture(t)
	orgA, orgB := sharedTestOrgID(t)+"-a", sharedTestOrgID(t)+"-b"
	const repos = 26
	for i := 0; i < repos; i++ {
		id := o3UUID(fmt.Sprintf("%s-repo-%02d", orgA, i))
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, id, orgA, fmt.Sprintf("acme/repo-%02d", i), "github", now); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, id, orgA, uint32(747), "PR 747", "open", now); err != nil {
			t.Fatalf("seed pull request: %v", err)
		}
	}
	// A repository whose owner only shares a prefix with "acme".
	lookalike := o3UUID(orgA + "-lookalike")
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, lookalike, orgA, "acmex/repo-00", "github", now); err != nil {
		t.Fatalf("seed lookalike repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, lookalike, orgA, uint32(747), "PR 747", "open", now); err != nil {
		t.Fatalf("seed lookalike pull request: %v", err)
	}
	// A repository stored with padding around its name still matches its clean slug.
	padded := o3UUID(orgA + "-padded")
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, padded, orgA, " Padded/Repo ", "github", now); err != nil {
		t.Fatalf("seed padded repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, padded, orgA, uint32(747), "PR 747", "open", now); err != nil {
		t.Fatalf("seed padded pull request: %v", err)
	}
	// Another organization owns a repository with the same slug and the same number.
	otherRepo := o3UUID(orgB + "-repo")
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, otherRepo, orgB, "acme/repo-25", "github", now); err != nil {
		t.Fatalf("seed other org repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, otherRepo, orgB, uint32(747), "PR 747", "open", now); err != nil {
		t.Fatalf("seed other org pull request: %v", err)
	}

	census := devhealthsource.NewCensusFunc(query)
	run := func(slugs []string) graphrank.CensusOutcome {
		t.Helper()
		outcome, err := census(graphrank.WithCensusRepositoryFilter(ctx, slugs), orgA, contextfabric.SubjectPullRequest, "747", true, "", "", false)
		if err != nil {
			t.Fatalf("census %v: %v", slugs, err)
		}
		return outcome
	}

	cases := []struct {
		name    string
		slugs   []string
		count   int
		applied bool
	}{
		{"no filter", nil, repos + 2, false},
		{"one repository, case-folded", []string{"ACME/Repo-25"}, 1, true},
		{"two repositories", []string{"acme/repo-00", "acme/repo-01"}, 2, true},
		{"owner wildcard", []string{"acme/*"}, repos, true},
		{"padded stored name", []string{"padded/repo"}, 1, true},
		{"unknown repository", []string{"acme/elsewhere"}, 0, true},
		{"other owner wildcard", []string{"nobody/*"}, 0, true},
		{"star falls back to unfiltered", []string{"*"}, repos + 2, false},
	}
	for _, tc := range cases {
		outcome := run(tc.slugs)
		if outcome.Count != tc.count || outcome.RepositoryFilterApplied != tc.applied || outcome.ClosureMismatch {
			t.Fatalf("%s: count=%d applied=%v mismatch=%v, want count=%d applied=%v", tc.name, outcome.Count, outcome.RepositoryFilterApplied, outcome.ClosureMismatch, tc.count, tc.applied)
		}
	}

	one := run([]string{"acme/repo-25"})
	if one.SatisfierCanonicalID == "" {
		t.Fatalf("a one-repository census must name its satisfier")
	}
	two := run([]string{"acme/repo-00", "acme/repo-01"})
	if len(two.SatisfierCanonicalIDs) != 2 {
		t.Fatalf("satisfier ids = %v, want 2", two.SatisfierCanonicalIDs)
	}
}
