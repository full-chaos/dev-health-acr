package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The tier read is bound to the target pull requests and to the requested
// projects' provider-qualified keys: it never takes the repository set.
func TestScopeExpander_LinkTierReadIsBoundToTheTargetPullRequests(t *testing.T) {
	const repoID = "22222222-2222-2222-2222-222222222222"
	client := &fakeClient{tables: []fakeTable{
		{match: "GROUP BY concat(p.provider, ':', p.id), toString(l.pr_repo_id), l.pr_number", rows: [][]any{
			{"github:P", repoID, uint32(2), "native"},
		}},
		{match: "GROUP BY p.id, toString(l.pr_repo_id), l.pr_number", rows: [][]any{
			{"P", repoID, uint32(2), "native"},
		}},
		{match: "FROM git_pull_requests AS p FINAL", rows: [][]any{
			{repoID, uint32(1)}, {repoID, uint32(2)}, {repoID, uint32(3)},
		}},
		{match: "argMax(l.provenance, l.link_rank)", rows: [][]any{
			{repoID, "example-org/repo", "github:P", "native", []string{"github:P"}},
		}},
	}}
	origin := projectSubject("github", "P")
	result, err := devhealthfacts.NewScopeExpander(client).ExpandFactScope(context.Background(), contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: "org-link-tier-bound", RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{origin},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           2,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	var tierQueries []capturedQuery
	for _, query := range client.queries {
		if strings.Contains(query.statement, "GROUP BY") && strings.HasSuffix(query.statement, "toString(l.pr_repo_id), l.pr_number") {
			tierQueries = append(tierQueries, query)
		}
	}
	if len(tierQueries) != 1 {
		t.Fatalf("%d tier statements, want 1", len(tierQueries))
	}
	got := map[string][]string{}
	for _, binding := range tierQueries[0].bindings {
		if values, ok := binding.Value.([]string); ok {
			got[binding.Name] = values
		}
	}
	if _, unbounded := got["repo_ids"]; unbounded {
		t.Fatalf("tier read still takes the repository set: %v", got)
	}
	if keys := got["pull_request_keys"]; len(keys) != len(result.Targets) || len(keys) != 3 {
		t.Fatalf("pull_request_keys = %v, want one key per target pull request (%d targets)", keys, len(result.Targets))
	}
	if keys := got["project_ids"]; len(keys) != 1 || keys[0] != "github:P" {
		t.Fatalf("project_ids = %v, want the provider-qualified key github:P", keys)
	}
}
