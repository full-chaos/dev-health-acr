package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type linkTierSeeder struct {
	t      *testing.T
	ctx    context.Context
	direct interface {
		Exec(context.Context, string, ...any) error
	}
	orgID   string
	at      time.Time
	repoID  string
	nextNum uint32
}

func (s *linkTierSeeder) exec(label, statement string, args ...any) {
	s.t.Helper()
	if err := s.direct.Exec(s.ctx, statement, args...); err != nil {
		s.t.Fatalf("seed %s: %v", label, err)
	}
}

func (s *linkTierSeeder) project(provider, id, key, name string) contextfabric.SubjectRef {
	s.t.Helper()
	s.exec("project "+provider+":"+id, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, s.orgID, name, key, provider, "", "https://example.invalid/"+key, uint8(1), s.at)
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, id}, nil)
	if err != nil || omitted {
		s.t.Fatalf("derive project canonical id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID, Label: name}
}

func (s *linkTierSeeder) pullRequest(number uint32) {
	s.t.Helper()
	s.exec("pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		s.repoID, s.orgID, number, "linked", "open", s.at, s.at, "feat/linked", "")
}

func (s *linkTierSeeder) issue(workItemID, projectKey string) {
	s.t.Helper()
	provider, _, _ := strings.Cut(workItemID, ":")
	s.exec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, s.repoID, s.orgID, "issue", "open", "", "", provider, projectKey, s.at)
}

func (s *linkTierSeeder) link(workItemID string, number uint32, tier string) {
	s.t.Helper()
	seedIssueLink(s.t, s.ctx, s.direct, s.orgID, workItemID, s.repoID, number, tier, s.at)
}

func expandPullRequests(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, limit int, origins ...contextfabric.SubjectRef) contextfabric.FactScopeExpansionResult {
	t.Helper()
	return expandPullRequestsAt(t, ctx, query, orgID, limit, contextfabric.TimeContext{}, origins...)
}

func expandPullRequestsAt(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID string, limit int, timeContext contextfabric.TimeContext, origins ...contextfabric.SubjectRef) contextfabric.FactScopeExpansionResult {
	t.Helper()
	result, err := devhealthfacts.NewScopeExpander(query).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		TimeContext:     timeContext,
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         origins,
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           limit,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	return result
}

func tierOf(result contextfabric.FactScopeExpansionResult, repoID string, number uint32, root contextfabric.SubjectRef) (string, int) {
	for _, target := range result.Targets {
		if target.CanonicalID == "pull_request:"+repoID+":"+itoa(number) {
			tiers := result.TargetLinkTier[contextfabric.FactSubjectKey(target)]
			return tiers[contextfabric.FactSubjectKey(root)], len(tiers)
		}
	}
	return "<no target>", -1
}

func itoa(n uint32) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{digits[n%10]}, b...)
	}
	return string(b)
}

// A pull request with a native link gets no lower-tier link, whichever
// project holds either link and whether or not that project was requested.
func TestScopeExpander_ProjectToPullRequest_NativeLinkCoversLowerTiersAcrossProjects(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at, orgID)
	s := &linkTierSeeder{t: t, ctx: ctx, direct: direct, orgID: orgID, at: at, repoID: chaos4099GitLabRepoID}
	first := chaos4099ProjectSubject(t)
	second := s.project("gitlab", "org-4099-scope-expander:gitlab:71133892", "acme/web", "acme web")
	s.issue("gitlab:acme/web#7", "acme/web")
	s.pullRequest(3)
	s.pullRequest(4)
	s.pullRequest(5)
	// PR 1: first holds native (fixture), second holds explicit_text.
	s.link("gitlab:acme/web#7", 1, "explicit_text")
	// PR 3: first holds explicit_text, second native.
	s.issue("gitlab:acme/api#31", "acme/api")
	s.link("gitlab:acme/api#31", 3, "explicit_text")
	s.link("gitlab:acme/web#7", 3, "native")
	// PR 4: both requested projects hold the same tier, so both keep it.
	s.issue("gitlab:acme/api#41", "acme/api")
	s.link("gitlab:acme/api#41", 4, "heuristic")
	s.link("gitlab:acme/web#7", 4, "heuristic")
	// PR 5: requested project holds explicit_text; an unrequested project holds native.
	s.project("gitlab", "org-4099-scope-expander:gitlab:99999", "acme/other", "acme other")
	s.issue("gitlab:acme/other#9", "acme/other")
	s.link("gitlab:acme/other#9", 5, "native")
	s.link("gitlab:acme/api#31", 5, "explicit_text")

	result := expandPullRequests(t, ctx, query, orgID, 20, first, second)
	for _, row := range []struct {
		number uint32
		root   contextfabric.SubjectRef
		tier   string
		count  int
	}{
		{1, first, "native", 1},
		{3, second, "native", 1},
		{4, first, "heuristic", 2},
		{4, second, "heuristic", 2},
		{5, first, "", 0},
	} {
		tier, count := tierOf(result, chaos4099GitLabRepoID, row.number, row.root)
		if tier != row.tier || count != row.count {
			t.Fatalf("pull request %d root %s: tier=%q roots=%d, want tier=%q roots=%d (all %v)", row.number, row.root.CanonicalID, tier, count, row.tier, row.count, result.TargetLinkTier)
		}
	}
}

// Two providers can mint one raw project id; each root keeps its own tier and
// the repository hop names both roots.
func TestScopeExpander_ProjectToPullRequest_ProviderQualifiedRootsShareARawID(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at, orgID)
	s := &linkTierSeeder{t: t, ctx: ctx, direct: direct, orgID: orgID, at: at, repoID: chaos4099GitLabRepoID}
	const rawID = "shared-raw-project-id"
	gitlab := s.project("gitlab", rawID, "gl-shared-key", "gitlab shared")
	github := s.project("github", rawID, "gh-shared-key", "github shared")
	s.issue("gitlab:shared#1", "gl-shared-key")
	s.issue("github:shared#1", "gh-shared-key")
	s.pullRequest(6)
	s.pullRequest(7)
	s.link("gitlab:shared#1", 6, "native")
	s.link("github:shared#1", 7, "explicit_text")

	result := expandPullRequests(t, ctx, query, orgID, 20, github, gitlab)
	for _, row := range []struct {
		number uint32
		root   contextfabric.SubjectRef
		other  contextfabric.SubjectRef
		tier   string
	}{
		{6, gitlab, github, "native"},
		{7, github, gitlab, "explicit_text"},
	} {
		tier, count := tierOf(result, chaos4099GitLabRepoID, row.number, row.root)
		if tier != row.tier || count != 1 {
			t.Fatalf("pull request %d root %s: tier=%q roots=%d, want tier=%q roots=1 (all %v)", row.number, row.root.CanonicalID, tier, count, row.tier, result.TargetLinkTier)
		}
		if other, _ := tierOf(result, chaos4099GitLabRepoID, row.number, row.other); other != "" {
			t.Fatalf("pull request %d carries a tier %q for the other provider's root", row.number, other)
		}
	}
	roots := map[string]bool{}
	for _, target := range result.Targets {
		if target.CanonicalID != "pull_request:"+chaos4099GitLabRepoID+":6" {
			continue
		}
		for _, root := range result.TargetRoots[contextfabric.FactSubjectKey(target)] {
			roots[root.CanonicalID] = true
		}
	}
	if !roots[gitlab.CanonicalID] || !roots[github.CanonicalID] {
		t.Fatalf("repository roots of pull request 6 = %v, want both providers' roots", roots)
	}
}

// The historical (valid_time) repository hop names each provider's root when
// two providers share one raw project id.
func TestScopeExpander_ProjectToPullRequest_AsOfProviderQualifiedRootsShareARawID(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC()
	for _, statement := range devhealthschema.DDL("project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create project_membership_transitions: %v", err)
		}
	}
	seedChaos4099Fixture(t, ctx, direct, at.Add(-48*time.Hour), orgID)
	s := &linkTierSeeder{t: t, ctx: ctx, direct: direct, orgID: orgID, at: at.Add(-48 * time.Hour), repoID: chaos4099GitLabRepoID}
	const rawID = "shared-raw-project-id"
	gitlab := s.project("gitlab", rawID, "gl-shared-key", "gitlab shared")
	github := s.project("github", rawID, "gh-shared-key", "github shared")
	s.issue("gitlab:shared#1", "gl-shared-key")
	s.issue("github:shared#1", "gh-shared-key")
	s.pullRequest(6)
	s.link("gitlab:shared#1", 6, "native")
	s.link("github:shared#1", 6, "native")

	asOf := at
	result := expandPullRequestsAt(t, ctx, query, orgID, 20, contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf}, github, gitlab)
	roots := map[string]bool{}
	for _, target := range result.Targets {
		if target.CanonicalID != "pull_request:"+chaos4099GitLabRepoID+":6" {
			continue
		}
		for _, root := range result.TargetRoots[contextfabric.FactSubjectKey(target)] {
			roots[root.CanonicalID] = true
		}
	}
	if !roots[gitlab.CanonicalID] || !roots[github.CanonicalID] {
		t.Fatalf("as-of repository roots of pull request 6 = %v, want both providers' roots", roots)
	}
}
