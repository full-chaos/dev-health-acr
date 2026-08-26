package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	// attribution's OWN repo_id column IS part of teamRepositories' join
	// (codex xhigh review round 2, confirmed real, MEDIUM): work_items'
	// declared sort key is (org_id, repo_id, work_item_id), so a bare
	// work_item_id is cross-repo-collidable (two different repositories can
	// each have their own issue "42"), and joining on work_item_id alone
	// let an attribution for ONE repo's work item admit a DIFFERENT repo
	// that merely happens to reuse the same bare id -- see
	// TestScopeExpander_TeamToRepository_CrossRepoWorkItemIDCollisionExcludesTheWrongRepo
	// below for the direct reproduction. compute_work_item_team_attributions
	// (ops/metrics/compute_work_items.py) writes this column from the SAME
	// WorkItem.repo_id that lands in work_items.repo_id for that row -- zero
	// UUID for a Linear-sourced item on BOTH sides, a real id on BOTH sides
	// otherwise -- so joining on it is a correct identity match, not a
	// scoping filter on a mostly-empty column.
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
	// AttributionSourceCounts itself is a RESOLVER-derived field (CHAOS-4101
	// codex xhigh review round 1, confirmed real, MEDIUM x2): the resolver
	// builds it from `kept`, its own cap-and-dedup-applied admitted set, not
	// from every target ExpandFactScope returns. This expander-level test
	// asserts the per-target TargetAttributionSource map ExpandFactScope
	// actually owns instead; TestChaos4101_AttributionSourceCountsExcludeTheOverflowedTarget
	// in the contextfabric package (unit test, fake expander) proves the
	// resolver's own overflow-safe derivation from that map.
	if got := result.TargetAttributionSource[contextfabric.FactSubjectKey(repoATarget)]; got != "native_team" {
		t.Fatalf("TargetAttributionSource[repo A] = %q, want native_team", got)
	}
	if got := result.TargetAttributionSource[contextfabric.FactSubjectKey(repoBTarget)]; got != "repo_ownership" {
		t.Fatalf("TargetAttributionSource[repo B] = %q, want repo_ownership", got)
	}
	for key, source := range result.TargetAttributionSource {
		if source == "project_ownership" {
			t.Fatalf("TargetAttributionSource[%s] = project_ownership, must not appear: it lost to native_team on repo A and was never the winner anywhere", key)
		}
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

// TestScopeExpander_TeamToPullRequestReview_InheritsRepositoryBasisAndDropsUnauthorized
// is the pull_request test above's own twin for the third and last team
// policy, team_primary_attribution_pull_request_review_v1 (codex xhigh
// review round 1, LOW: this path had no real-ClickHouse coverage despite
// carrying separate production logic -- pullRequestReviewsForRepositories'
// own INNER JOIN and identity.Derive call, untested by either of the two
// tests above). Repo A's one review must be admitted and inherit repo A's
// activity_proxy basis; repo B carries no review fixture, so authorization
// dropping it is unobservable here beyond the count -- the pull_request
// test already proves content from an unauthorized repository is never
// queried at all.
func TestScopeExpander_TeamToPullRequestReview_InheritsRepositoryBasisAndDropsUnauthorized(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		// Authorized for repo A only.
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4101RepoASlug}},
		RequirementKind: contextfabric.FactReviews,
		Origins:         []contextfabric.SubjectRef{chaos4101TeamSubject()},
		Policy:          contextfabric.FactScopePolicyTeamPrimaryAttributionPullRequestReview,
		TargetKind:      contractsv1.ContextFabricSubjectPullRequestReview,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 1 {
		t.Fatalf("targets = %+v, want repo A's one review only", result.Targets)
	}
	target := result.Targets[0]
	if got := result.TargetBasis[contextfabric.FactSubjectKey(target)]; got != contextfabric.FactScopeBasisActivityProxy {
		t.Fatalf("target %+v basis = %q, want activity_proxy (inherited from repo A, which has a native_team row)", target, got)
	}
	if got := result.TargetAttributionSource[contextfabric.FactSubjectKey(target)]; got != "native_team" {
		t.Fatalf("TargetAttributionSource[review] = %q, want native_team (repo A's winning source)", got)
	}
	if result.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1 (repo B, dropped before its reviews were ever queried)", result.Counts.AuthorizationDroppedCount)
	}
}

// TestScopeExpander_TeamToRepository_CrossRepoWorkItemIDCollisionExcludesTheWrongRepo
// is the direct reproduction for codex xhigh review round 2's first
// confirmed MEDIUM: work_items' declared sort key is (org_id, repo_id,
// work_item_id) -- work_item_id alone is NOT the table's natural key, and a
// bare id is cross-repo-collidable (two different repositories, e.g. two
// different GitHub repos, can each have their own issue "42"). Before this
// fix, teamRepositories' join matched work_items to
// work_item_team_attributions on work_item_id alone, so an attribution
// naming repo A's issue could ALSO match an unrelated repo's own,
// differently-attributed (or entirely unattributed) issue sharing the same
// bare id -- admitting a repository into the team's scope that no
// attribution row ever actually named.
func TestScopeExpander_TeamToRepository_CrossRepoWorkItemIDCollisionExcludesTheWrongRepo(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4101TeamFixture(t, ctx, direct, at)

	const collidingRepoID = "a3398fbc-1945-3717-05d8-eb78866b4e92"
	const collidingRepoSlug = "acme/repo-c-unrelated"
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustSeed("repo C", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		collidingRepoID, chaos4099OrgID, collidingRepoSlug, "github", at)
	// SAME bare work_item_id ("collide-42") as repo A's native_team-attributed
	// work item below, but belonging to a DIFFERENT repository and carrying
	// NO team attribution row of its own.
	mustSeed("repo A colliding work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"collide-42", chaos4101RepoAID, chaos4099OrgID, "issue collide-42 in repo A", "open", "", "", "", at)
	mustSeed("repo C colliding work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"collide-42", collidingRepoID, chaos4099OrgID, "issue collide-42 in repo C", "open", "", "", "", at)
	mustSeed("repo A colliding attribution", `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4099OrgID, chaos4101RepoAID, "collide-42", chaos4101TeamID, "native_team", uint8(1), "high", at)

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
	for _, target := range result.Targets {
		if target.CanonicalID == "repository:"+collidingRepoID {
			t.Fatalf("targets = %+v, repo C was admitted despite carrying no team attribution of its own -- its ONLY connection to the team is sharing a bare work_item_id with repo A's real attribution", result.Targets)
		}
	}
}

// TestScopeExpander_TeamToRepository_RootIsPerTargetAcrossTwoTeamOrigins is
// CHAOS-4260's real-ClickHouse proof, the exact scenario the ticket names:
// "an investigation names multiple same-kind subjects as roots (e.g. two
// teams) and they are traversed together in one expand() call". Two teams,
// each with its own primary attribution reaching its OWN, otherwise
// unrelated repository, requested together as Origins in ONE
// ExpandFactScope call. Before the fix, teamRepositories never populated a
// per-target root at all (repositoryCandidate had no such field), so
// fact_scope.go's expand() attributed EVERY admitted repository to
// origins[0] regardless of which team's own edge reached it --
// TestChaos4260_RootIsPerTargetAcrossMultipleSameKindOrigins (fake-expander
// unit test, contextfabric package) proves the resolver-side half of that
// fix; this proves the ClickHouse-backed teamRepositories query itself now
// resolves the correct origin per repository, including through the
// `min(a.team_id)` aggregate's tiebreak when both teams reach the SAME
// repository.
func TestScopeExpander_TeamToRepository_RootIsPerTargetAcrossTwoTeamOrigins(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	for _, statement := range devhealthschema.DDL("teams", "work_items", "work_item_team_attributions", "repos") {
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

	const teamAID = "ROOT-TEAM-A"
	const teamBID = "ROOT-TEAM-B"
	const repoOnlyAID = "b1198fbc-1945-3717-05d8-eb78866b4ea0"
	const repoOnlyASlug = "acme/root-repo-a"
	const repoOnlyBID = "b2298fbc-1945-3717-05d8-eb78866b4ea1"
	const repoOnlyBSlug = "acme/root-repo-b"
	const repoSharedID = "b3398fbc-1945-3717-05d8-eb78866b4ea2"
	const repoSharedSlug = "acme/root-repo-shared"

	mustSeed("team A", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		teamAID, "Root Team A", "", at, chaos4099OrgID, "linear", teamAID, uint8(1))
	mustSeed("team B", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		teamBID, "Root Team B", "", at, chaos4099OrgID, "linear", teamBID, uint8(1))
	mustSeed("repo only-A", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoOnlyAID, chaos4099OrgID, repoOnlyASlug, "github", at)
	mustSeed("repo only-B", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoOnlyBID, chaos4099OrgID, repoOnlyBSlug, "github", at)
	mustSeed("repo shared", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoSharedID, chaos4099OrgID, repoSharedSlug, "github", at)

	workItem := func(id, repoID string) {
		mustSeed("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, repoID, chaos4099OrgID, "issue "+id, "open", "", "", "", at)
	}
	attribution := func(label, workItemID, repoID, teamID string) {
		mustSeed(label, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			chaos4099OrgID, repoID, workItemID, teamID, "native_team", uint8(1), "high", at)
	}

	workItem("wi-only-a", repoOnlyAID)
	attribution("team A -> repo only-A", "wi-only-a", repoOnlyAID, teamAID)
	workItem("wi-only-b", repoOnlyBID)
	attribution("team B -> repo only-B", "wi-only-b", repoOnlyBID, teamBID)
	// The shared repo is reached by BOTH teams, on two DIFFERENT work items
	// (a repo/work_item pair is the join's natural key, so this is two
	// distinct, independently real attributions, not a duplicate row).
	// teamAID < teamBID lexicographically, so min(a.team_id) -- and
	// therefore this repo's root -- must resolve to team A.
	workItem("wi-shared-a", repoSharedID)
	attribution("team A -> repo shared", "wi-shared-a", repoSharedID, teamAID)
	workItem("wi-shared-b", repoSharedID)
	attribution("team B -> repo shared", "wi-shared-b", repoSharedID, teamBID)

	teamASubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + teamAID, Label: "Root Team A"}
	teamBSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + teamBID, Label: "Root Team B"}

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		// BOTH teams in ONE call -- this is the exact shape resolveRequirement
		// produces when an investigation names two same-kind roots.
		Origins:    []contextfabric.SubjectRef{teamASubject, teamBSubject},
		Policy:     contextfabric.FactScopePolicyTeamPrimaryAttributionRepository,
		TargetKind: contextfabric.SubjectRepository,
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 3 {
		t.Fatalf("targets = %+v, want all 3 repositories", result.Targets)
	}
	repoOnlyATarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoOnlyAID, Label: repoOnlyASlug}
	repoOnlyBTarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoOnlyBID, Label: repoOnlyBSlug}
	repoSharedTarget := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoSharedID, Label: repoSharedSlug}

	if got := result.TargetRoot[contextfabric.FactSubjectKey(repoOnlyATarget)]; got != teamASubject {
		t.Fatalf("TargetRoot[repo only-A] = %+v, want team A (%+v): its ONLY attribution is from team A", got, teamASubject)
	}
	if got := result.TargetRoot[contextfabric.FactSubjectKey(repoOnlyBTarget)]; got != teamBSubject {
		t.Fatalf("TargetRoot[repo only-B] = %+v, want team B (%+v): its ONLY attribution is from team B -- BEFORE the CHAOS-4260 fix this collapsed to origins[0] (team A) regardless", got, teamBSubject)
	}
	if got := result.TargetRoot[contextfabric.FactSubjectKey(repoSharedTarget)]; got != teamASubject {
		t.Fatalf("TargetRoot[repo shared] = %+v, want team A (%+v): min(a.team_id) tiebreaks to the lexicographically smaller of the two teams reaching it", got, teamASubject)
	}
}
