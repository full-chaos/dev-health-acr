package devhealthfacts_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// investment_metrics_daily keeps one row per (org, day, repo, team, area,
// stream). Two repositories of one team and stream on one day must both count:
// the newest row of each repository is taken first, then the repositories are
// summed.
func TestInvestmentSumsTheNewestRowOfEachRepositoryAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS4363Tables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)

	const orgID = "org-investment-two-repos"
	if err := direct.Exec(ctx, `ALTER TABLE investment_metrics_daily ADD COLUMN IF NOT EXISTS repo_id Nullable(UUID)`); err != nil {
		t.Fatalf("add repo_id: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"proj-repos", orgID, "linear", "REPO1", "Project", uint8(1), "active", "", ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed projects row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
		"team-repos", "Team Repos", ts(2026, 1, 1, 0, 0, 0), orgID, "linear", uint8(1)); err != nil {
		t.Fatalf("seed teams row: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		orgID, "linear", "team-repos", "irrelevant", "REPO1", "native", ts(2026, 1, 1, 0, 0, 0), nil, ts(2026, 1, 1, 0, 0, 0)); err != nil {
		t.Fatalf("seed team_project_ownership: %v", err)
	}
	type seed struct {
		repoID, stream           string
		day                      time.Time
		computedHour             int
		units, items, prs, cycle float64
		churn                    uint64
	}
	insert := func(r seed) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO investment_metrics_daily (repo_id, day, team_id, investment_area, project_stream, delivery_units, work_items_completed, prs_merged, churn_loc, cycle_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.repoID, r.day, "team-repos", "product", r.stream, uint32(r.units), uint32(r.items), uint32(r.prs), r.churn, r.cycle, ts(2026, 8, r.day.Day(), r.computedHour, 0, 0), orgID); err != nil {
			t.Fatalf("seed investment_metrics_daily: %v", err)
		}
	}
	const repoA, repoB = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	const noRepository = "00000000-0000-0000-0000-000000000000"
	d12, d11 := date(2026, 8, 12), date(2026, 8, 11)
	// growth: two repositories, each with a stale row; every summed measure
	// adds (units 30, items 4, prs 6, churn 300).
	insert(seed{repoA, "growth", d12, 5, 99, 9, 9, 9, 9})
	insert(seed{repoA, "growth", d12, 7, 10, 3, 2, 10.0, 100})
	insert(seed{repoB, "growth", d12, 6, 88, 8, 8, 8, 8})
	insert(seed{repoB, "growth", d12, 8, 20, 1, 4, 40.0, 200})
	// solo: one repository -> an exact median and a weighted mean equal to it.
	insert(seed{repoA, "solo", d12, 5, 5, 2, 1, 12.5, 10})
	// no-repository: work items with no repository carry the nil UUID and count.
	insert(seed{noRepository, "no-repository", d12, 5, 50, 1, 1, 1, 1})
	insert(seed{noRepository, "no-repository", d12, 9, 7, 1, 1, 6.0, 1})
	// zeroed: the newest row is zero, so the older non-zero row is never served.
	insert(seed{repoA, "zeroed", d12, 4, 40, 4, 4, 4, 4})
	insert(seed{repoA, "zeroed", d12, 9, 0, 0, 0, 0, 0})
	// idle: two repositories complete no work item, so no cycle value is known.
	insert(seed{repoA, "idle", d12, 9, 3, 0, 1, 77.0, 1})
	insert(seed{repoB, "idle", d12, 9, 4, 0, 1, 88.0, 1})
	// latest-day: the repository with no row on the newest day does not add its
	// older day.
	insert(seed{repoA, "latest-day", d12, 9, 4, 1, 1, 5.0, 1})
	insert(seed{repoB, "latest-day", d11, 9, 100, 1, 1, 5.0, 1})

	result, err := findProvider(t, providers, contextfabric.FactInvestment).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-repos")},
	})
	if err != nil || len(result.Facts) != 1 {
		t.Fatalf("ReadFacts = %v, %v; want one fact", result.Facts, err)
	}
	type wantRow struct {
		units, items, prs, churn int64
		cycle, weighted          *float64
	}
	f := func(v float64) *float64 { return &v }
	want := map[string]wantRow{
		"growth":        {30, 4, 6, 300, nil, f(17.5)},
		"solo":          {5, 2, 1, 10, f(12.5), f(12.5)},
		"no-repository": {7, 1, 1, 1, f(6.0), f(6.0)},
		"zeroed":        {0, 0, 0, 0, f(0), nil},
		"idle":          {7, 0, 2, 2, nil, nil},
		"latest-day":    {4, 1, 1, 1, f(5.0), f(5.0)},
	}
	rows := map[string]contextfabric.FactValueRow{}
	for _, row := range result.Facts[0].Fields["team_breakdown"].Rows {
		stream := row.Fields["project_stream"].String
		if stream == nil {
			t.Fatalf("row without project_stream: %v", row.Fields)
		}
		rows[*stream] = row
	}
	intOf := func(row contextfabric.FactValueRow, name string) int64 {
		if v := row.Fields[name].Integer; v != nil {
			return *v
		}
		return -1
	}
	numOf := func(row contextfabric.FactValueRow, name string) *float64 {
		cell, ok := row.Fields[name]
		if !ok || cell.Null {
			return nil
		}
		return cell.Number
	}
	for stream, w := range want {
		row, ok := rows[stream]
		if !ok {
			t.Errorf("stream %q: missing; got %v", stream, rows)
			continue
		}
		for name, wantValue := range map[string]int64{"delivery_units": w.units, "work_items_completed": w.items, "prs_merged": w.prs, "churn_loc": w.churn} {
			if got := intOf(row, name); got != wantValue {
				t.Errorf("stream %q: %s = %d, want %d", stream, name, got, wantValue)
			}
		}
		for name, wantValue := range map[string]*float64{"cycle_p50_hours": w.cycle, "cycle_p50_hours_weighted_mean": w.weighted} {
			got := numOf(row, name)
			switch {
			case wantValue == nil && got != nil:
				t.Errorf("stream %q: %s = %v, want absent", stream, name, *got)
			case wantValue != nil && (got == nil || math.Abs(*got-*wantValue) > 1e-9):
				t.Errorf("stream %q: %s = %v, want %v", stream, name, got, *wantValue)
			}
		}
	}
}
