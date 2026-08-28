package devhealthfacts_test

import (
	"context"
	"net"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// CHAOS-4099 stage 2: proves the ClickHouse-backed ScopeExpander's real SQL
// against a REAL ClickHouse container -- a fake client cannot execute the
// widened project-id/project-key JOIN (window functions, FINAL, DISTINCT
// over a UNION ALL), exactly the reason CHAOS-4108's own regression fixture
// (devhealthsource/chaos4108_join_arm_integration_test.go) gives for doing
// the same.
//
// FIXTURE PROVENANCE. The project/repo/work-item identity triple below --
// chaos4099GitLabProjectID (projects.id, an org:provider:id composite),
// chaos4099GitLabProjectKey (projects.project_key, a rename-safe
// project_full_path), chaos4099GitLabRepoID/RepoSlug -- reuses the EXACT
// same real, producer-verified shape CHAOS-4108's own fixture captured
// (dev-health-ops's gitlab_work_items_rows.go normalizer, via its own
// gitlab_work_items_oracle_test.go "open_labeled_issue" case): a gitlab
// project's own identity genuinely lives in TWO separate columns this way,
// and work_items.project_id genuinely carries the project_key value, never
// projects.id, for a gitlab-sourced row. Reusing the same verified values
// (rather than inventing a plausible-looking pair) is what makes this
// fixture prove the widened join arm actually fires, not merely that SOME
// join succeeds.
//
// git_pull_requests/git_pull_request_reviews rows below are NOT a second
// identity-linkage claim -- they carry no cross-provider project/work-item
// join at all, just repo_id/number/review_id columns queried directly
// against an ALREADY producer-verified repository. Ordinary seeded values
// are sufficient there; the standing "producer-generated, never
// hand-authored" rule exists to catch exactly the identity-linkage mistake
// CHAOS-4108 found, not to forbid straightforward fact-column fixtures.
const chaos4099OrgID = "org-4099-scope-expander"
const chaos4099GitLabProjectID = "org-4099-scope-expander:gitlab:71133891"
const chaos4099GitLabProjectKey = "acme/api"
const chaos4099GitLabRepoID = "c7198fbc-1945-3717-05d8-eb78866b4e79"
const chaos4099GitLabRepoSlug = "acme/api"
const chaos4099OrphanRepoID = "d8298fbc-1945-3717-05d8-eb78866b4e80"
const chaos4099ZeroRepositoryID = "00000000-0000-0000-0000-000000000000"

func newChaos4099ScopeExpanderClient(t *testing.T, ctx context.Context) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "clickhouse/clickhouse-server:24.8", ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate ClickHouse container: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort(host, port.Port())

	direct, err = clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Errorf("close native ClickHouse connection: %v", err)
		}
	})
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("clickhouse not ready for connections: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	query, err = runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open production ClickHouse query client: %v", err)
	}
	t.Cleanup(func() {
		if err := query.Close(); err != nil {
			t.Errorf("close production ClickHouse query client: %v", err)
		}
	})
	return query, direct
}

// seedChaos4099Fixture creates the shared schema and one gitlab project
// reachable ONLY via the project_key arm (chaos4099GitLabProjectKey), with:
//   - one real work item pointing to chaos4099GitLabRepoID (a real repos row)
//   - one work item whose repo_id is the zero-UUID sentinel (repo-less by
//     design -- must never expand to a fake repository)
//   - one work item whose repo_id names chaos4099OrphanRepoID, which has NO
//     matching repos row (an orphan -- must never expand to a fake
//     repository either)
//   - two pull requests and one review on the real repository
func seedChaos4099Fixture(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, at time.Time) {
	t.Helper()
	// devhealthschema:not-a-production-replica the table names passed to
	// devhealthschema.DDL below select what to render; the schema itself
	// is the declaration's, not this file's.
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "git_pull_requests", "git_pull_request_reviews") {
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

	mustSeed("gitlab project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4099GitLabProjectID, chaos4099OrgID, "acme api", chaos4099GitLabProjectKey, "gitlab", "", "https://gitlab.com/"+chaos4099GitLabProjectKey, uint8(1), at)
	mustSeed("real repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4099GitLabRepoID, chaos4099OrgID, chaos4099GitLabRepoSlug, "gitlab", at)

	// project_id carries the PROJECT_KEY value, never projects.id -- the
	// exact shape a real gitlab work item carries (CHAOS-4108's own
	// verified finding).
	mustSeed("real work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#42", chaos4099GitLabRepoID, chaos4099OrgID, "GitLab issue", "open", "", "", chaos4099GitLabProjectKey, at)
	mustSeed("zero-uuid work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#43", chaos4099ZeroRepositoryID, chaos4099OrgID, "GitLab issue (repo-less)", "open", "", "", chaos4099GitLabProjectKey, at)
	mustSeed("orphan work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#44", chaos4099OrphanRepoID, chaos4099OrgID, "GitLab issue (orphan repo)", "open", "", "", chaos4099GitLabProjectKey, at)

	mustSeed("pull request 1", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4099GitLabRepoID, chaos4099OrgID, uint32(1), "Add widget", "open", at, at, "feat/widget", "")
	mustSeed("pull request 2", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		chaos4099GitLabRepoID, chaos4099OrgID, uint32(2), "Fix widget", "merged", at, at, "fix/widget", "")
	mustSeed("pull request review", `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"review-1", chaos4099GitLabRepoID, chaos4099OrgID, uint32(1), "approved", at)
}

func chaos4099ProjectSubject(t *testing.T) contextfabric.SubjectRef {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"gitlab", chaos4099GitLabProjectID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project canonical id: err=%v omitted=%v", err, omitted)
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID, Label: "acme api"}
}

// TestScopeExpander_ProjectToRepository_WidenedJoinArmAndSentinelExclusion
// proves the repository hop: reaches the real repository through the
// project_key arm, and excludes BOTH the zero-UUID sentinel and the orphan
// repo_id -- neither may ever become a fake repository target.
func TestScopeExpander_ProjectToRepository_WidenedJoinArmAndSentinelExclusion(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 1 {
		t.Fatalf("targets = %+v, want exactly the one real repository", result.Targets)
	}
	want := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + chaos4099GitLabRepoID, Label: chaos4099GitLabRepoSlug}
	if result.Targets[0] != want {
		t.Fatalf("target = %+v, want %+v", result.Targets[0], want)
	}
	if result.Counts.CandidateCount != 1 {
		t.Fatalf("CandidateCount = %d, want 1 (the zero-UUID and orphan rows are NOT candidates)", result.Counts.CandidateCount)
	}
	if result.Counts.MissingNextHopCount != 2 {
		t.Fatalf("MissingNextHopCount = %d, want 2 (zero-UUID + orphan)", result.Counts.MissingNextHopCount)
	}
	if result.Counts.AuthorizationDroppedCount != 0 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 0: an unrestricted principal", result.Counts.AuthorizationDroppedCount)
	}
	if result.Counts.Truncated {
		t.Fatal("Truncated = true, want false: well under the cap")
	}
}

// TestScopeExpander_ProjectToRepository_ZeroUUIDNeverAdmitsEvenIfARepoRowClaimsIt
// is the zero-UUID sentinel's own regression case, isolated from the orphan
// case above (which happens to reach the same MissingNextHopCount outcome
// through repoSlug=="" alone, and so cannot by itself prove the explicit
// zeroRepositoryID check is load-bearing rather than redundant -- confirmed
// by hand: neutralizing that check while leaving the orphan fallback in
// place left this file's OTHER exclusion test passing, because an orphan
// repo_id with no matching repos row still falls through to repoSlug=="").
//
// This fixture seeds a PATHOLOGICAL repos row whose id IS the zero-UUID
// sentinel, carrying a real, non-empty slug -- schema-legal, and the one
// shape that actually discriminates the two checks: with it, repoSlug is
// NON-empty for that row, so only the explicit `repoID == zeroRepositoryID`
// case (checked BEFORE the empty-slug fallback) keeps it from being
// admitted as a fake repository target. Verified by hand: reverting the
// explicit case (leaving only the empty-slug fallback) makes this test
// fail -- the phantom repo is wrongly admitted -- confirming the guard's
// removal flips the outcome, restored via content diff against the
// pre-mutation file, never via git checkout or a build/vet pass alone.
func TestScopeExpander_ProjectToRepository_ZeroUUIDNeverAdmitsEvenIfARepoRowClaimsIt(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)
	// The pathological row: a repos entry whose id happens to be the
	// sentinel value, with a real-looking slug. Nothing in the repos
	// table schema forbids this; it is exactly the shape that would let a
	// buggy re-derivation of this guard silently admit a fake target.
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4099ZeroRepositoryID, chaos4099OrgID, "phantom/repo", "gitlab", at); err != nil {
		t.Fatalf("seed pathological zero-uuid repos row: %v", err)
	}

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	for _, target := range result.Targets {
		if target.CanonicalID == "repository:"+chaos4099ZeroRepositoryID {
			t.Fatalf("targets = %+v, the zero-UUID sentinel was admitted as a fake repository target -- this must NEVER happen regardless of what a repos row claims", result.Targets)
		}
	}
	if len(result.Targets) != 1 || result.Targets[0].CanonicalID != "repository:"+chaos4099GitLabRepoID {
		t.Fatalf("targets = %+v, want exactly the one genuine repository", result.Targets)
	}
}

// TestScopeExpander_ProjectToRepository_UnauthorizedPrincipalDropsTheCandidate
// pins the authorization wiring: a principal scoped to a DIFFERENT
// repository never admits the real one, and the drop is reported (never
// silently absorbed).
func TestScopeExpander_ProjectToRepository_UnauthorizedPrincipalDropsTheCandidate(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"someone-else/unrelated"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none: this principal is not authorized for the real repository", result.Targets)
	}
	if result.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1", result.Counts.AuthorizationDroppedCount)
	}
	if result.Counts.CandidateCount != 1 {
		t.Fatalf("CandidateCount = %d, want 1: the candidate existed, it was just dropped", result.Counts.CandidateCount)
	}
}

// TestScopeExpander_ProjectToPullRequest proves the second hop: repository
// -> pull requests, only from an authorized repository.
func TestScopeExpander_ProjectToPullRequest(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("targets = %+v, want the two seeded pull requests", result.Targets)
	}
	wantIDs := map[string]bool{
		"pull_request:" + chaos4099GitLabRepoID + ":1": false,
		"pull_request:" + chaos4099GitLabRepoID + ":2": false,
	}
	for _, target := range result.Targets {
		if target.Kind != contextfabric.SubjectPullRequest {
			t.Fatalf("target kind = %q, want pull_request", target.Kind)
		}
		if _, expected := wantIDs[target.CanonicalID]; !expected {
			t.Fatalf("unexpected target canonical id %q", target.CanonicalID)
		}
		wantIDs[target.CanonicalID] = true
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Fatalf("missing expected target %q", id)
		}
	}
}

// TestScopeExpander_ProjectToPullRequest_MixedAuthorizationOnlyAdmitsTheAuthorizedRepository
// proves the design doc's §9 "mixed authorized/unauthorized" activation
// precondition against REAL ClickHouse, not merely a fake-expander unit
// test (codex xhigh review round 2, confirmed real, LOW: the doc claimed
// this was proven by "dedicated integration tests ... with mixed
// authorized/unauthorized repos", but every existing integration test here
// authorized or dropped its ONE repository wholesale -- none actually
// mixed two repositories in a single call). A second repository, reachable
// from the SAME requested project, is authorized while the fixture's real
// repository is authorized too and the ONLY one denied is a brand-new
// second one -- so real pull-request facts DO reach the answer (from the
// authorized repository) alongside a genuine authorization drop (the
// second), the exact "mixed" case design doc §6b (2026-08-22 ruling)
// says stays `expanded`/`expanded_partial`, never `matched_unauthorized`.
func TestScopeExpander_ProjectToPullRequest_MixedAuthorizationOnlyAdmitsTheAuthorizedRepository(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	const mixedUnauthorizedRepoID = "a2518fbc-1945-3717-05d8-eb78866b4e83"
	const mixedUnauthorizedRepoSlug = "acme/other"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		mixedUnauthorizedRepoID, chaos4099OrgID, mixedUnauthorizedRepoSlug, "gitlab", at); err != nil {
		t.Fatalf("seed second, unauthorized repo: %v", err)
	}
	// A SECOND work item under the SAME requested project, so this second
	// repository is reachable from the SAME expansion call as the fixture's
	// authorized one.
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#46", mixedUnauthorizedRepoID, chaos4099OrgID, "GitLab issue (second repo)", "open", "", "", chaos4099GitLabProjectKey, at); err != nil {
		t.Fatalf("seed second-repo work item: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		mixedUnauthorizedRepoID, chaos4099OrgID, uint32(1), "Unauthorized PR", "open", at, at, "feat/unauth", ""); err != nil {
		t.Fatalf("seed second repo's pull request: %v", err)
	}

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		// Authorized for the fixture's real repository ONLY -- not the
		// second one, and not "*".
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{chaos4099GitLabRepoSlug}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	// Real facts DO reach the answer: the two PRs from the AUTHORIZED
	// repository, and nothing from the unauthorized one.
	if len(result.Targets) != 2 {
		t.Fatalf("targets = %+v, want the two seeded pull requests from the authorized repository only", result.Targets)
	}
	for _, target := range result.Targets {
		if target.CanonicalID == "pull_request:"+mixedUnauthorizedRepoID+":1" {
			t.Fatalf("targets = %+v, the unauthorized repository's pull request was admitted", result.Targets)
		}
	}
	if result.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1 (the second repository, dropped) -- this call mixed one authorized and one unauthorized repository", result.Counts.AuthorizationDroppedCount)
	}
	if result.Counts.CandidateCount != 2 {
		t.Fatalf("CandidateCount = %d, want 2 (both repositories were real candidates before authorization)", result.Counts.CandidateCount)
	}
}

// TestScopeExpander_ProjectToRepository_CrossProviderIDCollisionIsOmittedNotGuessed
// proves the ambiguity guard's DISTINCT step carries `provider` through
// (codex xhigh review round 2, confirmed real, MEDIUM): projects.id is only
// unique PER PROVIDER (teams_projects.go's own doc comment, dedup key
// (org_id, provider, id)), so two DIFFERENT providers' projects can
// schema-legally share the identical raw id string. Without `provider` in
// the ambiguity CTE's DISTINCT (id, join_key) step, such a collision
// silently collapses into ONE row and the guard never fires -- exactly the
// gap this test pins.
func TestScopeExpander_ProjectToRepository_CrossProviderIDCollisionIsOmittedNotGuessed(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	// A second project, DIFFERENT provider, whose id is IDENTICAL to the
	// requested project's own id (schema-legal: the dedup key is
	// (org_id, provider, id), not (org_id, id)).
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4099GitLabProjectID, chaos4099OrgID, "same id, different provider", "", "linear", "", "https://linear.app/other", uint8(1), at); err != nil {
		t.Fatalf("seed cross-provider colliding project: %v", err)
	}
	// Reachable ONLY via the requested project's OWN id arm -- the arm this
	// collision poisons.
	const crossProviderRepoID = "b3629fbc-1945-3717-05d8-eb78866b4e84"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		crossProviderRepoID, chaos4099OrgID, "acme/cross-provider", "gitlab", at); err != nil {
		t.Fatalf("seed cross-provider-collision repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#47", crossProviderRepoID, chaos4099OrgID, "GitLab issue (own-id arm, now poisoned)", "open", "", "", chaos4099GitLabProjectID, at); err != nil {
		t.Fatalf("seed own-id-arm work item: %v", err)
	}

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	for _, target := range result.Targets {
		if target.CanonicalID == "repository:"+crossProviderRepoID {
			t.Fatalf("targets = %+v, the cross-provider-collision repository was admitted -- the requested project's own id arm is ambiguous once a DIFFERENT provider's project shares that same raw id", result.Targets)
		}
	}
	// The project_key arm is untouched by this collision -- the fixture's
	// own real repository must still resolve.
	if len(result.Targets) != 1 || result.Targets[0].CanonicalID != "repository:"+chaos4099GitLabRepoID {
		t.Fatalf("targets = %+v, want exactly the one repository reached via the unaffected project_key arm", result.Targets)
	}
}

// TestScopeExpander_ProjectToPullRequest_UnauthorizedRepositoryNeverQueried
// proves content from a dropped repository is never even READ: an
// unauthorized principal gets zero pull-request targets, and the drop is
// attributed to the repository hop.
func TestScopeExpander_ProjectToPullRequest_UnauthorizedRepositoryNeverQueried(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"someone-else/unrelated"}},
		RequirementKind: contextfabric.FactPullRequests,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequest,
		TargetKind:      contextfabric.SubjectPullRequest,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("targets = %+v, want none", result.Targets)
	}
	if result.Counts.AuthorizationDroppedCount != 1 {
		t.Fatalf("AuthorizationDroppedCount = %d, want 1 (the repository, not the pull requests)", result.Counts.AuthorizationDroppedCount)
	}
}

// TestScopeExpander_ProjectToPullRequestReview proves the third hop:
// repository -> pull request -> review, and that the derived canonical id
// decodes through identity.Segments exactly the way
// devhealthsource/tables.go's queryPullRequestReviews' own minted id would.
func TestScopeExpander_ProjectToPullRequestReview(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactReviews,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemPullRequestReview,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	if len(result.Targets) != 1 {
		t.Fatalf("targets = %+v, want the one seeded review", result.Targets)
	}
	wantCanonicalID, omitted, err := identity.Derive(identity.KindPullRequestReview, []string{chaos4099GitLabRepoID, "1", "review-1"}, nil)
	if err != nil || omitted {
		t.Fatalf("derive want canonical id: err=%v omitted=%v", err, omitted)
	}
	if result.Targets[0].CanonicalID != wantCanonicalID {
		t.Fatalf("canonical id = %q, want %q (must decode identically to queryPullRequestReviews' own minted id)", result.Targets[0].CanonicalID, wantCanonicalID)
	}
	segments, ok := identity.Segments(identity.KindPullRequestReview, result.Targets[0].CanonicalID)
	if !ok || len(segments) != 3 || segments[0] != chaos4099GitLabRepoID || segments[1] != "1" || segments[2] != "review-1" {
		t.Fatalf("segments = %+v, ok=%v, want [%s 1 review-1]", segments, ok, chaos4099GitLabRepoID)
	}
}

// chaos4099AmbiguousProjectID is a SECOND, UNREQUESTED project's own id,
// deliberately equal to chaos4099GitLabProjectKey ("acme/api") -- the exact
// ambiguity shape devhealthsource/teams_projects_edges.go's own
// key_resolution_count guard exists to catch (codex xhigh review round 1,
// confirmed real, MEDIUM): a work item whose project_id is "acme/api"
// cannot be safely attributed to EITHER project by that value alone, since
// it matches chaos4099GitLabProjectID's own project_key arm AND this
// project's own id arm. Per queryWorkItemProjects' own documented
// discipline (teams_projects_edges.go:81, "an ambiguous value is omitted
// (never guessed)"), key_resolution_count > 1 omits EVERY claimant of that
// value -- including the requested project's own edge through it, not only
// the unrelated project's. This is why the fixture's real work item (#42,
// reachable ONLY via the poisoned project_key arm) must NOT survive either;
// see the dedicated own-id-arm work item below for the control case that
// proves the guard isn't simply dropping everything.
const chaos4099AmbiguousProjectID = "acme/api"

// TestScopeExpander_ProjectToRepository_AmbiguousJoinKeyIsOmittedNotGuessed
// proves the org-wide ambiguity guard the widened join's own SQL computes
// (mirroring queryWorkItemProjects' key_resolution_count exactly, restricted
// to the requested project only in the OUTER filter, after ambiguity is
// decided over the whole org). A version that computed key_resolution_count
// scoped only to the REQUESTED project ids (the pre-fix bug) would never see
// the second, unrequested project at all, and so would (a) wrongly admit the
// fixture's real repository through the now-poisoned project_key arm AND (b)
// wrongly fold the UNRELATED project's own work item into this project's
// results, since both share the same join_key string. The fix instead omits
// BOTH claimants of the poisoned value, while a work item reached through
// the requested project's own, non-colliding id arm still resolves --
// proving the guard is neither blind to cross-project collisions nor
// indiscriminately conservative.
func TestScopeExpander_ProjectToRepository_AmbiguousJoinKeyIsOmittedNotGuessed(t *testing.T) {
	ctx := context.Background()
	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	at := time.Now().UTC()
	seedChaos4099Fixture(t, ctx, direct, at)

	// A work item reachable ONLY via the requested project's OWN id arm
	// (chaos4099GitLabProjectID, never its project_key) -- the control case:
	// it must stay resolvable even after the project_key arm below becomes
	// ambiguous, proving the guard omits the specific poisoned value, not
	// the whole project.
	const unambiguousRepoID = "f1408fbc-1945-3717-05d8-eb78866b4e82"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		unambiguousRepoID, chaos4099OrgID, "acme/unambiguous", "gitlab", at); err != nil {
		t.Fatalf("seed unambiguous repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:acme/api#45", unambiguousRepoID, chaos4099OrgID, "GitLab issue (own-id arm)", "open", "", "", chaos4099GitLabProjectID, at); err != nil {
		t.Fatalf("seed own-id-arm work item: %v", err)
	}

	// The second, unrequested project: its own id collides with the
	// requested project's project_key ("acme/api"). This poisons that
	// shared join_key's key_resolution_count to 2.
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4099AmbiguousProjectID, chaos4099OrgID, "unrelated project", "", "gitlab", "", "https://gitlab.com/other", uint8(1), at); err != nil {
		t.Fatalf("seed ambiguous second project: %v", err)
	}
	// A second repository, reachable ONLY through the ambiguous work item
	// below -- under the pre-fix bug this repository would have been
	// wrongly folded into the REQUESTED project's own expansion.
	const ambiguousRepoID = "e9308fbc-1945-3717-05d8-eb78866b4e81"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		ambiguousRepoID, chaos4099OrgID, "unrelated/repo", "gitlab", at); err != nil {
		t.Fatalf("seed ambiguous repo: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab:unrelated#1", ambiguousRepoID, chaos4099OrgID, "Ambiguous work item", "open", "", "", chaos4099AmbiguousProjectID, at); err != nil {
		t.Fatalf("seed ambiguous work item: %v", err)
	}

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: chaos4099OrgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4099ProjectSubject(t)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	for _, target := range result.Targets {
		if target.CanonicalID == "repository:"+ambiguousRepoID {
			t.Fatalf("targets = %+v, the ambiguous work item's repository was admitted -- it cannot be safely attributed to this project, since its project_id also matches a DIFFERENT project's own id", result.Targets)
		}
		if target.CanonicalID == "repository:"+chaos4099GitLabRepoID {
			t.Fatalf("targets = %+v, the fixture's real repository was admitted through the now-poisoned project_key arm -- an ambiguous value must be omitted for EVERY claimant, not guessed in the requester's favor", result.Targets)
		}
	}
	// The work item reached via the requested project's own, non-colliding
	// id arm must still resolve -- the guard omits the poisoned VALUE, not
	// the entire project.
	if len(result.Targets) != 1 || result.Targets[0].CanonicalID != "repository:"+unambiguousRepoID {
		t.Fatalf("targets = %+v, want exactly the one repository reached via the requested project's own unambiguous id arm", result.Targets)
	}
}
