package devhealthfacts_test

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestFlowProviderProjectRollupSumsAcrossTeamOwnScopesAndProviders proves
// codex R2 P1's fix against a REAL ClickHouse server, not a fakeClient: the
// bug was a SQL-only defect (readProjectFlow's row_number() partitioned by
// team_id ALONE, so ClickHouse itself -- not this package's Go loop --
// picked one arbitrary (provider, work_scope_id) row per team and silently
// discarded the rest), so a fakeClient unit test (which hands back
// pre-baked rows, bypassing real SQL execution) cannot exercise it; only a
// real server evaluating the real aggregation can.
//
// Seeds ONE team owning ONE project, but with work_item_metrics_daily rows
// from TWO distinct (provider, work_scope_id) pairs for that same team.
// Before the fix, the project rollup's items_started/items_completed would
// reflect only ONE of the two rows (whichever row_number()'s tiebreak
// picked); after the fix, both rows are summed into that team's single
// team_breakdown entry.
func TestFlowProviderProjectRollupSumsAcrossTeamOwnScopesAndProviders(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "work_item_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-flow-project-rollup"
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-flow", orgID, "linear", "FLOW1", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		// Left in place deliberately: flow no longer consults ownership at
		// all, so this row existing (and pointing at an irrelevant
		// project_id) proves the read is independent of it.
		orgID, "linear", "team-flow-a", "irrelevant", "FLOW1", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed team_project_ownership: %v", err)
	}
	// CHAOS-4521b: the work scopes are the PROJECT'S OWN identity, not an
	// unrelated sprint id. The fixture previously seeded `sprint-5` and
	// relied on the ownership hop to reach it -- which is precisely the
	// defect this ticket removed: a project's flow was assembled from every
	// work scope its owning team touched, other projects' rows included.
	//
	// What the case actually covers is unchanged and still worth covering:
	// two rows for the SAME team under DIFFERENT providers, sharing one
	// work_scope_id string, must BOTH count. That is also why
	// ProjectIdentityMatchSQL deliberately does not match on provider --
	// cross-provider equal ids are one project by design here, so a
	// provider equality would drop the github row.
	seedScope := func(provider, workScopeID string, itemsStarted, itemsCompleted uint32) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			date(2026, 8, 12), provider, workScopeID, "team-flow-a", itemsStarted, itemsCompleted, uint32(1), float64(4), float64(8), float64(0.1), float64(2), ts(2026, 8, 12, 6, 0, 0), orgID); err != nil {
			t.Fatalf("seed work_item_metrics_daily(%s, %s): %v", provider, workScopeID, err)
		}
	}
	seedScope("linear", "proj-flow", 10, 6)
	seedScope("github", "proj-flow", 4, 3) // same work_scope_id string, DIFFERENT provider

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-flow")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1", result.Facts)
	}
	fact := result.Facts[0]
	if got := fact.Fields["team_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_count = %#v, want 1", fact.Fields["team_count"])
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 1 {
		t.Fatalf("team_breakdown = %#v, want 1 row (one team, aggregated)", rows)
	}
	// 10+4 and 6+3: BOTH provider rows counted. Pre-fix this would be
	// EITHER (10,6) or (4,3) depending on the row_number() tiebreak hash --
	// never the sum.
	if got := rows[0].Fields["items_started"].Integer; got == nil || *got != 14 {
		t.Fatalf("items_started = %#v, want 14 (summed across both providers' rows for the team)", rows[0].Fields["items_started"])
	}
	if got := rows[0].Fields["items_completed"].Integer; got == nil || *got != 9 {
		t.Fatalf("items_completed = %#v, want 9 (summed across both providers' rows for the team)", rows[0].Fields["items_completed"])
	}
}
