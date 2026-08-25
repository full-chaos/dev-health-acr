package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4101: proves the ClickHouse-backed ScopeExpander's three team-origin
// policies against a REAL ClickHouse container, mirroring
// chaos4099_scope_expander_integration_test.go's own rationale for doing so
// (window functions, FINAL, argMin -- a fake client cannot execute this
// SQL). Reuses that file's org id and repository/orphan/zero-UUID fixture
// values where the shapes coincide, so a reader comparing the two files sees
// the SAME sentinel handling proven twice, not two different conventions.
const chaos4101TeamID = "PLATFORM"
const chaos4101RepoAID = "e1198fbc-1945-3717-05d8-eb78866b4e90"
const chaos4101RepoASlug = "acme/repo-a"
const chaos4101RepoBID = "f2298fbc-1945-3717-05d8-eb78866b4e91"
const chaos4101RepoBSlug = "acme/repo-b"

// seedChaos4101TeamFixture creates one team (chaos4101TeamID) with:
//   - repo A reached by TWO primary attributions on DIFFERENT work items,
//     one native_team-sourced and one project_ownership-sourced -- the
//     native_team row must win repo A's basis (ActivityProxy).
//   - repo B reached by ONE primary attribution, repo_ownership-sourced --
//     no native_team row exists for it, so its basis is
//     AttributedPrimaryTeam.
//   - a zero-UUID work item and an orphan-repo work item, both primary
//     attributions to the same team -- neither may ever admit a fake
//     repository target.
//   - two pull requests on repo A, one on repo B, and one review on repo A.
func seedChaos4101TeamFixture(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, at time.Time) {
	t.Helper()
	for _, statement := range devhealthschema.DDL("teams", "work_items", "work_item_team_attributions", "repos", "git_pull_requests", "git_pull_request_reviews") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}

	mustSeed("team", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4101TeamID, "Platform", "", at, chaos4099OrgID, "linear", "PLATFORM", uint8(1))

	mustSeed("repo A", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4101RepoAID, chaos4099OrgID, chaos4101RepoASlug, "github", at)
	mustSeed("repo B", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4101RepoBID, chaos4099OrgID, chaos4101RepoBSlug, "gitlab", at)

	workItem := func(id, repoID string) {
		mustSeed("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repoID, chaos4099OrgID, "issue "+id, "open", "", "", "", at)
	}
	// attribution's OWN repo_id column value is deliberately NOT trusted by
	// teamRepositories' SQL (it joins work_items for repo_id instead --
	// devhealthsource's queryWorkItemTeams gives the CHAOS-3785 reason: this
	// column is mostly the zero UUID and would be meaningless to scope on).
	// Passed here anyway, matching each row's real repo, purely so this
	// fixture reads honestly rather than relying on that column being inert.
	attribution := func(label, workItemID, repoID, source string) {
		mustSeed(label, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			chaos4099OrgID, repoID, workItemID, chaos4101TeamID, source, uint8(1), "high", at)
	}

	workItem("wi-a-native", chaos4101RepoAID)
	attribution("repo A native_team attribution", "wi-a-native", chaos4101RepoAID, "native_team")
	workItem("wi-a-heuristic", chaos4101RepoAID)
	attribution("repo A project_ownership attribution", "wi-a-heuristic", chaos4101RepoAID, "project_ownership")

	workItem("wi-b-heuristic", chaos4101RepoBID)
	attribution("repo B repo_ownership attribution", "wi-b-heuristic", chaos4101RepoBID, "repo_ownership")

	workItem("wi-zero-uuid", chaos4099ZeroRepositoryID)
	attribution("zero-uuid attribution", "wi-zero-uuid", chaos4099ZeroRepositoryID, "issue_project")
	workItem("wi-orphan", chaos4099OrphanRepoID)
	attribution("orphan attribution", "wi-orphan", chaos4099OrphanRepoID, "assignee_membership")

	mustSeed("repo A pull request 1", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4101RepoAID, chaos4099OrgID, uint32(1), "Add widget", "open", at, at, "feat/widget", "")
	mustSeed("repo A pull request 2", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4101RepoAID, chaos4099OrgID, uint32(2), "Fix widget", "merged", at, at, "fix/widget", "")
	mustSeed("repo A review", `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"review-a-1", chaos4101RepoAID, chaos4099OrgID, uint32(1), "approved", at)
	mustSeed("repo B pull request", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4101RepoBID, chaos4099OrgID, uint32(3), "Repo B PR", "open", at, at, "feat/b", "")
}

func chaos4101TeamSubject() contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + chaos4101TeamID, Label: "Platform"}
}

// TestScopeExpander_TeamToRepository_BasisVariesByWinningSourceAndSentinelsExcluded
// is CHAOS-4101's headline proof: ONE traversal admitting two repositories
// with DIFFERENT bases (repo A wins native_team over its own
// project_ownership row -- native_team wins REGARDLESS of which work item
// the resolver happens to read first; repo B has no native_team row and so
// stays attributed_primary_team), with the zero-UUID and orphan rows
// excluded exactly as the project chain excludes them.
func TestScopeExpander_TeamToRepository_BasisVariesByWinningSourceAndSentinelsExcluded(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Policy:          contextfabric.FactScopePolicyTeamPrimaryAttributionRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("targets = %+v, want repo A and repo B only", result.Targets)
	}
	repoATarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos4101RepoAID, Label: chaos4101RepoASlug}
	repoBTarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos4101RepoBID, Label: chaos4101RepoBSlug}
	seen := map[contextfabric.SubjectRef]bool{}
	for _, target := range result.Targets {
		seen[target] = true
	}
	if !seen[repoATarget] || !seen[repoBTarget] {
		t.Fatalf("targets = %+v, want both %+v and %+v", result.Targets, repoATarget, repoBTarget)
	}
	if got := result.TargetBasis[contextfabric.FactSubjectKey(repoATarget)]; got != contextfabric.FactScopeBasisActivityProxy {
		t.Fatalf("repo A basis = %q, want activity_proxy: its native_team row must win over its project_ownership row", got)
	}
	if got := result.TargetBasis[contextfabric.FactSubjectKey(repoBTarget)]; got != contextfabric.FactScopeBasisAttributedPrimaryTeam {
		t.Fatalf("repo B basis = %q, want attributed_primary_team: no native_team row reaches it", got)
	}
	if result.Counts.CandidateCount != 2 {
		t.Fatalf("CandidateCount = %d, want 2 (the zero-UUID and orphan rows are NOT candidates)", result.Counts.CandidateCount)
	}
	if result.Counts.MissingNextHopCount != 2 {
		t.Fatalf("MissingNextHopCount = %d, want 2 (zero-UUID + orphan)", result.Counts.MissingNextHopCount)
	}
	if got := result.Counts.AttributionSourceCounts["native_team"]; got != 1 {
		t.Fatalf("AttributionSourceCounts[native_team] = %d, want 1 (repo A)", got)
	}
	if got := result.Counts.AttributionSourceCounts["repo_ownership"]; got != 1 {
		t.Fatalf("AttributionSourceCounts[repo_ownership] = %d, want 1 (repo B)", got)
	}
	if _, present := result.Counts.AttributionSourceCounts["project_ownership"]; present {
		t.Fatalf("AttributionSourceCounts = %+v, project_ownership must not appear: it lost to native_team on repo A and was never the winner anywhere", result.Counts.AttributionSourceCounts)
	}
}

// TestScopeExpander_TeamToRepository_UnauthorizedPrincipalDropsTheCandidate
// is CHAOS-4101's per-hop authorization proof (team-lead ruling point 4):
// the team-attribution hop reaching a repository the principal is NOT
// authorized for must never admit it, mirroring
// TestScopeExpander_ProjectToRepository_UnauthorizedPrincipalDropsTheCandidate
// exactly for the team origin.
func TestScopeExpander_TeamToRepository_UnauthorizedPrincipalDropsTheCandidate(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		// Authorized for repo A only -- repo B must be dropped, not admitted.
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4101RepoASlug}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Policy:          contextfabric.FactScopePolicyTeamPrimaryAttributionRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly repo A", result.Targets)
	}
	if result.Targets[0].CanonicalID != "repository:"+chaos4101RepoAID {
		t.Fatalf("target = %+v, want repo A", result.Targets[0])
	}
	if result.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1: repo B exists but this principal cannot read it", result.Counts.AuthorizationDroppedCount)
	}
	if result.Counts.CandidateCount != 2 {
		t.Fatalf("CandidateCount = %d, want 2: both repositories were real candidates before authorization", result.Counts.CandidateCount)
	}
}

// TestScopeExpander_TeamToPullRequest_InheritsRepositoryBasisAndDropsUnauthorized
// proves the one-hop-further case: pull requests inherit the basis of the
// repository they belong to, and an unauthorized repository's pull requests
// are never queried, let alone returned -- content from a repository this
// caller may not read must never be read at all, the same invariant the
// project policies hold.
func TestScopeExpander_TeamToPullRequest_InheritsRepositoryBasisAndDropsUnauthorized(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		// Authorized for repo A only.
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4101RepoASlug}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Policy:          contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("targets = %+v, want the two repo A pull requests only -- repo B is unauthorized", result.Targets)
	}
	for _, target := range result.Targets {
		if target.CanonicalID == "pull_request:"+chaos4101RepoBID+":3" {
			t.Fatalf("targets = %+v, the unauthorized repository's pull request was admitted", result.Targets)
		}
		if got := result.TargetBasis[contextfabric.FactSubjectKey(target)]; got != contextfabric.FactScopeBasisActivityProxy {
			t.Fatalf("target %+v basis = %q, want activity_proxy (inherited from repo A, which has a native_team row)", target, got)
		}
	}
	if result.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1 (repo B, dropped before its pull requests were ever queried)", result.Counts.AuthorizationDroppedCount)
	}
}
