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
