package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// seedFlowMetric inserts one work_item_metrics_daily row.
func seedFlowMetric(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, day, provider, scope, team string, started, completed uint32, bug, points float64, computedAt time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		day, provider, scope, team, started, completed, uint32(1), float64(4), float64(8), bug, points, computedAt, orgID); err != nil {
		t.Fatalf("seed work_item_metrics_daily(%s %s): %v", day, provider, err)
	}
}

// seedFlowCapability inserts one work_item_measure_capability answer for the
// window ending windowEnd.
func seedFlowCapability(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID, provider, measure string, tracked uint8, windowEnd string, computedAt time.Time) {
	t.Helper()
	if err := direct.Exec(ctx, `INSERT INTO work_item_measure_capability (org_id, provider, measure, tracked, evidence_count, item_count, window_start, window_end, computed_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, provider, measure, tracked, uint32(tracked), uint32(3), "2026-05-01", windowEnd, computedAt); err != nil {
		t.Fatalf("seed work_item_measure_capability(%s %s %s): %v", provider, measure, windowEnd, err)
	}
}

func flowRowByDay(t *testing.T, value contextfabric.FactValue, day string) contextfabric.FactValueRow {
	t.Helper()
	for _, row := range value.Rows {
		if d := row.Fields["day"].String; d != nil && *d == day {
			return row
		}
	}
	t.Fatalf("no row for day %s in %#v", day, value.Rows)
	return contextfabric.FactValueRow{}
}

func wantFlowMeasure(t *testing.T, label string, fields map[string]contextfabric.FactValue, measure string, want *float64, wantReason string) {
	t.Helper()
	got, present := fields[measure]
	reason, hasReason := fields[measure+"_unavailable_reason"]
	if want != nil {
		if !present || got.Number == nil || *got.Number != *want {
			t.Errorf("%s %s = %#v, want %v", label, measure, got, *want)
		}
		if hasReason {
			t.Errorf("%s %s carries reason %#v next to a value", label, measure, reason)
		}
		return
	}
	if present {
		t.Errorf("%s %s = %#v, want it omitted (%s)", label, measure, got, wantReason)
	}
	if !hasReason || reason.String == nil || *reason.String != wantReason {
		t.Errorf("%s %s_unavailable_reason = %#v, want %q", label, measure, reason, wantReason)
	}
}

func f64(v float64) *float64 { return &v }

// A measure the source does not track is omitted with its reason; a tracked
// measure that is truly zero stays 0; a day with no capability answer is
// unknown (never an earlier window's answer); a re-run answer wins by
// computed_at; and a mixed day aggregates over the tracked providers only.
func TestFlowProviderServesNotTrackedApartFromZeroAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID = "org-flow-capability"
	const team = "team-flow-capability"
	at := ts(2026, 8, 20, 6, 0, 0)

	// 08-10: linear tracks both (points 4, bug 0.25); github tracks neither
	// (stored 0). 08-11: github only, its points answer was re-run
	// (tracked 1 then 0), bug has no answer. 08-12: linear only, no answer
	// for this window_end (the 08-10 answer must not carry over).
	// 08-13: linear tracks points and stores a true 0.
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "linear", "scope-l", team, 5, 3, 0.25, 4, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "github", "scope-g", team, 2, 1, 0, 0, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-11", "github", "scope-g", team, 1, 1, 0, 0, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-12", "linear", "scope-l", team, 1, 0, 0, 0, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-13", "linear", "scope-l", team, 1, 1, 0, 0, at)

	for _, m := range []string{"story_points_completed", "bug_completed_ratio"} {
		seedFlowCapability(t, ctx, direct, orgID, "linear", m, 1, "2026-08-10", at)
		seedFlowCapability(t, ctx, direct, orgID, "github", m, 0, "2026-08-10", at)
	}
	seedFlowCapability(t, ctx, direct, orgID, "github", "story_points_completed", 1, "2026-08-11", at)
	seedFlowCapability(t, ctx, direct, orgID, "github", "story_points_completed", 0, "2026-08-11", at.Add(time.Hour))
	seedFlowCapability(t, ctx, direct, orgID, "linear", "story_points_completed", 1, "2026-08-13", at)
	seedFlowCapability(t, ctx, direct, orgID, "linear", "bug_completed_ratio", 0, "2026-08-13", at)

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
	fields := result.Facts[0].Fields
	for _, name := range []string{"daily_flow", "scope_breakdown"} {
		if err := fields[name].Validate(); err != nil {
			t.Fatalf("%s fails Validate(): %v", name, err)
		}
	}
	daily := fields["daily_flow"]
	const points, bug = "story_points_completed", "bug_completed_ratio"
	d10 := flowRowByDay(t, daily, "2026-08-10").Fields
	wantFlowMeasure(t, "08-10", d10, points, f64(4), "")
	wantFlowMeasure(t, "08-10", d10, bug, f64(0.25), "")
	d11 := flowRowByDay(t, daily, "2026-08-11").Fields
	wantFlowMeasure(t, "08-11", d11, points, nil, "not_tracked_by_source")
	wantFlowMeasure(t, "08-11", d11, bug, nil, "capability_unknown")
	d12 := flowRowByDay(t, daily, "2026-08-12").Fields
	wantFlowMeasure(t, "08-12", d12, points, nil, "capability_unknown")
	wantFlowMeasure(t, "08-12", d12, bug, nil, "capability_unknown")
	d13 := flowRowByDay(t, daily, "2026-08-13").Fields
	wantFlowMeasure(t, "08-13", d13, points, f64(0), "")
	wantFlowMeasure(t, "08-13", d13, bug, nil, "not_tracked_by_source")

	byProvider := map[string]map[string]contextfabric.FactValue{}
	for _, row := range fields["scope_breakdown"].Rows {
		byProvider[*row.Fields["provider"].String] = row.Fields
	}
	// Latest day per scope: linear scope-l = 08-13, github scope-g = 08-11.
	wantFlowMeasure(t, "scope linear", byProvider["linear"], points, f64(0), "")
	wantFlowMeasure(t, "scope linear", byProvider["linear"], bug, nil, "not_tracked_by_source")
	wantFlowMeasure(t, "scope github", byProvider["github"], points, nil, "not_tracked_by_source")
	wantFlowMeasure(t, "scope github", byProvider["github"], bug, nil, "capability_unknown")
}

func TestFlowProviderProjectRollupServesNotTrackedApartFromZeroAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	providers := devhealthfacts.NewProviders(query)
	const orgID = "org-flow-capability-project"
	at := ts(2026, 8, 20, 6, 0, 0)
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-capability", orgID, "linear", "CAPABILITY", "Project", uint8(1), "active", "", at); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	// One project, three source rows on one day: two teams under linear (one
	// tracks points = 4, one stores 0) and a github team that tracks nothing.
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "linear", "proj-capability", "team-a", 5, 3, 0.5, 4, at)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "github", "proj-capability", "team-b", 2, 1, 0, 0, at)
	for _, m := range []string{"story_points_completed", "bug_completed_ratio"} {
		seedFlowCapability(t, ctx, direct, orgID, "linear", m, 1, "2026-08-10", at)
		seedFlowCapability(t, ctx, direct, orgID, "github", m, 0, "2026-08-10", at)
	}

	provider := findProvider(t, providers, contextfabric.FactFlow)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-capability")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fields := result.Facts[0].Fields
	for _, name := range []string{"daily_flow", "team_breakdown"} {
		if err := fields[name].Validate(); err != nil {
			t.Fatalf("%s fails Validate(): %v", name, err)
		}
	}
	day := flowRowByDay(t, fields["daily_flow"], "2026-08-10").Fields
	wantFlowMeasure(t, "project day", day, "story_points_completed", f64(4), "")
	wantFlowMeasure(t, "project day", day, "bug_completed_ratio", f64(0.5), "")
	for _, row := range fields["team_breakdown"].Rows {
		switch *row.Fields["team_id"].String {
		case "team-a":
			wantFlowMeasure(t, "team-a", row.Fields, "story_points_completed", f64(4), "")
			wantFlowMeasure(t, "team-a", row.Fields, "bug_completed_ratio", f64(0.5), "")
		case "team-b":
			wantFlowMeasure(t, "team-b", row.Fields, "story_points_completed", nil, "not_tracked_by_source")
			wantFlowMeasure(t, "team-b", row.Fields, "bug_completed_ratio", nil, "not_tracked_by_source")
		}
	}
}

// The same property at the MCP tool boundary: what a read_facts client
// receives names a not-tracked measure instead of serving 0.
func TestFlowNotTrackedAtTheReadFactsBoundaryAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	const orgID = "org-flow-capability-boundary"
	const team = "team-boundary"
	at := ts(2026, 8, 20, 6, 0, 0)
	seedFlowMetric(t, ctx, direct, orgID, "2026-08-10", "github", "scope-g", team, 2, 1, 0, 0, at)
	for _, m := range []string{"story_points_completed", "bug_completed_ratio"} {
		seedFlowCapability(t, ctx, direct, orgID, "github", m, 0, "2026-08-10", at)
	}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(seam), nil)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000007505"),
		storage.Principal{OrgID: orgID, Subject: "t7505", CredentialID: "t7505"}, directread.FactsRequest{
			Kinds:    []string{"flow"},
			Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: teamSubject(team).CanonicalID}},
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	if len(response.Facts) != 1 {
		raw, _ := json.Marshal(response)
		t.Fatalf("facts = %s", raw)
	}
	for _, name := range []string{"daily_flow", "scope_breakdown"} {
		table, ok := response.Facts[0].Tables[name]
		if !ok || len(table.Rows) == 0 {
			t.Fatalf("%s table missing: %+v", name, response.Facts[0].Tables)
		}
		col := map[string]int{}
		for i, c := range table.Columns {
			col[c] = i
		}
		row := table.Rows[0]
		for _, measure := range []string{"story_points_completed", "bug_completed_ratio"} {
			if i, has := col[measure]; has && row[i] != nil {
				t.Errorf("%s %s = %v, want omitted for a not-tracked measure", name, measure, row[i])
			}
			i, has := col[measure+"_unavailable_reason"]
			if !has || row[i] != "not_tracked_by_source" {
				t.Errorf("%s %s_unavailable_reason = %v (columns %v), want not_tracked_by_source", name, measure, row, table.Columns)
			}
		}
	}
}
