package devhealthfacts_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func workloadRow(teamID, workScopeID string) []any {
	return []any{teamID, workScopeID, float64(3.2), float64(0.8), uint8(1), int64(14), uint8(0), uint8(1), int64(120), "2026-07-27 04:00:00"}
}

// workloadBaseQueryMatch distinguishes the pre-existing latest-per-scope
// capacity_forecasts reads (readers.ReadTeamWorkload / ReadProjectWorkload)
// from the CHAOS-4645 daily-series queries below (workloadDailySeriesMatch)
// -- both read "FROM capacity_forecasts", so matching on that alone would
// let a test's canned base-query rows also answer the new daily-series
// query with the WRONG column shape, panicking fakeScanner.Scan on a type
// assertion (the exact bug class flow.go's own flowDailySeriesMatch guards
// against). The base queries' row_number() partitions on exactly
// "team_id, work_scope_id" (no day component); the daily-series queries
// partition on "team_id, work_scope_id, toDate(computed_at)", so neither
// substring is contained in the other.
const workloadBaseQueryMatch = "work_scope_id ORDER BY computed_at DESC, forecast_id DESC"

// workloadDailySeriesMatch matches ONLY queryTeamWorkloadDailySeries /
// queryProjectWorkloadDailySeries -- see workloadBaseQueryMatch's doc
// comment.
const workloadDailySeriesMatch = "work_scope_id, toDate(computed_at) ORDER BY computed_at DESC"

// workloadDailySeriesRow shapes one CHAOS-4645 queryTeamWorkloadDailySeries /
// queryProjectWorkloadDailySeries output row: (team_id or "provider:id",
// day, backlog_size, throughput_mean, throughput_stddev).
func workloadDailySeriesRow(key, day string, backlogSize int64, throughputMean, throughputStddev float64) []any {
	return []any{key, day, backlogSize, throughputMean, throughputStddev}
}

func TestWorkloadProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["throughput_mean"].Number == nil || *fact.Fields["throughput_mean"].Number != 3.2 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["high_variance"].Boolean == nil || !*fact.Fields["high_variance"].Boolean {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["forecast_p50_days"].Integer == nil || *fact.Fields["forecast_p50_days"].Integer != 14 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["work_scope_id"].String == nil || *fact.Fields["work_scope_id"].String != "scope-a" {
		t.Fatalf("fields = %#v, want work_scope_id=scope-a named in the payload (F3)", fact.Fields)
	}
	// Semantic-honesty guard: a workload fact must state, in its own
	// structure, that it is a capacity forecast, never a current-load
	// reading (team-lead review requirement).
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "capacity_forecast" {
		t.Fatalf("fields = %#v, want basis=capacity_forecast", fact.Fields)
	}
}

// TestWorkloadProviderMultipleWorkScopesProduceMultipleFacts is the F5
// regression test for Codex finding F3: a team forecast under several
// distinct work_scope_id values must produce one fact PER scope, never a
// silent collapse into a single, arbitrarily-chosen scope.
func TestWorkloadProviderMultipleWorkScopesProduceMultipleFacts(t *testing.T) {
	t.Parallel()
	rowB := workloadRow("CHAOS", "scope-b")
	rowB[2] = float64(0.01) // a very different throughput_mean, proving it's a distinct row, not a repeat
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a"), rowB}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 -- one per work_scope_id, neither discarded", result.Facts)
	}
	scopes := map[string]bool{}
	for _, fact := range result.Facts {
		if fact.Fields["work_scope_id"].String != nil {
			scopes[*fact.Fields["work_scope_id"].String] = true
		}
	}
	if !scopes["scope-a"] || !scopes["scope-b"] {
		t.Fatalf("scopes seen = %#v, want both scope-a and scope-b", scopes)
	}
}

func TestWorkloadProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("ghost-team")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestWorkloadProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// TestWorkloadProviderNoPersonLevelFields is the "no person-level workload
// output" guard (§19.6.3). capacity_forecasts has no per-person column at
// all, so this proves that structurally: every field name on a workload
// fact is one of the known team-level aggregate fields, never anything
// person-shaped.
func TestWorkloadProviderNoPersonLevelFields(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	allowed := map[string]bool{
		"basis": true, "work_scope_id": true, "throughput_mean": true, "throughput_stddev": true, "insufficient_history": true,
		"high_variance": true, "backlog_size": true, "computed_at": true, "forecast_p50_days": true,
		"daily_workload": true, "daily_workload_omitted_count": true,
	}
	for _, fact := range result.Facts {
		for field := range fact.Fields {
			if !allowed[field] {
				t.Fatalf("unexpected field %q on a workload fact -- possible person-level leak", field)
			}
		}
	}
}

func TestWorkloadProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "CHAOS" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestWorkloadProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestWorkloadProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("other-team", "scope-a")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested team", result.Facts)
	}
}

// workloadProjectRollupRow shapes one row of the project rollup join output:
// (project_key, team_id, team_name, work_scope_id, throughput_mean,
// throughput_stddev, hasP50, p50_days, insufficient_history, high_variance,
// backlog_size, computed_at).
func workloadProjectRollupRow(provider, projectID, teamID, teamName, workScopeID string, throughputMean, throughputStddev float64, backlogSize int64, highVariance uint8) []any {
	// has_team = 1: an attributed row. CHAOS-4521b added the flag so an
	// UNATTRIBUTED row (source team_id NULL) stays distinguishable.
	return []any{provider + ":" + projectID, uint8(1), teamID, teamName, workScopeID, throughputMean, throughputStddev, uint8(0), int64(0), uint8(0), highVariance, backlogSize, "2026-07-27 04:00:00"}
}

// TestWorkloadProviderProjectRollupBreaksDownByTeamNeverAverages pins
// CHAOS-4363's contract: Monte Carlo forecast stats are never summed or
// averaged across owning teams -- each team's own latest per-scope forecast
// survives verbatim in the renderable team_breakdown table.
func TestWorkloadProviderProjectRollupBreaksDownByTeamNeverAverages(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: [][]any{
		workloadProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, 120, 1),
		workloadProjectRollupRow("linear", "proj-1", "team-2", "Team Two", "scope-b", 9.0, 2.1, 40, 0),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 2 {
		t.Fatalf("team_count = %#v, want 2", fact.Fields["team_count"])
	}
	if _, hasTop := fact.Fields["throughput_mean"]; hasTop {
		t.Fatalf("fields = %#v, want no project-level throughput_mean -- forecasts are not additive", fact.Fields)
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2", rows)
	}
	if got := rows[0].Fields["throughput_mean"].Number; got == nil || *got != 3.2 {
		t.Fatalf("row[0].throughput_mean = %#v, want team-1's own 3.2", got)
	}
	if got := rows[1].Fields["backlog_size"].Integer; got == nil || *got != 40 {
		t.Fatalf("row[1].backlog_size = %#v, want team-2's own 40, not summed", got)
	}
}

func TestWorkloadProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

const maxWorkloadRowsPerQueryForTest = 200

func workloadRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = workloadRow("team-"+strconv.Itoa(i), "scope-"+strconv.Itoa(i))
	}
	return rows
}

func TestWorkloadProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: workloadRows(maxWorkloadRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true when the row count reaches the limit")
	}
	if len(client.queries) == 0 || !strings.Contains(strings.ToUpper(client.queries[len(client.queries)-1].statement), "LIMIT") {
		t.Fatalf("query statement = %#v, want a LIMIT clause", client.queries)
	}
}

// TestWorkloadProviderTeamReadsDailyWorkloadSeries is CHAOS-4645's core team
// shape (design doc §5.2): a genuine time_series alongside the existing
// scalar fields, additive -- basis/throughput_mean/work_scope_id etc. must
// stay exactly as before this ticket.
func TestWorkloadProviderTeamReadsDailyWorkloadSeries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a")}},
		{match: workloadDailySeriesMatch, rows: [][]any{
			workloadDailySeriesRow("CHAOS", "2026-07-27", 120, 3.2, 0.8),
			workloadDailySeriesRow("CHAOS", "2026-07-26", 100, 4.0, 1.0),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	// Additive: the pre-existing scalars are untouched.
	if fact.Fields["throughput_mean"].Number == nil || *fact.Fields["throughput_mean"].Number != 3.2 {
		t.Fatalf("throughput_mean = %#v, want unchanged at 3.2", fact.Fields["throughput_mean"])
	}
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "capacity_forecast" {
		t.Fatalf("basis = %#v, want unchanged", fact.Fields["basis"])
	}
	table := fact.Fields["daily_workload"].Table
	if table == nil {
		t.Fatal("daily_workload field is missing")
	}
	if table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_workload.Shape = %q, want time_series", table.Shape)
	}
	if len(table.Key) != 1 || table.Key[0] != "day" {
		t.Fatalf("daily_workload.Key = %#v, want [day]", table.Key)
	}
	wantMeasures := []string{"backlog_size", "throughput_mean", "throughput_stddev"}
	if !reflect.DeepEqual(table.Measures, wantMeasures) {
		t.Fatalf("daily_workload.Measures = %#v, want %#v", table.Measures, wantMeasures)
	}
	if err := fact.Fields["daily_workload"].Validate(); err != nil {
		t.Fatalf("daily_workload fails FactValue.Validate(): %v", err)
	}
	rows := fact.Fields["daily_workload"].Rows
	if len(rows) != 2 {
		t.Fatalf("daily_workload rows = %d, want 2", len(rows))
	}
	if got := rows[0].Fields["day"].String; got == nil || *got != "2026-07-27" {
		t.Fatalf("rows[0].day = %#v, want 2026-07-27", got)
	}
	if got := rows[0].Fields["backlog_size"].Integer; got == nil || *got != 120 {
		t.Fatalf("rows[0].backlog_size = %#v, want 120", got)
	}
	if got := rows[0].Fields["throughput_stddev"].Number; got == nil || *got != 0.8 {
		t.Fatalf("rows[0].throughput_stddev = %#v, want 0.8", got)
	}
	// p50_days/insufficient_history/high_variance have no valid combination
	// rule across concurrent scopes and must never appear on a daily row.
	for _, forbidden := range []string{"forecast_p50_days", "insufficient_history", "high_variance", "work_scope_id"} {
		if _, present := rows[0].Fields[forbidden]; present {
			t.Fatalf("daily_workload row carries forbidden field %q -- no valid cross-scope combination rule", forbidden)
		}
	}
}

// TestWorkloadProviderProjectReadsDailyWorkloadSeries mirrors the team case
// for the project rollup (CHAOS-4645, design doc §5.2), additive alongside
// the existing team_breakdown.
func TestWorkloadProviderProjectReadsDailyWorkloadSeries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadBaseQueryMatch, rows: [][]any{
			workloadProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, 120, 1),
		}},
		{match: workloadDailySeriesMatch, rows: [][]any{
			workloadDailySeriesRow("linear:proj-1", "2026-07-27", 160, 12.2, 1.28),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	// Additive: team_count/team_breakdown are untouched.
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 1 {
		t.Fatalf("team_count = %#v, want unchanged at 1", fact.Fields["team_count"])
	}
	if len(fact.Fields["team_breakdown"].Rows) != 1 {
		t.Fatalf("team_breakdown = %#v, want unchanged at 1 row", fact.Fields["team_breakdown"].Rows)
	}
	table := fact.Fields["daily_workload"].Table
	if table == nil {
		t.Fatal("daily_workload field is missing")
	}
	if table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_workload.Shape = %q, want time_series", table.Shape)
	}
	if err := fact.Fields["daily_workload"].Validate(); err != nil {
		t.Fatalf("daily_workload fails FactValue.Validate(): %v", err)
	}
	if len(fact.Fields["daily_workload"].Rows) != 1 {
		t.Fatalf("daily_workload rows = %d, want 1", len(fact.Fields["daily_workload"].Rows))
	}
	// CHAOS-4681: before this ticket, a project's top-level Fields carried
	// no scalar matching any of daily_workload's declared Measures --
	// genkitruntime.modelFacingFacts drops daily_workload itself before
	// synthesis, so a project-subject workload trend could never be claimed
	// at all. The freshest day is now copied in under its own field names.
	if fact.Fields["backlog_size"].Integer == nil || *fact.Fields["backlog_size"].Integer != 160 {
		t.Fatalf("backlog_size = %#v, want a scalar sibling matching the declared measure (160)", fact.Fields["backlog_size"])
	}
	if fact.Fields["throughput_mean"].Number == nil || *fact.Fields["throughput_mean"].Number != 12.2 {
		t.Fatalf("throughput_mean = %#v, want a scalar sibling matching the declared measure (12.2)", fact.Fields["throughput_mean"])
	}
}

// TestWorkloadProviderProjectRollupBasisIsFactLevelScalar is the CHAOS-4645
// fix for the Fable-F3 debt CHAOS-4633 deliberately deferred: "basis" is
// constant "capacity_forecast" across every team_breakdown row, so it moves
// to a sibling scalar on the CanonicalFact (alongside rollup_basis/
// team_count) instead of being repeated on every row. This is a DELIBERATE
// behavior change from the pre-CHAOS-4645 shape, where "basis" was a row
// column and part of team_breakdown's declared Key.
func TestWorkloadProviderProjectRollupBasisIsFactLevelScalar(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: workloadBaseQueryMatch, rows: [][]any{
		workloadProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, 120, 1),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "capacity_forecast" {
		t.Fatalf("fact-level basis = %#v, want capacity_forecast (Fable-F3 fix)", fact.Fields["basis"])
	}
	table := fact.Fields["team_breakdown"].Table
	if table == nil {
		t.Fatal("team_breakdown field is missing")
	}
	for _, key := range table.Key {
		if key == "basis" {
			t.Fatalf("team_breakdown.Key = %#v, want no \"basis\" entry -- it is now a fact-level scalar, never a row identity column", table.Key)
		}
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 1 {
		t.Fatalf("team_breakdown rows = %#v, want 1", rows)
	}
	if _, present := rows[0].Fields["basis"]; present {
		t.Fatalf("team_breakdown row still carries \"basis\" -- want it moved to the fact-level scalar only")
	}
	if err := fact.Fields["team_breakdown"].Validate(); err != nil {
		t.Fatalf("team_breakdown fails FactValue.Validate(): %v", err)
	}
	// CHAOS-4680: insufficient_history/high_variance are BooleanFactValue
	// flags, not quantities -- a Measures column is now producer-validated
	// numeric-only (FactTable.Validate), so a regression that puts them
	// back in Measures fails Validate() above, since the cells are
	// booleans. This assertion pins WHERE they live.
	for _, measure := range table.Measures {
		if measure == "insufficient_history" || measure == "high_variance" {
			t.Fatalf("team_breakdown.Measures = %v, must not classify a boolean flag as a measure", table.Measures)
		}
	}
	wantObservations := map[string]bool{"insufficient_history": false, "high_variance": false}
	for _, observation := range table.Observations {
		if _, want := wantObservations[observation]; want {
			wantObservations[observation] = true
		}
	}
	for name, found := range wantObservations {
		if !found {
			t.Fatalf("team_breakdown.Observations = %v, want %q declared as an observation", table.Observations, name)
		}
	}
}
