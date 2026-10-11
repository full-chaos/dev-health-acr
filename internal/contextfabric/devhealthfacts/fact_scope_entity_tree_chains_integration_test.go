package devhealthfacts_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
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

// seedIssueLink writes one link of record row: the issue's linked pull request
// number in the pull request's repository, at the given provenance tier.
func seedIssueLink(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, orgID, workItemID, pullRequestRepoID string, number uint32, tier string, at time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pullRequestRepoID, workItemID, number, float32(1), tier, "", at, orgID); err != nil {
		t.Fatalf("seed link %s -> %s#%d (%s): %v", workItemID, pullRequestRepoID, number, tier, err)
	}
}

func entityTreeProjectSubject(t *testing.T) contextfabric.SubjectRef {
	t.Helper()
	projectID, omitted, err := identity.Derive(identity.KindProject, []string{"jira", entityTreeProject}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: projectID, Label: "Tree"}
}

func seedEntityTreeIssue(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, orgID, id, columnRepoID string, at time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, provider) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, columnRepoID, orgID, id, "open", "", "", entityTreeProject, at, "jira"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}

// An issue with no link row reaches no repository and is disclosed; a repo-less
// issue (zero UUID column) with a link row reaches the pull request's
// repository; the strongest tier per repository is the one carried.
func TestFactScopeEntityTree_ProjectDisclosesUnlinkedIssuesAndCarriesTheStrongestTier(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedEntityTreeFixture(t, ctx, direct, orgID, at)
	const zeroRepo = "00000000-0000-0000-0000-000000000000"
	seedEntityTreeIssue(t, ctx, direct, orgID, "tree#2", entityTreeRepoA, at) // no link row
	seedEntityTreeIssue(t, ctx, direct, orgID, "tree#3", zeroRepo, at)        // repo-less, linked below
	seedIssueLink(t, ctx, direct, orgID, "tree#3", entityTreeRepoB, 7, "heuristic", at)
	seedIssueLink(t, ctx, direct, orgID, "tree#3", entityTreeRepoA, 1, "explicit_text", at)
	expander := devhealthfacts.NewScopeExpander(query)

	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactMetrics,
		Origins: []contextfabric.SubjectRef{entityTreeProjectSubject(t)}, Policy: contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind: contextfabric.SubjectRepository, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if got := entityTreeSlugs(result.Targets); len(got) != 2 || got[0] != entityTreeSlugA || got[1] != entityTreeSlugB {
		t.Fatalf("targets = %v, want [%s %s]: repo A through the repo-less issue's explicit_text link, repo B through tree#1's native link", got, entityTreeSlugA, entityTreeSlugB)
	}
	tiers := map[string]string{}
	for _, target := range result.Targets {
		tiers[target.Label] = result.TargetAttributionSource[contextfabric.FactSubjectKey(target)]
	}
	if tiers[entityTreeSlugB] != "native" {
		t.Fatalf("repo B tier = %q, want native: the strongest of its native and heuristic links", tiers[entityTreeSlugB])
	}
	if tiers[entityTreeSlugA] != "explicit_text" {
		t.Fatalf("repo A tier = %q, want explicit_text", tiers[entityTreeSlugA])
	}
	if result.Counts.MissingNextHopCount != 1 {
		t.Fatalf("MissingNextHopCount = %d, want 1: tree#2 has no link row and reaches no repository", result.Counts.MissingNextHopCount)
	}
}

// The pull request hop starts from the linked repository only.
func TestFactScopeEntityTree_ProjectPullRequestsComeFromTheLinkedRepositoryAndAuthorizeIt(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	seedEntityTreeFixture(t, ctx, direct, orgID, time.Now().UTC())
	expander := devhealthfacts.NewScopeExpander(query)
	request := func(scopes []string) contextfabric.FactScopeExpansionResult {
		t.Helper()
		result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
			Principal: storage.Principal{OrgID: orgID, RepositoryScopes: scopes}, RequirementKind: contextfabric.FactPullRequests,
			Origins: []contextfabric.SubjectRef{entityTreeProjectSubject(t)}, Policy: contextfabric.FactScopePolicyProjectWorkItemPullRequest,
			TargetKind: contextfabric.SubjectPullRequest, Limit: 20,
		})
		if err != nil {
			t.Fatalf("ExpandFactScope: %v", err)
		}
		return result
	}
	all := request([]string{"*"})
	if len(all.Targets) != 1 || all.Targets[0].CanonicalID != "pull_request:"+entityTreeRepoB+":7" {
		t.Fatalf("targets = %+v, want only repository B's pull request 7", all.Targets)
	}
	onlyColumnRepo := request([]string{entityTreeSlugA})
	if len(onlyColumnRepo.Targets) != 0 || onlyColumnRepo.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("a principal granted only the issue's column repository: targets=%+v dropped=%d, want none and 1 dropped (the linked repository)", onlyColumnRepo.Targets, onlyColumnRepo.Counts.AuthorizationDroppedCount)
	}
}

// A team reaches the repositories its owned projects' issues link to; the
// owned repository is Direct while the linked-only one is an activity proxy.
func TestFactScopeEntityTree_TeamReachesOwnedProjectsLinkedRepositories(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedEntityTreeFixture(t, ctx, direct, orgID, at)
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "jira", entityTreeTeam, entityTreeProject, nil, "native", at.Add(-time.Hour), nil, at); err != nil {
		t.Fatalf("seed project ownership: %v", err)
	}
	// An issue of the owned project, linked into the repository the team does not own.
	seedEntityTreeIssue(t, ctx, direct, orgID, "tree#4", entityTreeRepoB, at)
	seedIssueLink(t, ctx, direct, orgID, "tree#4", entityTreeRepoA, 1, "heuristic", at)
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + entityTreeTeam, Label: "Tree team"}
	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactMetrics,
		Origins: []contextfabric.SubjectRef{team}, Policy: contextfabric.FactScopePolicyTeamPrimaryAttributionRepository,
		TargetKind: contextfabric.SubjectRepository, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if got := entityTreeSlugs(result.Targets); len(got) != 2 || got[0] != entityTreeSlugA || got[1] != entityTreeSlugB {
		t.Fatalf("targets = %v, want [%s %s]", got, entityTreeSlugA, entityTreeSlugB)
	}
	for _, target := range result.Targets {
		key := contextfabric.FactSubjectKey(target)
		wantSource, wantBasis := "heuristic", contextfabric.FactScopeBasisActivityProxy
		if target.Label == entityTreeSlugB {
			wantSource, wantBasis = "team_repo_ownership", contextfabric.FactScopeBasisDirect
		}
		if got := result.TargetAttributionSource[key]; got != wantSource {
			t.Fatalf("%s source = %q, want %q", target.Label, got, wantSource)
		}
		if got := result.TargetBasis[key]; got != wantBasis {
			t.Fatalf("%s basis = %q, want %q", target.Label, got, wantBasis)
		}
	}
}

// The root names the team that reaches a repository by the strongest source: a
// team that owns the repository directly wins over a lexicographically smaller
// team that reaches it only through an owned project's link.
func TestFactScopeEntityTree_TeamRootFollowsTheStrongestSource(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedEntityTreeFixture(t, ctx, direct, orgID, at)
	const linkingTeam = "AAA-LINKING-TEAM"
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "jira", linkingTeam, entityTreeProject, nil, "native", at.Add(-time.Hour), nil, at); err != nil {
		t.Fatalf("seed project ownership: %v", err)
	}
	owner := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + entityTreeTeam, Label: "Tree team"}
	linker := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + linkingTeam, Label: "Linking team"}
	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactMetrics,
		Origins: []contextfabric.SubjectRef{linker, owner}, Policy: contextfabric.FactScopePolicyTeamPrimaryAttributionRepository,
		TargetKind: contextfabric.SubjectRepository, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 1 || result.Targets[0].Label != entityTreeSlugB {
		t.Fatalf("targets = %+v, want only %s", result.Targets, entityTreeSlugB)
	}
	key := contextfabric.FactSubjectKey(result.Targets[0])
	if got := result.TargetBasis[key]; got != contextfabric.FactScopeBasisDirect {
		t.Fatalf("basis = %q, want direct", got)
	}
	if got := result.TargetRoot[key]; got != owner {
		t.Fatalf("root = %+v, want the owning team %+v: the root must be the origin that produced the strongest source", got, owner)
	}
}
