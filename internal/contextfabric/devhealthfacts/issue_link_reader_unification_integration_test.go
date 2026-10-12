package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Every reader of the issue <> pull request link of record admits a link row
// the same way: the pull request it names exists, and the work item is an
// issue (not typed pr or merge_request). The fixture is the entity-tree chain
// fixture: one issue, natively linked to repository B's pull request 7, and an
// own-repository column that names repository A.

const issueLinkReaderStalePullRequest = uint32(99)

func seedStaleIssueLink(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, orgID string, at time.Time) {
	t.Helper()
	seedEntityTreeIssue(t, ctx, direct, orgID, "tree#stale", entityTreeRepoB, at)
	// The link names repository A's pull request 99; A has no such pull request.
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entityTreeRepoA, "tree#stale", issueLinkReaderStalePullRequest, float32(1), "native", "", at, orgID); err != nil {
		t.Fatalf("seed stale link: %v", err)
	}
}

func seedPullRequestTypedItems(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, orgID string, at time.Time) {
	t.Helper()
	for _, id := range []string{"tree#prlinked", "tree#prbare"} {
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at, provider, type) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, entityTreeRepoB, orgID, id, "open", "", "", entityTreeProject, at, "jira", "pr"); err != nil {
			t.Fatalf("seed pull-request-typed item %s: %v", id, err)
		}
	}
	// A link to repository A's pull request 1, which exists.
	seedIssueLink(t, ctx, direct, orgID, "tree#prlinked", entityTreeRepoA, 1, "native", at)
}

func expandIssueLinkRepositories(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, origin contextfabric.SubjectRef, policy contextfabric.FactScopePolicy, timeContext contextfabric.TimeContext) contextfabric.FactScopeExpansionResult {
	t.Helper()
	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactMetrics,
		Origins: []contextfabric.SubjectRef{origin}, Policy: policy, TargetKind: contextfabric.SubjectRepository,
		TimeContext: timeContext, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	return result
}

func assertOnlyLinkedRepository(t *testing.T, result contextfabric.FactScopeExpansionResult, wantMissing int) {
	t.Helper()
	if got := entityTreeSlugs(result.Targets); len(got) != 1 || got[0] != entityTreeSlugB {
		t.Fatalf("repositories = %v, want only %q: a link naming a missing pull request, or a pull-request-typed work item, reaches no repository", got, entityTreeSlugB)
	}
	if result.Counts.MissingNextHopCount != wantMissing {
		t.Fatalf("MissingNextHopCount = %d, want %d", result.Counts.MissingNextHopCount, wantMissing)
	}
}

func TestIssueLinkReader_ProjectCurrentAxisSkipsStaleLinkAndCountsTheIssue(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedEntityTreeFixture(t, ctx, direct, orgID, at)
	seedStaleIssueLink(t, ctx, direct, orgID, at)
	result := expandIssueLinkRepositories(t, ctx, query, orgID, entityTreeProjectSubject(t),
		contextfabric.FactScopePolicyProjectWorkItemRepository, contextfabric.TimeContext{})
	assertOnlyLinkedRepository(t, result, 1)
}

func TestIssueLinkReader_TeamRepositoriesSkipStaleLinkAndCountTheIssue(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedEntityTreeFixture(t, ctx, direct, orgID, at)
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "jira", entityTreeTeam, entityTreeProject, nil, "native", at.Add(-time.Hour), nil, at); err != nil {
		t.Fatalf("seed project ownership: %v", err)
	}
	seedStaleIssueLink(t, ctx, direct, orgID, at)
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + entityTreeTeam, Label: "Tree team"}
	result := expandIssueLinkRepositories(t, ctx, query, orgID, team,
		contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, contextfabric.TimeContext{})
	assertOnlyLinkedRepository(t, result, 1)
}

func TestIssueLinkReader_PullRequestTypedItemsAreNotIssuesOnAnyProjectReader(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedEntityTreeFixture(t, ctx, direct, orgID, at)
	seedPullRequestTypedItems(t, ctx, direct, orgID, at)
	result := expandIssueLinkRepositories(t, ctx, query, orgID, entityTreeProjectSubject(t),
		contextfabric.FactScopePolicyProjectWorkItemRepository, contextfabric.TimeContext{})
	// Neither the linked nor the unlinked pull-request-typed item is an issue:
	// the first reaches no repository, the second is not a missing next hop.
	assertOnlyLinkedRepository(t, result, 0)
}

func TestIssueLinkReader_ProjectAsOfSkipsStaleLink(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	chaos4109ScopeSeed(t, ctx, direct, t1, t2, t3)
	for _, statement := range []string{`TRUNCATE TABLE work_graph_issue_pr`, `TRUNCATE TABLE git_pull_requests`} {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	// The only link names pull request 1, which has no pull request row.
	if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4109ScopeRepoID, chaos4109ScopeWorkItemID, uint32(1), float32(1), "native", "", t1, chaos4109ScopeOrgID); err != nil {
		t.Fatalf("seed stale link: %v", err)
	}
	asOf := t1.Add(15 * 24 * time.Hour)
	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal: storage.Principal{OrgID: chaos4109ScopeOrgID, RepositoryScopes: []string{"*"}}, RequirementKind: contextfabric.FactMetrics,
		Origins: []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, chaos4109ScopeProjectAID)},
		Policy:  contextfabric.FactScopePolicyProjectWorkItemRepository, TargetKind: contextfabric.SubjectRepository,
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}, Limit: 20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none: the only link names a pull request that does not exist", result.Targets)
	}
	if result.Counts.MissingNextHopCount != 1 {
		t.Fatalf("MissingNextHopCount = %d, want 1", result.Counts.MissingNextHopCount)
	}
}
