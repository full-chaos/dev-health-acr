package devhealthfacts_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The entity tree is Repository <> Pull request <> Issue <> Project. A
// project reaches repositories only through its issues' linked pull request
// rows (work_graph_issue_pr, whose repo_id is the PULL REQUEST's repository);
// a team reaches repositories only through its ownership rows. These rows
// seed the case where the issue's own repository column and the
// attribution row point at repository A while the tree points at B.
const (
	entityTreeProject  = "acme-tree"
	entityTreeTeam     = "TREE-TEAM"
	entityTreeRepoA    = "a1198fbc-1945-3717-05d8-eb78866b4e01"
	entityTreeRepoB    = "b2298fbc-1945-3717-05d8-eb78866b4e02"
	entityTreeSlugA    = "acme/column-repo"
	entityTreeSlugB    = "acme/linked-repo"
	entityTreeIssueID  = "tree#1"
	entityTreeLinkedPR = uint32(7)
)

func seedEntityTreeFixture(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, orgID string, at time.Time) {
	t.Helper()
	for _, statement := range devhealthschema.DDL("teams", "work_items", "work_item_team_attributions", "repos", "git_pull_requests", "git_pull_request_reviews", "projects", "team_project_ownership", "team_repo_ownership", "work_graph_issue_pr") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	seed("project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeProject, orgID, "Tree", entityTreeProject, "jira", "", "", uint8(1), at)
	seed("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeTeam, "Tree team", "", at, orgID, "jira", entityTreeTeam, uint8(1))
	seed("repo A", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, entityTreeRepoA, orgID, entityTreeSlugA, "github", at)
	seed("repo B", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, entityTreeRepoB, orgID, entityTreeSlugB, "github", at)
	// The issue's own repository column says A.
	seed("issue", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeIssueID, entityTreeRepoA, orgID, "issue", "open", "", "", entityTreeProject, at, "jira")
	// Its linked pull request lives in B (native tier).
	seed("link", `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeRepoB, entityTreeIssueID, entityTreeLinkedPR, float32(1), "native", "", at, orgID)
	prInsert := `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`
	seed("pr in A", prInsert, entityTreeRepoA, orgID, uint32(1), "A pr", "open", at, at, "a", "")
	seed("pr in B", prInsert, entityTreeRepoB, orgID, entityTreeLinkedPR, "B pr", "open", at, at, "b", "")
	// The attribution row names A; ownership says the team owns B only.
	seed("attribution", `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, entityTreeRepoA, entityTreeIssueID, entityTreeTeam, "native_team", uint8(1), "high", at)
	seed("team owns B", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		orgID, "github", entityTreeTeam, entityTreeRepoB, entityTreeSlugB, "exact", "native", uint8(1), uint16(1), int32(1), at.Add(-24*time.Hour), nil, at)
}

func entityTreeSlugs(targets []contextfabric.SubjectRef) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.Label)
	}
	sort.Strings(out)
	return out
}

func TestFactScopeEntityTree_ProjectRepositoryFollowsTheLinkedPullRequest(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	seedEntityTreeFixture(t, ctx, direct, orgID, time.Now().UTC())
	projectID, omitted, err := identity.Derive(identity.KindProject, []string{"jira", entityTreeProject}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectID, Label: "Tree"}
	expander := devhealthfacts.NewScopeExpander(query)
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}

	repos, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: principal, RequirementKind: contextfabric.FactMetrics, Origins: []contextfabric.SubjectRef{project},
		Policy: contextfabric.FactScopePolicyProjectWorkItemRepository, TargetKind: contextfabric.SubjectRepository, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope repository: %v", err)
	}
	if got := entityTreeSlugs(repos.Targets); len(got) != 1 || got[0] != entityTreeSlugB {
		t.Fatalf("project repositories = %v, want only the linked pull request's repository %q (the issue's own repository column names %q)", got, entityTreeSlugB, entityTreeSlugA)
	}

	prs, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: principal, RequirementKind: contextfabric.FactPullRequests, Origins: []contextfabric.SubjectRef{project},
		Policy: contextfabric.FactScopePolicyProjectWorkItemPullRequest, TargetKind: contextfabric.SubjectPullRequest, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope pull request: %v", err)
	}
	for _, target := range prs.Targets {
		if target.CanonicalID == "pull_request:"+entityTreeRepoA+":1" {
			t.Fatalf("project pull requests include repository A's pull request %q: reached through the issue's repository column, not a linked pull request row", target.CanonicalID)
		}
	}
}

func TestFactScopeEntityTree_TeamRepositoryFollowsOwnershipRows(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	seedEntityTreeFixture(t, ctx, direct, orgID, time.Now().UTC())
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + entityTreeTeam, Label: "Tree team"}
	expander := devhealthfacts.NewScopeExpander(query)

	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactMetrics,
		Origins: []contextfabric.SubjectRef{team}, Policy: contextfabric.FactScopePolicyTeamPrimaryAttributionRepository,
		TargetKind: contextfabric.SubjectRepository, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if got := entityTreeSlugs(result.Targets); len(got) != 1 || got[0] != entityTreeSlugB {
		t.Fatalf("team repositories = %v, want only the owned repository %q (a work_item_team_attributions row names %q, which the team does not own)", got, entityTreeSlugB, entityTreeSlugA)
	}
}
