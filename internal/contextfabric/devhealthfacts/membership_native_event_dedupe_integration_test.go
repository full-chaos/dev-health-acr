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

// CHAOS-7829: the reader keeps ONE row per (org, subject, provider, event_id) -- the
// latest last_synced -- so a provider-native event stored at two occurred_at
// values is one touch, not an ADD plus a duplicate ADD.
func TestScopeExpanderAsOf_NativeEventStoredAtTwoOccurredAtIsOneTouch(t *testing.T) {
	ctx := context.Background()
	const orgID = "org-7829-native-event-two-occurred-at"
	const projectAID = "70000000-0000-4000-8000-000000000c"
	const repoID = "80000000-0000-4000-8000-000000000003"
	const repoSlug = "acme/native-event-two-occurred-at"
	const workItemID = "linear:CHAOS-7829"

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	query, direct := newChaos4099ScopeExpanderClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "repos", "work_items", "project_membership_transitions", "team_project_ownership", "team_repo_ownership", "work_graph_issue_pr") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	mustExec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	mustExec("project A", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectAID, orgID, "Project A", "", "linear", "backlog", "", uint8(1), t1)
	mustExec("repo", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, repoSlug, "linear", t1)
	mustExec("work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workItemID, repoID, orgID, "Duplicate-add history", "open", "", "", "linear", projectAID, t2)
	// TWO ADD touches of the same (work_item, project A) pair, no REMOVE in
	// between -- the continuation membershipTouchesAsOfSQL flags via
	// is_duplicate_add.
	mustExec("transition add A (1)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, projectAID, t1, t1, "linear:H-moved")
	mustExec("transition add A (2, duplicate)", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?)`,
		orgID, repoID, workItemID, projectAID, t2, t2, "linear:H-moved")

	expander := devhealthfacts.NewScopeExpander(query)
	result, err := expander.ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
		Principal:       storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}},
		RequirementKind: contextfabric.FactMetrics,
		Origins:         []contextfabric.SubjectRef{chaos4109ScopeProjectSubject(t, projectAID)},
		Policy:          contextfabric.FactScopePolicyProjectWorkItemRepository,
		TargetKind:      contextfabric.SubjectRepository,
		TimeContext:     contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("ExpandFactScope: %v", err)
	}
	// One native event stored at two occurred_at values is ONE touch (CHAOS-7829):
	// the interval is admitted once and the second copy is not counted as a
	// duplicate ADD.
	want := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoID, Label: repoSlug}
	if len(result.Targets) != 1 || result.Targets[0] != want {
		t.Fatalf("targets = %+v, want exactly [%+v]", result.Targets, want)
	}
	if result.Counts.DuplicateAddCount != 0 {
		t.Fatalf("DuplicateAddCount = %d, want 0 -- the two copies are one native event, not two ADD touches", result.Counts.DuplicateAddCount)
	}
	if result.Counts.MalformedTouchCount != 0 {
		t.Fatalf("MalformedTouchCount = %d, want 0", result.Counts.MalformedTouchCount)
	}
}
