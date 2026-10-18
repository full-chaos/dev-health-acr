package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A pull request carries, per requested project, the tier of that project's
// own link row; a project that reached it through the repository only has none.
func TestScopeExpander_ProjectToPullRequest_CarriesTheLinkTier(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at, orgID)
	const otherProjectID = "org-4099-scope-expander:gitlab:71133892"
	const otherProjectKey = "acme/web"
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	exec("second project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		otherProjectID, orgID, "acme web", otherProjectKey, "gitlab", "", "https://gitlab.com/"+otherProjectKey, uint8(1), at)
	exec("pull request 3", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4099GitLabRepoID, orgID, uint32(3), "Text linked", "open", at, at, "feat/text", "")
	exec("second project work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/web#7", chaos4099GitLabRepoID, orgID, "GitLab issue (second project)", "open", "", "", otherProjectKey, at)
	seedIssueLink(t, ctx, direct, orgID, "gitlab:acme/web#7", chaos4099GitLabRepoID, 3, "explicit_text", at)

	first := chaos4099ProjectSubject(t)
	secondID, omitted, err := identity.Derive(identity.KindProject, []string{"gitlab", otherProjectID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive second project canonical id: err=%v omitted=%v", err, omitted)
	}
	second := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: secondID, Label: "acme web"}

	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{first, second},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	prefix := "pull_request:" + chaos4099GitLabRepoID + ":"
	want := map[string]map[string]string{
		prefix + "1": {contextfabric.FactSubjectKey(first): "native"},
		prefix + "2": {},
		prefix + "3": {contextfabric.FactSubjectKey(second): "explicit_text"},
	}
	if len(result.Targets) != len(want) {
		t.Fatalf("targets = %+v, want %d pull requests", result.Targets, len(want))
	}
	for _, target := range result.Targets {
		tiers, expected := want[target.CanonicalID]
		if !expected {
			t.Fatalf("unexpected target %q", target.CanonicalID)
		}
		got := result.TargetLinkTier[contextfabric.FactSubjectKey(target)]
		if len(got) != len(tiers) {
			t.Fatalf("pull request %q link tiers = %v, want %v (all %v)", target.CanonicalID, got, tiers, result.TargetLinkTier)
		}
		for root, tier := range tiers {
			if got[root] != tier {
				t.Fatalf("pull request %q link tier of %q = %q, want %q (all %v)", target.CanonicalID, root, got[root], tier, result.TargetLinkTier)
			}
		}
	}
}
