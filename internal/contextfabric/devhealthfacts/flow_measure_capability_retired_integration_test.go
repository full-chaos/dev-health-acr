package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// seedFlowCapabilityItems inserts one capability row with an explicit item_count
// (0 is the retirement marker).
func seedFlowCapabilityItems(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, provider, measure string, tracked uint8, itemCount uint32, windowEnd string, computedAt time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO work_item_measure_capability (org_id, provider, measure, tracked, evidence_count, item_count, window_start, window_end, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, provider, measure, tracked, uint32(tracked), itemCount, "2026-05-01", windowEnd, computedAt); err != nil {
		t.Fatalf("seed work_item_measure_capability(%s %s %s): %v", provider, measure, windowEnd, err)
	}
}

// A newer item_count = 0 row retires a key: it reads unknown, never
// not_tracked_by_source and never a value. The newest row decides, so a key
// whose newest row has item_count >= 1 is served as that row says.
func TestFlowProviderRetiredCapabilityReadsUnknownAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID = "org-flow-capability-retired"
	const team = "team-flow-capability-retired"
	at := ts(2026, 8, 20, 6, 0, 0)
	const points, bug = "story_points_completed", "bug_completed_ratio"

	// 08-10 linear: points tracked then retired (reads unknown); bug not
	// tracked then retired (reads unknown, not not_tracked).
	// 08-11 linear: control, newest row item_count 3 -> points tracked value, bug not tracked.
	// 08-12 linear: marker first, then a re-appearing row (item_count 2) -> newest decides: points tracked.
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "linear", "scope-l", team, 5, 3, 0.25, 4, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-11", "linear", "scope-l", team, 5, 3, 0.25, 4, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-12", "linear", "scope-l", team, 5, 3, 0.25, 4, at)

	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", points, 1, 3, "2026-08-10", at)
	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", points, 0, 0, "2026-08-10", at.Add(time.Hour))
	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", bug, 0, 3, "2026-08-10", at)
	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", bug, 0, 0, "2026-08-10", at.Add(time.Hour))

	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", points, 1, 3, "2026-08-11", at)
	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", bug, 0, 3, "2026-08-11", at)

	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", points, 0, 0, "2026-08-12", at)
	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", points, 1, 2, "2026-08-12", at.Add(time.Hour))
	seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", bug, 0, 0, "2026-08-12", at)

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject(team)},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	daily := result.Facts[0].Fields["daily_flow"]
	d10 := flowRowByDay(t, daily, "2026-08-10").Fields
	wantFlowMeasure(t, "08-10 retired", d10, points, nil, "capability_unknown")
	wantFlowMeasure(t, "08-10 retired", d10, bug, nil, "capability_unknown")
	d11 := flowRowByDay(t, daily, "2026-08-11").Fields
	wantFlowMeasure(t, "08-11 control", d11, points, f64(4), "")
	wantFlowMeasure(t, "08-11 control", d11, bug, nil, "not_tracked_by_source")
	d12 := flowRowByDay(t, daily, "2026-08-12").Fields
	wantFlowMeasure(t, "08-12 reappeared", d12, points, f64(4), "")
	wantFlowMeasure(t, "08-12 marker only", d12, bug, nil, "capability_unknown")

	// Latest day per scope is 08-12.
	scope := result.Facts[0].Fields["scope_breakdown"].Rows[0].Fields
	wantFlowMeasure(t, "scope", scope, points, f64(4), "")
	wantFlowMeasure(t, "scope", scope, bug, nil, "capability_unknown")
}

func TestFlowProviderProjectRollupRetiredCapabilityReadsUnknownAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID = "org-flow-capability-retired-project"
	at := ts(2026, 8, 20, 6, 0, 0)
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-retired", orgID, "linear", "RETIRED", "Project", uint8(1), "active", "", at); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "linear", "proj-retired", "team-a", 5, 3, 0.5, 4, at)
	for _, m := range []string{"story_points_completed", "bug_completed_ratio"} {
		seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", m, 1, 3, "2026-08-10", at)
		seedFlowCapabilityItems(t, ctx, direct, orgID, "linear", m, 0, 0, "2026-08-10", at.Add(time.Hour))
	}
	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-retired")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fields := result.Facts[0].Fields
	day := flowRowByDay(t, fields["daily_flow"], "2026-08-10").Fields
	wantFlowMeasure(t, "project day", day, "story_points_completed", nil, "capability_unknown")
	wantFlowMeasure(t, "project day", day, "bug_completed_ratio", nil, "capability_unknown")
	for _, row := range fields["team_breakdown"].Rows {
		wantFlowMeasure(t, "team-a", row.Fields, "story_points_completed", nil, "capability_unknown")
		wantFlowMeasure(t, "team-a", row.Fields, "bug_completed_ratio", nil, "capability_unknown")
	}
}
