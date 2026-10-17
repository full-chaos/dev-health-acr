package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A pull request reached through an issue of the project carries the tier of
// that link row; one reached through its repository only carries none.
func TestScopeExpander_ProjectToPullRequest_CarriesTheLinkTier(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at, orgID)
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4099GitLabRepoID, orgID, uint32(3), "Text linked", "open", at, at, "feat/text", ""); err != nil {
		t.Fatal(err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#45", chaos4099GitLabRepoID, orgID, "GitLab issue (text link)", "open", "", "", chaos4099GitLabProjectKey, at); err != nil {
		t.Fatal(err)
	}
	seedIssueLink(t, ctx, direct, orgID, "gitlab:acme/api#45", chaos4099GitLabRepoID, 3, "explicit_text", at)

	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	want := map[string]string{
		"pull_request:" + chaos4099GitLabRepoID + ":1": "native",
		"pull_request:" + chaos4099GitLabRepoID + ":2": "",
		"pull_request:" + chaos4099GitLabRepoID + ":3": "explicit_text",
	}
	if len(result.Targets) != len(want) {
		t.Fatalf("targets = %+v, want %d pull requests", result.Targets, len(want))
	}
	for _, target := range result.Targets {
		tier, expected := want[target.CanonicalID]
		if !expected {
			t.Fatalf("unexpected target %q", target.CanonicalID)
		}
		if got := result.TargetLinkTier[contextfabric.FactSubjectKey(target)]; got != tier {
			t.Fatalf("pull request %q link tier = %q, want %q (all tiers %v)", target.CanonicalID, got, tier, result.TargetLinkTier)
		}
	}
}
