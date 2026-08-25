package devhealthsource_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4108: ACR joined the wrong arm of a deliberate dual project-id
// space. Ops writes work_items.project_id differently per provider
// (normalize.py:165-176, native_status_change.py:161-220, CHAOS-3380 x3;
// mirrored faithfully by the Go worker port, gitlab_work_items_rows.go:228
// /:375): a LINEAR work item carries projects.id verbatim, but a GITLAB work
// item carries projects.project_key (a rename-safe project_full_path, e.g.
// "full.chaos/dev-health-ops") -- never projects.id. querySubjectProjectMemberships'
// pre-fix INNER JOIN (p.id = w.project_id) and the census project anchor
// predicate (built from canonicalIDValue's raw projects.id extraction) both
// only ever tried the id arm, so every gitlab work item joined to NO
// project, live-verified.
//
// This file proves the fix against a REAL ClickHouse (a fake client cannot
// execute the widened JOIN's window functions or the anchor predicate's
// scoped subquery) with one gitlab-shaped work item (matched via the
// project_key arm) seeded ALONGSIDE one Linear-shaped work item (matched via
// the unchanged id arm, in the SAME organization) -- proving the widening
// added a match rather than silently changing which single arm wins.

// chaos4108OrgID is this file's own organization -- distinct from every
// other integration test's org id in this package, so a shared-container
// test run can never let one test's rows answer for another's.
const chaos4108OrgID = "org-4108-join-arm"

// chaos4108GitLabWorkItemID/RepoID/ProjectKey are NOT hand-typed: they are
// the VERBATIM output of ops's own real gitlab work-item producer
// (dev-health-ops internal/providersync/gitlab_work_items_rows.go's
// normalizeGitLabIssueWorkItem, called through its own existing oracle test
// harness -- gitlab_work_items_oracle_test.go's "open_labeled_issue" case,
// input repo_full_name="acme/api"/repo_id="c7198fbc-1945-3717-05d8-eb78866b4e79",
// captured 2026-08-22) -- never invented. The captured row's own
// project_id field ("acme/api") is exactly gitlab_work_items_rows.go:228's
// ProjectID: stringPointer(fullName): the project_key value, never
// projects.id -- the defect this ticket fixes.
const chaos4108GitLabWorkItemID = "gitlab:acme/api#42"
const chaos4108GitLabRepoID = "c7198fbc-1945-3717-05d8-eb78866b4e79"
const chaos4108GitLabProjectKey = "acme/api"

// chaos4108GitLabRepoSlug is repos.repo for chaos4108GitLabRepoID -- the
// SAME repo_full_name ("acme/api") the captured row's own repo_id was seeded
// under in ops's oracle fixture, so "carries its repo" (CHAOS-4108's own
// acceptance framing) resolves through the SAME real vocabulary the project
// side uses, proven by this row's repos join (unaffected by this fix,
// exercised alongside it so the regression fixture covers the full
// activity-proxy chain: project <-BELONGS_TO_PROJECT- work_item
// -BELONGS_TO_REPOSITORY-> repository, in one seed).
const chaos4108GitLabRepoSlug = "acme/api"

// chaos4108GitLabProjectID is the project's OWN canonical id column
// (projects.id) -- the provider-composite shape gitlab projects carry,
// live-verified in this package's OWN existing fixture (teams_projects_test.go's
// liveShapedTeamsProjectsClient, read from the ground-truth org
// 2026-08-13): "<org>:gitlab:<n>". ops's projects-table normalizer is still
// Python-only (normalize.py:165-176, cited by this ticket) with no Go port
// to capture a value from the same way as the work-item row above, so this
// reuses that already-live-verified SHAPE with a placeholder org/number, the
// SAME synthetic-id convention this file's sibling integration tests already
// use for ids whose exact value is not itself under test (e.g.
// collidingRepoID in clickhouse_org_isolation_integration_test.go). This is
// DELIBERATELY NOT what work_items.project_id holds for this row (that is
// chaos4108GitLabProjectKey above) -- that mismatch is the defect.
const chaos4108GitLabProjectID = "org-4108-join-arm:gitlab:71133891"

// chaos4108LinearProjectID is a bare Linear project id -- the OTHER arm,
// seeded so this fixture proves the widened predicate still resolves the
// id-space case it always handled, not just the newly-fixed one.
const chaos4108LinearProjectID = "6000fcb5-c3e9-49ff-b17c-07877aaac001"

func seedChaos4108DualArmFixture(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}, at time.Time) {
	t.Helper()
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}

	// The gitlab project: id space (a) and project_key space (b) are BOTH
	// present, and deliberately differ from each other -- exactly the
	// production shape.
	mustSeed("gitlab project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108GitLabProjectID, chaos4108OrgID, "dev-health-ops", chaos4108GitLabProjectKey, "gitlab", "", "https://gitlab.com/"+chaos4108GitLabProjectKey, uint8(1), at)
	mustSeed("gitlab repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4108GitLabRepoID, chaos4108OrgID, chaos4108GitLabRepoSlug, "gitlab", at)
	// project_id holds the PROJECT_KEY value, never projects.id -- the exact
	// shape gitlab_work_items_rows.go's normalizeGitLabIssueWorkItem/
	// normalizeGitLabMergeRequestWorkItem write. provider=gitlab is now
	// explicit (CHAOS-4193): project_membership_presence's work_item_column
	// arm refuses gitlab rows outright (`w.provider != 'gitlab'`, Context
	// Fabric ruling 2026-08-24 09:55 -- "GitLab's own 'project' concept IS
	// this schema's repo_id"), so this row is deliberately no longer a
	// BELONGS_TO_PROJECT source at all -- see
	// TestQueryWorkItemProjectsGitLabColumnArmIsRetired below.
	mustSeed("gitlab work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108GitLabWorkItemID, chaos4108GitLabRepoID, chaos4108OrgID, "GitLab issue", "open", "", "", "gitlab", chaos4108GitLabProjectKey, at)

	// The Linear project/work item: project_id holds projects.id verbatim --
	// the id arm, unaffected by this fix, seeded to prove it still resolves.
	mustSeed("linear project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108LinearProjectID, chaos4108OrgID, "Chaos Draw", "", "linear", "backlog", "https://linear.app/fullchaos/project/chaos-draw", uint8(1), at)
	mustSeed("linear work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"linear:CHAOS-9001", zeroRepositoryUUID, chaos4108OrgID, "Linear issue", "open", "", "", "linear", chaos4108LinearProjectID, at)
}

// TestQueryWorkItemProjectsResolvesTheProjectKeyArm is CHAOS-4108's Linear
// half, still live post-CHAOS-4193: a Linear work item resolves its project
// via the id arm of the dual id/project_key widening
// (querySubjectProjectMemberships, teams_projects_edges.go), reading
// through project_membership_presence's work_item_column arm rather than
// work_items.project_id directly, but with the SAME join semantics on the
// project side. The gitlab half of this fixture is asserted separately by
// TestGitLabColumnArmProducesNoProjectEdgeAnymore below -- CHAOS-4193
// retired it, so this test no longer proves it.
func TestQueryWorkItemProjectsResolvesTheProjectKeyArm(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	seedChaos4108DualArmFixture(t, ctx, direct, at)

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4108OrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}

	wantLinearWorkItemCanonicalID, _, err := identity.Derive(identity.KindWorkItem, []string{zeroRepositoryUUID, "linear:CHAOS-9001"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(work_item, linear): %v", err)
	}
	wantLinearProjectCanonicalID, _, err := identity.Derive(identity.KindProject, []string{"linear", chaos4108LinearProjectID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(project, linear): %v", err)
	}

	foundLinearEdge := false
	for _, relationship := range batch.Relationships {
		if relationship.Type != contractsv1.ContextFabricRelationshipBelongsToProject {
			continue
		}
		if relationship.From.CanonicalID == wantLinearWorkItemCanonicalID {
			foundLinearEdge = true
			if relationship.To.CanonicalID != wantLinearProjectCanonicalID {
				t.Fatalf("linear work item's BELONGS_TO_PROJECT target = %q, want %q (the id arm, unaffected by this fix)", relationship.To.CanonicalID, wantLinearProjectCanonicalID)
			}
		}
	}
	if !foundLinearEdge {
		t.Fatalf("expected a BELONGS_TO_PROJECT edge for the linear work item (the id arm, must remain unaffected): relationships=%+v", batch.Relationships)
	}
}

// TestGitLabColumnArmProducesNoProjectEdgeAnymore is CHAOS-4193's own
// regression against the SAME chaos4108GitLabWorkItemID fixture
// TestQueryWorkItemProjectsResolvesTheProjectKeyArm used to prove a gitlab
// BELONGS_TO_PROJECT edge FOR (CHAOS-4108). Context Fabric ruling
// 2026-08-24 09:55 retired that path on purpose:
// project_membership_presence's work_item_column arm refuses every gitlab
// row outright ("GitLab's own 'project' concept IS this schema's repo_id"),
// and gitlab is not among the {github, jira, linear} systems CHAOS-4193/4194
// register a transition producer for either -- so a gitlab work item's
// project membership is, as of this version, entirely unobservable through
// this producer. That is a real, ruled capability gap (recorded in this
// producer's TeamsProjectsSourceVersion v4 doc comment), not a bug this
// test papers over: it exists so a FUTURE change that accidentally makes a
// gitlab edge reappear (e.g. a careless widening of the view's WHERE
// clause) fails loudly, the same "observe every guard failing" discipline
// AGENTS.md's four verification rules require.
func TestGitLabColumnArmProducesNoProjectEdgeAnymore(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	seedChaos4108DualArmFixture(t, ctx, direct, at)

	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4108OrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available (the Linear edge still projects)")
	}

	wantGitLabWorkItemCanonicalID, _, err := identity.Derive(identity.KindWorkItem, []string{chaos4108GitLabRepoID, chaos4108GitLabWorkItemID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(work_item, gitlab): %v", err)
	}
	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject && relationship.From.CanonicalID == wantGitLabWorkItemCanonicalID {
			t.Fatalf("found a BELONGS_TO_PROJECT edge for the gitlab work item, want none -- CHAOS-4193/ruling 2026-08-24 09:55 retired this path: %+v", relationship)
		}
	}
}

// chaos4193AmbiguousFanoutOrgID is this test's own organization -- distinct
// from every sibling integration test's org id (this file's own convention,
// chaos4108OrgID's doc comment), so its ambiguous project pair cannot be
// joined against another test's presence rows in a shared-container run.
const chaos4193AmbiguousFanoutOrgID = "org-4193-ambiguous-fanout"

// chaos4193AmbiguousFanoutSharedKey is deliberately BOTH one project's raw
// id AND a second, different project's project_key, within the SAME
// provider -- the exact "join_key resolves to more than one project" shape
// key_resolution_count exists to detect (querySubjectProjectMemberships,
// teams_projects_edges.go).
const chaos4193AmbiguousFanoutSharedKey = "ambiguous-shared-key"
const chaos4193AmbiguousFanoutProjectA = chaos4193AmbiguousFanoutSharedKey
const chaos4193AmbiguousFanoutProjectB = "org-4193-ambiguous-fanout:linear:other-project"
const chaos4193AmbiguousFanoutWorkItemID = "linear:CHAOS-4193-FANOUT"

// TestAmbiguousProjectKeyIsCountedOncePerPresenceRow is codex xhigh review
// R2's Medium finding, fixed: the projects-side subquery's LEFT JOIN used
// to fan out to key_resolution_count rows for a SINGLE ambiguous presence
// row (one real dropped row scanned N times), each scan independently
// incrementing presenceTelemetryLedger.ambiguousRows -- overcounting the
// very fan-out metric TestUnresolvedFanOutIsCountedByRowNotJustByDistinctKey
// (chaos4193_presence_read_swap_test.go, R1's fix) exists to report
// accurately. `LIMIT 1 BY provider, join_key` deduplicates the join's build
// side to one representative row per ambiguous group while
// key_resolution_count (a window function, computed before that LIMIT BY
// trims anything) still carries the group's true size -- proven here only
// against a REAL ClickHouse, because chaos4193_presence_read_swap_test.go's
// fake client cannot execute the window function or the multi-row join this
// defect and its fix both depend on (R2's own citation: the fake fixture's
// keyResolutionCount=2 field is supplied directly, never produced by an
// actual multi-result join).
func TestAmbiguousProjectKeyIsCountedOncePerPresenceRow(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	// Project A's raw id IS project B's project_key -- one join_key value,
	// two distinct projects, same provider (key_resolution_count's
	// PARTITION BY provider, join_key requires both to match for ambiguity).
	mustSeed("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4193AmbiguousFanoutProjectA, chaos4193AmbiguousFanoutOrgID, "Project A", "", "linear", "backlog", "", uint8(1), at)
	mustSeed("project B sharing A's raw id as its own project_key", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4193AmbiguousFanoutProjectB, chaos4193AmbiguousFanoutOrgID, "Project B", chaos4193AmbiguousFanoutSharedKey, "linear", "backlog", "", uint8(1), at)
	// ONE work item, ONE presence row (the view's work_item_column arm --
	// provider=linear passes its provider guard unconditionally), whose
	// project_id is the ambiguous shared key. Pre-fix this single row
	// scanned twice (once per colliding project); post-fix, once.
	mustSeed("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4193AmbiguousFanoutWorkItemID, zeroRepositoryUUID, chaos4193AmbiguousFanoutOrgID, "Fanout issue", "open", "", "", "linear", chaos4193AmbiguousFanoutSharedKey, at)

	logged := &bytes.Buffer{}
	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if _, _, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: chaos4193AmbiguousFanoutOrgID, Source: devhealthsource.TeamsProjectsSourceName}); err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}

	output := logged.String()
	if !strings.Contains(output, "ambiguous_project_entity=1") {
		t.Fatalf("want ambiguous_project_entity=1 (one distinct ambiguous key), got:\n%s", output)
	}
	if !strings.Contains(output, "ambiguous_project_entity_rows=1") {
		t.Fatalf("want ambiguous_project_entity_rows=1 (the ONE real presence row, not fanned out to 2 by the ambiguous join) -- codex R2's fan-out finding regressed if this reads 2, got:\n%s", output)
	}
}

// TestCensusProjectAnchorCountsBothArms is the census-execution half of
// CHAOS-4108's regression, run at the SAME level the three live silent
// answer defects (classifyCandidate false elimination, the single-satisfier
// rescue, kindInsensitivityNoMatchSound's false-sound no_match) actually
// consume: BuildCensusDiscriminator + RunCensus against real ClickHouse, not
// a SQL-string assertion. Pre-fix, a work_item census anchored on the gitlab
// project's canonical id read Count=0 (a false proof of absence) even though
// the gitlab work item genuinely exists and belongs to it. Both arms are
// exercised in the SAME test so a regression that fixes one by breaking the
// other cannot pass silently.
func TestCensusProjectAnchorCountsBothArms(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	seedChaos4108DualArmFixture(t, ctx, direct, at)

	censusFor := func(t *testing.T, anchorCanonicalID string) int {
		t.Helper()
		predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "", false, contextfabric.SubjectProject, anchorCanonicalID, true)
		if err != nil {
			t.Fatalf("BuildCensusDiscriminator(%q): %v", anchorCanonicalID, err)
		}
		result, err := devhealthsource.RunCensus(ctx, query, chaos4108OrgID, contextfabric.SubjectWorkItem, predicate)
		if err != nil {
			t.Fatalf("RunCensus(%q): %v", anchorCanonicalID, err)
		}
		return result.Count
	}

	gitlabAnchorID, _, err := identity.Derive(identity.KindProject, []string{"gitlab", chaos4108GitLabProjectID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(project, gitlab): %v", err)
	}
	linearAnchorID, _, err := identity.Derive(identity.KindProject, []string{"linear", chaos4108LinearProjectID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(project, linear): %v", err)
	}

	if count := censusFor(t, gitlabAnchorID); count != 1 {
		t.Fatalf("census count for the gitlab project anchor (project_key arm) = %d, want 1 -- pre-fix this read 0, a false proof of absence", count)
	}
	if count := censusFor(t, linearAnchorID); count != 1 {
		t.Fatalf("census count for the linear project anchor (id arm) = %d, want 1 -- the unaffected arm must still count correctly", count)
	}
}

// chaos4108HandleMismatchWorkItemID shares chaos4108GitLabWorkItemID's
// project (project_id = chaos4108GitLabProjectKey) but carries a DIFFERENT
// ticket key, so a handle bound to chaos4108GitLabWorkItemID's own key must
// never match it -- codex xhigh review round-1 finding F1's regression
// (unparenthesized OR letting the project_key arm escape a handle AND).
const chaos4108HandleMismatchWorkItemID = "gitlab:acme/api#99"

// TestCensusProjectAnchorGroupingSurvivesAHandle is codex xhigh review
// round-1 finding F1's regression, proven functionally (not just as a SQL
// string) against real ClickHouse: BuildCensusDiscriminator joins the handle
// and anchor fragments with " AND ". Because the project anchor's own SQL
// disjoins (id arm OR project_key arm), an unparenthesized join lets SQL's
// AND-binds-tighter-than-OR precedence rewrite "handle AND id_arm OR
// key_arm" into "(handle AND id_arm) OR key_arm" -- silently dropping the
// handle requirement for any row satisfying only the project_key arm. A
// second gitlab work item, seeded in the SAME project via the project_key
// arm but under a DIFFERENT ticket key, must be excluded when the census is
// bound to the FIRST work item's own handle.
func TestCensusProjectAnchorGroupingSurvivesAHandle(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	seedChaos4108DualArmFixture(t, ctx, direct, at)
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108HandleMismatchWorkItemID, chaos4108GitLabRepoID, chaos4108OrgID, "GitLab issue (different ticket)", "open", "", "", chaos4108GitLabProjectKey, at); err != nil {
		t.Fatalf("seed handle-mismatch gitlab work item: %v", err)
	}

	gitlabAnchorID, _, err := identity.Derive(identity.KindProject, []string{"gitlab", chaos4108GitLabProjectID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(project, gitlab): %v", err)
	}
	// The handle grammar extracts the substring after chaos4108GitLabWorkItemID's
	// FIRST ':' -- "acme/api#42" -- matching ONLY that one work item, never
	// chaos4108HandleMismatchWorkItemID's "acme/api#99".
	predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "acme/api#42", true, contextfabric.SubjectProject, gitlabAnchorID, true)
	if err != nil {
		t.Fatalf("BuildCensusDiscriminator: %v", err)
	}
	result, err := devhealthsource.RunCensus(ctx, query, chaos4108OrgID, contextfabric.SubjectWorkItem, predicate)
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("census count with handle+anchor both bound = %d, want 1 -- an unparenthesized OR would let the handle-mismatched work item (same project, wrong ticket key) count too", result.Count)
	}
	// Not just the COUNT: the decisive satisfier must be the target work
	// item itself, not the handle-mismatched one -- codex xhigh review
	// round-2 finding P2 (a mutation that excluded the target and counted
	// only the mismatched row would otherwise still pass a Count==1 check).
	wantSatisfier := chaos4108OrgID + ":" + chaos4108GitLabRepoID + ":" + chaos4108GitLabWorkItemID
	if result.SatisfierNaturalKey != wantSatisfier {
		t.Fatalf("SatisfierNaturalKey = %q, want %q (the target work item, not the handle-mismatched one)", result.SatisfierNaturalKey, wantSatisfier)
	}
}

// chaos4108SharedProjectKey is DELIBERATELY carried by TWO different
// projects (chaos4108AmbiguousKeyProjectID, gitlab, and
// chaos4108AmbiguousKeyCollidingProjectID, github) -- codex xhigh review
// round-1 finding F2's regression: project_key is documented unique only
// WITHIN a provider (teams_projects_edges.go's own THIRD note), so an anchor
// predicate that trusts an ambiguous key without checking could match a
// foreign project's work item.
const chaos4108SharedProjectKey = "shared/ambiguous-key"
const chaos4108AmbiguousKeyProjectID = "org-4108-join-arm:gitlab:80001"
const chaos4108AmbiguousKeyCollidingProjectID = "org-4108-join-arm:github:80002"
const chaos4108ForeignWorkItemID = "gitlab:shared/ambiguous-key#7"
const chaos4108ForeignRepoID = "50000000-0000-4000-8000-000000004108"

// chaos4108AmbiguousKeyOwnWorkItemID matches the gitlab project's OWN raw id
// (never its ambiguous project_key) -- codex xhigh review round-2 finding P2:
// the fallback-to-id-only-arm claim needs a work item that actually exercises
// it, not just an absence of the foreign one.
const chaos4108AmbiguousKeyOwnWorkItemID = "gitlab:own/repo#5"
const chaos4108AmbiguousKeyOwnRepoID = "60000000-0000-4000-8000-000000004108"

// TestCensusProjectAnchorRefusesAnAmbiguousProjectKey is codex xhigh review
// round-1 finding F2's regression, proven against real ClickHouse: two
// projects sharing the SAME project_key, and one work item carrying that key
// as its own project_id. A census anchored on EITHER project must NOT count
// that work item -- the ambiguous key can name either project, so
// BuildCensusDiscriminator's project-anchor predicate must omit the
// project_key arm entirely rather than guess. Round 2 additionally proves
// the two claims round 1's test left unproven (codex finding P2): the
// id-only arm still counts a work item that genuinely uses the project's raw
// id (the "falls back" half of the claim), and the SAME refusal holds
// symmetrically when anchored on the OTHER (github) project sharing the key.
func TestCensusProjectAnchorRefusesAnAmbiguousProjectKey(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
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
		chaos4108AmbiguousKeyProjectID, chaos4108OrgID, "colliding-a", chaos4108SharedProjectKey, "gitlab", "", "https://gitlab.com/"+chaos4108SharedProjectKey, uint8(1), at)
	mustSeed("github project sharing the same key", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108AmbiguousKeyCollidingProjectID, chaos4108OrgID, "colliding-b", chaos4108SharedProjectKey, "github", "", "https://github.com/"+chaos4108SharedProjectKey, uint8(1), at)
	mustSeed("foreign repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4108ForeignRepoID, chaos4108OrgID, chaos4108SharedProjectKey, "gitlab", at)
	mustSeed("foreign work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108ForeignWorkItemID, chaos4108ForeignRepoID, chaos4108OrgID, "Foreign issue", "open", "", "", chaos4108SharedProjectKey, at)
	mustSeed("gitlab repo's own repo row", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4108AmbiguousKeyOwnRepoID, chaos4108OrgID, "own/repo", "gitlab", at)
	// project_id here is the gitlab project's own RAW id (the id arm), never
	// its ambiguous project_key -- proves the id-only arm keeps working even
	// while the project_key arm is refused for ambiguity.
	mustSeed("work item using the project's own raw id", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108AmbiguousKeyOwnWorkItemID, chaos4108AmbiguousKeyOwnRepoID, chaos4108OrgID, "Own-id issue", "open", "", "", chaos4108AmbiguousKeyProjectID, at)

	censusFor := func(t *testing.T, provider, projectID string) devhealthsource.CensusPredicate {
		t.Helper()
		anchorID, _, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
		if err != nil {
			t.Fatalf("identity.Derive(project, %s): %v", provider, err)
		}
		predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "", false, contextfabric.SubjectProject, anchorID, true)
		if err != nil {
			t.Fatalf("BuildCensusDiscriminator: %v", err)
		}
		return predicate
	}
	runCensus := func(t *testing.T, predicate devhealthsource.CensusPredicate) devhealthsource.CensusResult {
		t.Helper()
		result, err := devhealthsource.RunCensus(ctx, query, chaos4108OrgID, contextfabric.SubjectWorkItem, predicate)
		if err != nil {
			t.Fatalf("RunCensus: %v", err)
		}
		return result
	}

	// The gitlab anchor: the id-only arm still finds its own work item (1),
	// never the foreign one that only matches via the ambiguous key.
	gitlabResult := runCensus(t, censusFor(t, "gitlab", chaos4108AmbiguousKeyProjectID))
	if gitlabResult.Count != 1 {
		t.Fatalf("gitlab anchor census count = %d, want 1 -- the id-only arm must still count the project's own work item even while the ambiguous project_key arm is refused", gitlabResult.Count)
	}
	wantSatisfier := chaos4108OrgID + ":" + chaos4108AmbiguousKeyOwnRepoID + ":" + chaos4108AmbiguousKeyOwnWorkItemID
	if gitlabResult.SatisfierNaturalKey != wantSatisfier {
		t.Fatalf("gitlab anchor SatisfierNaturalKey = %q, want %q (the id-arm work item, never the foreign key-colliding one)", gitlabResult.SatisfierNaturalKey, wantSatisfier)
	}

	// The github anchor (the OTHER project sharing the same ambiguous key,
	// with no work item of its own using its raw id): must read 0,
	// symmetrically -- the ambiguity is refused regardless of which of the
	// two colliding projects is the anchor.
	githubResult := runCensus(t, censusFor(t, "github", chaos4108AmbiguousKeyCollidingProjectID))
	if githubResult.Count != 0 {
		t.Fatalf("github anchor census count = %d, want 0 -- the SAME ambiguous key must be refused symmetrically for the OTHER colliding project", githubResult.Count)
	}
}

// chaos4108CollidingRawID is DELIBERATELY carried by TWO different projects
// of DIFFERENT providers (chaos4108CollidingIDProjectA/B) -- codex xhigh
// review round-2 finding P1 (F3): projects.id is unique only per
// (org, provider, id) (devhealthschema's own declared sort key), while
// work_items.project_id has no provider column at all (AnchorCollision's own
// doc comment, chaos3898_s3_census_bridge.go). Filtering the project_key
// lookup on a bare `id = {census_anchor_id}` -- without also checking the id
// itself is unambiguous -- would return BOTH providers' project_keys for a
// colliding id, letting a foreign provider's work item satisfy this anchor.
const chaos4108CollidingRawID = "org-4108-join-arm:colliding-raw-id"
const chaos4108CollidingIDProjectA = chaos4108CollidingRawID
const chaos4108CollidingIDProjectB = chaos4108CollidingRawID
const chaos4108CollidingIDProjectAKey = "provider-a/own-key"
const chaos4108CollidingIDProjectBKey = "provider-b/own-key"
const chaos4108CollidingIDForeignWorkItemID = "providerb:provider-b/own-key#1"
const chaos4108CollidingIDForeignRepoID = "70000000-0000-4000-8000-000000004108"

// TestCensusProjectAnchorRefusesAnAmbiguousRawID is codex xhigh review
// round-2 finding P1 (F3)'s regression, proven against real ClickHouse: two
// projects of DIFFERENT providers sharing the exact same raw id, each with
// its OWN distinct (individually-unique) project_key. A census anchored on
// EITHER project must NOT count a work item that only matches via the
// OTHER project's own key -- the colliding id must invalidate the whole
// project_key lookup for both rows, not just leave each key looking safe on
// its own.
func TestCensusProjectAnchorRefusesAnAmbiguousRawID(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
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
	mustSeed("provider-a project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CollidingIDProjectA, chaos4108OrgID, "colliding-id-a", chaos4108CollidingIDProjectAKey, "providera", "", "https://providera.example/"+chaos4108CollidingIDProjectAKey, uint8(1), at)
	mustSeed("provider-b project sharing the same raw id", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CollidingIDProjectB, chaos4108OrgID, "colliding-id-b", chaos4108CollidingIDProjectBKey, "providerb", "", "https://providerb.example/"+chaos4108CollidingIDProjectBKey, uint8(1), at)
	mustSeed("foreign repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4108CollidingIDForeignRepoID, chaos4108OrgID, chaos4108CollidingIDProjectBKey, "providerb", at)
	mustSeed("foreign work item (provider-b's own key)", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CollidingIDForeignWorkItemID, chaos4108CollidingIDForeignRepoID, chaos4108OrgID, "Foreign issue", "open", "", "", chaos4108CollidingIDProjectBKey, at)

	anchorID, _, err := identity.Derive(identity.KindProject, []string{"providera", chaos4108CollidingIDProjectA}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(project, providera): %v", err)
	}
	predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "", false, contextfabric.SubjectProject, anchorID, true)
	if err != nil {
		t.Fatalf("BuildCensusDiscriminator: %v", err)
	}
	result, err := devhealthsource.RunCensus(ctx, query, chaos4108OrgID, contextfabric.SubjectWorkItem, predicate)
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("census count anchored on a project whose raw id collides with another provider's project = %d, want 0 -- a colliding id must invalidate the project_key lookup entirely, never leak the OTHER provider's key into the match set", result.Count)
	}
}

// chaos4108CrossSpaceProjectID/Key is a target project (gitlab) whose OWN
// project_key equals a DIFFERENT project's OWN raw id -- codex xhigh review
// round-3 finding P1's exact repro: an id-only ambiguity count and a
// key-only ambiguity count each individually see this value as "unique" (it
// is the ONLY id equal to it, and the ONLY key equal to it), but the two
// spaces collide against EACH OTHER once compared against the SAME column
// (work_items.project_id).
const chaos4108CrossSpaceProjectID = "org-4108-join-arm:gitlab:90001"
const chaos4108CrossSpaceProjectKey = "cross-space/shared-value"
const chaos4108CrossSpaceForeignProjectID = "cross-space/shared-value"
const chaos4108CrossSpaceGitLabWorkItemID = "gitlab:cross-space-own-id#1"
const chaos4108CrossSpaceGitLabRepoID = "80000000-0000-4000-8000-000000004108"
const chaos4108CrossSpaceForeignWorkItemID = "linear:cross-space-foreign"

// TestCensusProjectAnchorRefusesACrossSpaceCollision is codex xhigh review
// round-3 finding P1's regression, proven against real ClickHouse: the
// target gitlab project's own project_key exactly equals a DIFFERENT
// (linear) project's own raw id. A linear work item genuinely and correctly
// belongs to the LINEAR project via the id arm (project_id = the linear
// project's own id, which happens to equal the gitlab project's key) --
// anchoring on the GITLAB project must never count it. The gitlab project's
// own genuine work item, matched via its OWN raw id (unaffected by the
// cross-space collision on its key), must still be counted.
func TestCensusProjectAnchorRefusesACrossSpaceCollision(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
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
	mustSeed("gitlab target project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CrossSpaceProjectID, chaos4108OrgID, "cross-space-gitlab", chaos4108CrossSpaceProjectKey, "gitlab", "", "https://gitlab.com/"+chaos4108CrossSpaceProjectKey, uint8(1), at)
	mustSeed("linear foreign project whose id equals the gitlab project's key", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CrossSpaceForeignProjectID, chaos4108OrgID, "cross-space-linear", "", "linear", "backlog", "https://linear.app/cross-space", uint8(1), at)
	mustSeed("gitlab repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		chaos4108CrossSpaceGitLabRepoID, chaos4108OrgID, "cross-space/own-repo", "gitlab", at)
	// The gitlab project's OWN genuine work item, matched via its OWN raw id
	// -- unaffected by the cross-space collision on its key.
	mustSeed("gitlab's own work item (id arm)", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CrossSpaceGitLabWorkItemID, chaos4108CrossSpaceGitLabRepoID, chaos4108OrgID, "GitLab issue (own id)", "open", "", "", chaos4108CrossSpaceProjectID, at)
	// The LINEAR project's own genuine work item, matched via ITS OWN id --
	// which happens to equal the gitlab project's key. This work item
	// legitimately belongs to LINEAR, never to the gitlab anchor.
	mustSeed("linear's own work item (id arm, colliding value)", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos4108CrossSpaceForeignWorkItemID, zeroRepositoryUUID, chaos4108OrgID, "Linear issue (colliding id)", "open", "", "", chaos4108CrossSpaceForeignProjectID, at)

	anchorID, _, err := identity.Derive(identity.KindProject, []string{"gitlab", chaos4108CrossSpaceProjectID}, nil)
	if err != nil {
		t.Fatalf("identity.Derive(project, gitlab): %v", err)
	}
	predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectWorkItem, "", false, contextfabric.SubjectProject, anchorID, true)
	if err != nil {
		t.Fatalf("BuildCensusDiscriminator: %v", err)
	}
	result, err := devhealthsource.RunCensus(ctx, query, chaos4108OrgID, contextfabric.SubjectWorkItem, predicate)
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("census count anchored on the gitlab project = %d, want 1 -- must count its own id-arm work item but never the linear work item whose id happens to equal the gitlab project's key", result.Count)
	}
	wantSatisfier := chaos4108OrgID + ":" + chaos4108CrossSpaceGitLabRepoID + ":" + chaos4108CrossSpaceGitLabWorkItemID
	if result.SatisfierNaturalKey != wantSatisfier {
		t.Fatalf("SatisfierNaturalKey = %q, want %q (gitlab's own work item, never the cross-space-colliding linear one)", result.SatisfierNaturalKey, wantSatisfier)
	}
}
