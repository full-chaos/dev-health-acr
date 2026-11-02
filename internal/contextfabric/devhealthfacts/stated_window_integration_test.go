package devhealthfacts_test

// CHAOS-12536. An answer on the current axis with a stated window reads the
// real providers over a real ClickHouse. A subject whose newest row is older
// than the window has no data in that window (check 12): a fact built from that
// row puts an old number under a window it is not in. A subject with a row
// inside the window keeps its fact.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
)

const (
	statedWindowStaleDays  = 113
	statedWindowFreshDays  = 3
	statedWindowStaleTeam  = "tm-stale"
	statedWindowFreshTeam  = "tm-fresh"
	statedWindowStaleRepo  = "repo-stale"
	statedWindowFreshRepo  = "repo-fresh"
	statedWindowStaleScope = "proj-stale"
	statedWindowFreshScope = "proj-fresh"
	// statedWindowHotspotNoRows is the published no-rows basis of the fact.
	statedWindowHotspotNoRows = "no_hotspot_rows_in_window"
)

func statedWindowDay(daysAgo int) time.Time {
	return time.Now().UTC().AddDate(0, 0, -daysAgo).Truncate(24 * time.Hour)
}

// statedWindowFixture is a real ClickHouse with one stale and one fresh subject
// of each kind, in every table of a dated read.
func statedWindowFixture(t *testing.T, ctx context.Context, org string) []contextfabric.FactProvider {
	t.Helper()
	rawQuery, direct := sharedClickHouseFixture(t)
	for _, statement := range devhealthschema.DDL("projects", "team_project_ownership", "ic_landscape_rolling_30d", "team_repo_ownership", "estimate_coverage_metrics_daily", "capacity_forecasts", "file_hotspot_daily", "file_complexity_snapshots", "cicd_metrics_daily", "deploy_metrics_daily", "team_metrics_daily", "compounding_risk_daily", "work_item_metrics_daily", "repo_metrics_daily") {
		if err := direct.Exec(ctx, statement); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	synced := statedWindowDay(200)
	for _, side := range []struct {
		days        int
		team, scope string
		repo        string
	}{
		{statedWindowStaleDays, statedWindowStaleTeam, statedWindowStaleScope, repoUUID(statedWindowStaleRepo)},
		{statedWindowFreshDays, statedWindowFreshTeam, statedWindowFreshScope, repoUUID(statedWindowFreshRepo)},
	} {
		day := statedWindowDay(side.days)
		at := day.Add(6 * time.Hour)
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			side.scope, org, "github", side.scope, side.scope, uint8(1), "active", "", synced)
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			org, "github", side.team, side.scope, side.scope, "native", synced, nil, synced)
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, side.repo, org, "acme/"+side.team, "github", synced)
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			org, "github", side.team, side.repo, "acme/"+side.team, "exact", "native", uint8(1), uint16(1), int32(1), synced, nil, synced)
		for _, mapName := range []string{"churn_throughput", "cycle_throughput", "wip_throughput"} {
			exec(`INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				org, side.repo, day, "alice", side.team, mapName, 1.5, 2.5, 0.5, 0.5, uint64(100), uint32(2), 5.0, uint32(3), at)
		}
		exec(`INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			day, "github", side.scope, side.team, uint32(8), uint32(2), uint32(10), 0.8, at, org)
		exec(`INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			repoUUID("forecast-"+side.team), at, side.team, side.scope, uint32(10), 2.0, 0.5, uint8(0), uint8(0), org)
		exec(`INSERT INTO file_hotspot_daily (org_id, repo_id, day, file_path, churn_loc_30d, churn_commits_30d, cyclomatic_total, cyclomatic_avg, blame_concentration, risk_score, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			org, side.repo, day, "src/a.go", uint64(10), uint32(2), uint32(3), 1.5, 0.3, 0.9, at)
		exec(`INSERT INTO cicd_metrics_daily (repo_id, day, pipelines_count, success_rate, avg_duration_minutes, p90_duration_minutes, avg_queue_minutes, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?)`,
			side.repo, day, uint32(40), 0.85, 10.5, 22.0, 2.0, at, org)
		exec(`INSERT INTO deploy_metrics_daily (repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours, lead_time_p50_hours, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?)`,
			side.repo, day, uint32(6), uint32(1), 1.2, 4.5, at, org)
		exec(`INSERT INTO team_metrics_daily (day, team_id, team_name, commits_count, after_hours_commits_count, weekend_commits_count, after_hours_commit_ratio, weekend_commit_ratio, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			day, side.team, side.team, uint32(7), uint32(1), uint32(1), 0.1, 0.1, at, org)
		exec(`INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, severity, compounding_risk, computed_at) VALUES (?,?,?,?,?,?,?)`,
			org, "repo", side.repo, day, "low", 0.1, at)
		exec(`INSERT INTO compounding_risk_daily (org_id, scope, scope_id, day, severity, compounding_risk, computed_at) VALUES (?,?,?,?,?,?,?)`,
			org, "team", side.team, day, "low", 0.1, at)
		exec(`INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			day, "github", side.scope, side.team, uint32(3), uint32(2), uint32(1), float64(4), float64(8), float64(0.1), float64(2), at, org)
	}
	return devhealthfacts.NewProviders(statementthreads.API(rawQuery))
}

func TestAStatedWindowBoundsEveryDatedRead(t *testing.T) {
	ctx := context.Background()
	org := sharedTestOrgID(t)
	providers := statedWindowFixture(t, ctx, org)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	start := today.AddDate(0, 0, -90)
	stated := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &today, RelativeID: contextfabric.RelativeWindowTrailing90D}}

	team := func(id string) contextfabric.SubjectRef { return teamSubject(id) }
	repo := func(label string) contextfabric.SubjectRef { return repoSubject(repoUUID(label)) }
	project := func(id string) contextfabric.SubjectRef { return projectSubject("github", id) }
	for _, read := range []struct {
		name         string
		kind         contextfabric.FactKind
		stale, fresh contextfabric.SubjectRef
	}{
		{"landscape team", contextfabric.FactLandscape, team(statedWindowStaleTeam), team(statedWindowFreshTeam)},
		{"landscape project", contextfabric.FactLandscape, project(statedWindowStaleScope), project(statedWindowFreshScope)},
		{"readiness team", contextfabric.FactReadiness, team(statedWindowStaleTeam), team(statedWindowFreshTeam)},
		{"readiness project", contextfabric.FactReadiness, project(statedWindowStaleScope), project(statedWindowFreshScope)},
		{"workload team", contextfabric.FactWorkload, team(statedWindowStaleTeam), team(statedWindowFreshTeam)},
		{"workload project", contextfabric.FactWorkload, project(statedWindowStaleScope), project(statedWindowFreshScope)},
		{"file_hotspots repository", contextfabric.FactFileHotspots, repo(statedWindowStaleRepo), repo(statedWindowFreshRepo)},
		{"ci repository", contextfabric.FactContinuousIntegration, repo(statedWindowStaleRepo), repo(statedWindowFreshRepo)},
		{"deployments repository", contextfabric.FactDeployments, repo(statedWindowStaleRepo), repo(statedWindowFreshRepo)},
		{"metrics team", contextfabric.FactMetrics, team(statedWindowStaleTeam), team(statedWindowFreshTeam)},
		{"metrics project", contextfabric.FactMetrics, project(statedWindowStaleScope), project(statedWindowFreshScope)},
		{"health repository", contextfabric.FactHealth, repo(statedWindowStaleRepo), repo(statedWindowFreshRepo)},
		{"health team", contextfabric.FactHealth, team(statedWindowStaleTeam), team(statedWindowFreshTeam)},
		{"flow team", contextfabric.FactFlow, team(statedWindowStaleTeam), team(statedWindowFreshTeam)},
		{"flow project", contextfabric.FactFlow, project(statedWindowStaleScope), project(statedWindowFreshScope)},
	} {
		t.Run(read.name, func(t *testing.T) {
			provider := findProvider(t, providers, read.kind)
			run := func(subject contextfabric.SubjectRef) contextfabric.FactProviderResult {
				t.Helper()
				result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org}, contextfabric.FactQuery{
					Time: stated, Kind: read.kind, Subjects: []contextfabric.SubjectRef{subject},
				})
				if err != nil {
					t.Fatalf("ReadFacts: %v", err)
				}
				return result
			}
			if fresh := run(read.fresh); len(fresh.Facts) == 0 {
				t.Errorf("a subject with a row inside the window lost its fact: state %s, reason %q", fresh.State, fresh.Reason)
			}
			stale := run(read.stale)
			// A repository's hotspot fact always states its basis, and with no
			// row the basis says so: that is no old number under the window.
			if read.kind == contextfabric.FactFileHotspots {
				if len(stale.Facts) != 1 || stale.Facts[0].Fields["latest_run_day"].String != nil || stale.Facts[0].Fields["hotspot_files"].Table != nil {
					t.Errorf("a %d-day-old hotspot run reads under a stated 90-day window: %+v", statedWindowStaleDays, stale.Facts)
				}
				if basis := stale.Facts[0].Fields["hotspot_basis"].String; basis == nil || *basis != statedWindowHotspotNoRows {
					t.Errorf("hotspot_basis = %v, want the no-rows basis", basis)
				}
				return
			}
			if len(stale.Facts) != 0 {
				t.Errorf("a %d-day-old row reads as a fact under a stated 90-day window: state %s, fields %v", statedWindowStaleDays, stale.State, factFieldNames(stale.Facts[0]))
			}
			if len(stale.Facts) == 0 && stale.State != contextfabric.SourceNoData {
				t.Errorf("a subject with no row in the window has state %s, want no_data: %s", stale.State, stale.Reason)
			}
			if len(stale.Facts) == 0 && !strings.Contains(stale.Reason, "no rows in the stated window") {
				t.Errorf("the reason does not say the window: %q", stale.Reason)
			}
		})
	}
}

func factFieldNames(fact contextfabric.CanonicalFact) []string {
	names := make([]string, 0, len(fact.Fields))
	for name := range fact.Fields {
		names = append(names, name)
	}
	return names
}

// A row dated today is in a window that runs up to today, whose stated end is
// 00:00 today. A past window takes the latest row inside it, not the newest row
// of the table, and no stated window and all_time read the latest row as before.
func TestAStatedWindowKeepsTodayAndReadsThePast(t *testing.T) {
	ctx := context.Background()
	org := sharedTestOrgID(t)
	providers := statedWindowFixture(t, ctx, org)
	_, direct := sharedClickHouseFixture(t)
	provider := findProvider(t, providers, contextfabric.FactLandscape)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seed := func(team string, day time.Time) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO ic_landscape_rolling_30d (org_id, repo_id, as_of_day, identity_id, team_id, map_name, x_raw, y_raw, x_norm, y_norm, churn_loc_30d, delivery_units_30d, cycle_p50_30d_hours, wip_max_30d, computed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			org, repoUUID(statedWindowFreshRepo), day, "alice", team, "churn_throughput", 1.5, 2.5, 0.5, 0.5, uint64(100), uint32(2), 5.0, uint32(3), day); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	const todayTeam, pastTeam = "tm-today", "tm-past"
	seed(todayTeam, today)
	seed(pastTeam, today.AddDate(0, 0, -40))
	seed(pastTeam, today.AddDate(0, 0, -5))
	window := func(start, end time.Time, relative contextfabric.RelativeWindowID) contextfabric.TimeContext {
		return contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end, RelativeID: relative}}
	}
	day := func(team string, at contextfabric.TimeContext) string {
		t.Helper()
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org}, contextfabric.FactQuery{Time: at, Kind: contextfabric.FactLandscape, Subjects: []contextfabric.SubjectRef{teamSubject(team)}})
		if err != nil {
			t.Fatalf("ReadFacts: %v", err)
		}
		if len(result.Facts) == 0 {
			return ""
		}
		return *result.Facts[0].Fields["area_breakdown"].Rows[0].Fields["as_of_day"].String
	}
	stamp := func(at time.Time) string { return at.Format(time.DateOnly) }
	if got := day(todayTeam, window(today.AddDate(0, 0, -90), today, contextfabric.RelativeWindowTrailing90D)); got != stamp(today) {
		t.Fatalf("a row dated today under a window up to today reads %q", got)
	}
	if got := day(pastTeam, window(today.AddDate(0, 0, -60), today.AddDate(0, 0, -30), "")); got != stamp(today.AddDate(0, 0, -40)) {
		t.Fatalf("a past window reads day %q, want the latest row inside it", got)
	}
	if got := day(pastTeam, window(today.AddDate(0, 0, -60), today.AddDate(0, 0, -40), "")); got != "" {
		t.Fatalf("a past window's end is exclusive, and day %q was read", got)
	}
	if got := day(statedWindowStaleTeam, contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}); got != stamp(statedWindowDay(statedWindowStaleDays)) {
		t.Fatalf("no stated window reads day %q, want the latest row", got)
	}
	allTime := contextfabric.RelativeWindowAllTime
	if got := day(statedWindowStaleTeam, window(today.AddDate(0, 0, -90), today, allTime)); got != stamp(statedWindowDay(statedWindowStaleDays)) {
		t.Fatalf("all_time reads day %q, want the latest row", got)
	}
}
