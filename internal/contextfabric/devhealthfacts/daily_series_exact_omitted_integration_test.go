package devhealthfacts_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// dailySeriesKind seeds one daily-series source table with days rows for one
// key, newest day 2026-08-31, and a same-day rerun of every day when reruns is
// set: a rerun is a second stored row of a day, never a second day.
type dailySeriesKind struct {
	kind  contextfabric.FactKind
	table string
	seed  func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, teamID, workScopeID string, days int, rerun bool)
}

func dailySeriesExec(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, statement string) {
	t.Helper()
	if err := direct.Exec(ctx, statement); err != nil {
		t.Fatalf("seed: %v\n%s", err, statement)
	}
}

func dailySeriesComputedAt(rerun bool) string {
	if rerun {
		return "toDateTime('2026-08-31 07:00:00')"
	}
	return "toDateTime('2026-08-31 06:00:00')"
}

var dailySeriesKinds = []dailySeriesKind{
	{contextfabric.FactFlow, "daily_flow", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, teamID, workScopeID string, days int, rerun bool) {
		for _, again := range []bool{false, rerun} {
			dailySeriesExec(t, ctx, direct, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id)
SELECT toDate('2026-08-31') - number, 'linear', '`+workScopeID+`', '`+teamID+`', toUInt32(number), toUInt32(1), toUInt32(1), 4.0, 8.0, 0.1, 2.0, `+dailySeriesComputedAt(again)+`, '`+orgID+`' FROM numbers(`+strconv.Itoa(days)+`)`)
			if !rerun {
				break
			}
		}
	}},
	{contextfabric.FactReadiness, "daily_readiness", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, teamID, workScopeID string, days int, rerun bool) {
		for _, again := range []bool{false, rerun} {
			dailySeriesExec(t, ctx, direct, `INSERT INTO estimate_coverage_metrics_daily (day, provider, work_scope_id, team_id, estimated_count, unestimated_count, backlog_size, ratio, computed_at, org_id)
SELECT toDate('2026-08-31') - number, 'linear', '`+workScopeID+`', '`+teamID+`', toUInt32(3), toUInt32(1), toUInt32(4), 0.75, `+dailySeriesComputedAt(again)+`, '`+orgID+`' FROM numbers(`+strconv.Itoa(days)+`)`)
			if !rerun {
				break
			}
		}
	}},
	{contextfabric.FactWorkload, "daily_workload", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, teamID, workScopeID string, days int, rerun bool) {
		for _, again := range []bool{false, rerun} {
			dailySeriesExec(t, ctx, direct, `INSERT INTO capacity_forecasts (forecast_id, computed_at, team_id, work_scope_id, backlog_size, throughput_mean, throughput_stddev, insufficient_history, high_variance, org_id)
SELECT concat('`+teamID+`-`+workScopeID+`-', toString(number), '-`+strconv.FormatBool(again)+`'), `+dailySeriesComputedAt(again)+` - toIntervalDay(number), '`+teamID+`', '`+workScopeID+`', toUInt32(12), 3.0, 0.5, toUInt8(0), toUInt8(0), '`+orgID+`' FROM numbers(`+strconv.Itoa(days)+`)`)
			if !rerun {
				break
			}
		}
	}},
	{contextfabric.FactHealth, "daily_health", func(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID, teamID, _ string, days int, rerun bool) {
		for _, again := range []bool{false, rerun} {
			dailySeriesExec(t, ctx, direct, `INSERT INTO compounding_risk_daily (org_id, day, scope, scope_id, compounding_risk, severity, computed_at)
SELECT '`+orgID+`', toDate('2026-08-31') - number, 'team', '`+teamID+`', 0.5, 'elevated', `+dailySeriesComputedAt(again)+` FROM numbers(`+strconv.Itoa(days)+`)`)
			if !rerun {
				break
			}
		}
	}},
}

func createDailySeriesTables(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	createCHAOS4363Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("work_item_metrics_daily", "work_item_measure_capability") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
}

func dailyOmittedCount(fact contextfabric.CanonicalFact, table string) (int64, bool) {
	value, ok := fact.Fields[table+"_omitted_count"]
	if !ok || value.Integer == nil {
		return 0, false
	}
	return *value.Integer, true
}

// A team daily table names every window day it does not show: the count is
// the team's deduped days, read in the same statement before the per-team
// cap, never the rows the cap let through. Every day carries a same-day rerun,
// so a count of stored rows instead of days is caught too.
func TestTeamDailySeriesOmittedCountIsExactAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createDailySeriesTables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	for _, kind := range dailySeriesKinds {
		for _, days := range []int{230, 201, 64} {
			t.Run(fmt.Sprintf("%s_%d_days", kind.table, days), func(t *testing.T) {
				orgID := fmt.Sprintf("org-%s-%d", kind.table, days)
				teamID := fmt.Sprintf("team-%s-%d", kind.table, days)
				kind.seed(t, ctx, direct, orgID, teamID, "scope-x", days, true)
				result, err := findProvider(t, providers, kind.kind).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
					Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
					Kind: kind.kind, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
				})
				if err != nil {
					t.Fatalf("ReadFacts() error = %v", err)
				}
				var fact *contextfabric.CanonicalFact
				for index := range result.Facts {
					if _, ok := result.Facts[index].Fields[kind.table]; ok {
						fact = &result.Facts[index]
						break
					}
				}
				if fact == nil {
					t.Fatalf("no fact carries %s: %#v", kind.table, result.Facts)
				}
				if shown := len(fact.Fields[kind.table].Rows); shown != contextfabric.MaxFactValueRows {
					t.Fatalf("%s rows = %d, want %d", kind.table, shown, contextfabric.MaxFactValueRows)
				}
				omitted, named := dailyOmittedCount(*fact, kind.table)
				want := int64(days - contextfabric.MaxFactValueRows)
				if want == 0 {
					if named {
						t.Fatalf("%s_omitted_count = %d, want absent: every day is shown", kind.table, omitted)
					}
					return
				}
				if !named || omitted != want {
					t.Fatalf("%s_omitted_count = %d (named %v), want %d: %d window days, %d shown", kind.table, omitted, named, want, days, contextfabric.MaxFactValueRows)
				}
				if fact.SourceState != contextfabric.SourceTruncated || !result.Truncated || !result.TruncationSubjectLocal {
					t.Fatalf("state=%q Truncated=%v TruncationSubjectLocal=%v, want a truncated fact and a subject-local cut", fact.SourceState, result.Truncated, result.TruncationSubjectLocal)
				}
			})
		}
	}
}

// Each requested project keeps its own newest days: projects whose days sum
// past any shared budget each show up to the display cap and name the rest on
// their own fact, and a project sorting last still gets its series.
func TestProjectDailySeriesAreCappedPerProjectAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createDailySeriesTables(t, ctx, direct)
	providers := devhealthfacts.NewProviders(query)
	projects := []struct {
		id   string
		days int
	}{{"proj-a", 150}, {"proj-b", 150}, {"proj-c", 30}}
	for _, kind := range dailySeriesKinds {
		t.Run(kind.table, func(t *testing.T) {
			orgID := "org-project-" + kind.table
			subjects := make([]contextfabric.SubjectRef, 0, len(projects))
			wantDays := map[string]int{}
			for _, project := range projects {
				projectKey := strings.ToUpper(strings.ReplaceAll(project.id, "-", ""))
				dailySeriesExec(t, ctx, direct, `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('`+project.id+`', '`+orgID+`', 'linear', '`+projectKey+`', 'Project', 1, 'active', '', toDateTime('2026-01-01 00:00:00'))`)
				teamID := "team-x"
				if kind.kind == contextfabric.FactHealth {
					// A project's health is its owning teams' risk, so each project gets its own team.
					teamID = "team-" + project.id
					dailySeriesExec(t, ctx, direct, `INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES ('`+teamID+`', '`+teamID+`', NULL, toDateTime('2026-01-01 00:00:00'), '`+orgID+`', 'linear', [], 1)`)
					dailySeriesExec(t, ctx, direct, `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES ('`+orgID+`', 'linear', '`+teamID+`', 'irrelevant', '`+projectKey+`', 'native', toDateTime('2026-01-01 00:00:00'), NULL, toDateTime('2026-01-01 00:00:00'))`)
				}
				kind.seed(t, ctx, direct, orgID, teamID, project.id, project.days, false)
				subject := projectSubject("linear", project.id)
				subjects = append(subjects, subject)
				wantDays[subject.CanonicalID] = project.days
			}
			result, err := findProvider(t, providers, kind.kind).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: kind.kind, Subjects: subjects,
			})
			if err != nil {
				t.Fatalf("ReadFacts() error = %v", err)
			}
			seen := 0
			for _, fact := range result.Facts {
				days, requested := wantDays[fact.Subject.CanonicalID]
				if !requested {
					continue
				}
				seen++
				shown := len(fact.Fields[kind.table].Rows)
				omitted, named := dailyOmittedCount(fact, kind.table)
				if want := min(days, contextfabric.MaxFactValueRows); shown != want {
					t.Errorf("%s %s rows = %d, want %d: a neighbour must not take its rows", fact.Subject.CanonicalID, kind.table, shown, want)
				}
				if want := int64(days - min(days, contextfabric.MaxFactValueRows)); omitted != want || named != (want > 0) {
					t.Errorf("%s %s_omitted_count = %d (named %v), want %d", fact.Subject.CanonicalID, kind.table, omitted, named, want)
				}
				wantState := contextfabric.SourceAvailable
				if days > contextfabric.MaxFactValueRows {
					wantState = contextfabric.SourceTruncated
				}
				if fact.SourceState != wantState {
					t.Errorf("%s state = %q, want %q", fact.Subject.CanonicalID, fact.SourceState, wantState)
				}
			}
			if seen != len(projects) {
				t.Fatalf("project facts = %d, want %d: %#v", seen, len(projects), result.Facts)
			}
			if !result.Truncated || !result.TruncationSubjectLocal || result.OmittedCount != 0 {
				t.Fatalf("Truncated=%v TruncationSubjectLocal=%v OmittedCount=%d, want a subject-local cut", result.Truncated, result.TruncationSubjectLocal, result.OmittedCount)
			}
		})
	}
}

// A daily table dropped for the joint bytes bound names every window day it
// held, not only the days the read cap let through.
func TestFlowBytesBoundDropNamesEveryWindowDayAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createDailySeriesTables(t, ctx, direct)
	const orgID, teamID, days = "org-bytes-bound-days", "team-bytes-bound-days", 230
	dailySeriesKinds[0].seed(t, ctx, direct, orgID, teamID, "scope-days", days, false)
	wide := strings.Repeat("<", 1000)
	for i := 0; i < contextfabric.MaxFactValueRows-1; i++ {
		if err := direct.Exec(ctx, `INSERT INTO work_item_metrics_daily (day, provider, work_scope_id, team_id, items_started, items_completed, wip_count_end_of_day, cycle_time_p50_hours, lead_time_p50_hours, bug_completed_ratio, story_points_completed, computed_at, org_id) VALUES (toDate('2026-08-31'), 'linear', ?, ?, 1, 1, 1, 4.0, 8.0, 0.1, 2.0, toDateTime('2026-08-31 06:00:00'), ?)`,
			fmt.Sprintf("%s-%02d", wide, i), teamID, orgID); err != nil {
			t.Fatalf("seed wide scope: %v", err)
		}
	}
	result, err := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactFlow).ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if reason := fact.Fields["daily_flow_omitted_reason"].String; reason == nil || *reason != "fact_bytes_bound_exceeded" {
		t.Fatalf("daily_flow_omitted_reason = %v, want fact_bytes_bound_exceeded: the bytes bound did not fire", reason)
	}
	if _, kept := fact.Fields["daily_flow"]; kept {
		t.Fatal("daily_flow kept, want it dropped for the bytes bound")
	}
	if omitted, named := dailyOmittedCount(fact, "daily_flow"); !named || omitted != days {
		t.Fatalf("daily_flow_omitted_count = %d (named %v), want %d: every window day", omitted, named, days)
	}
}
