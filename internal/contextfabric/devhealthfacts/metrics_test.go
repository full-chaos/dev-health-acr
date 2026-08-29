package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func metricsRow(repoID string) []any {
	return []any{repoID, "2026-02-21", int64(42), int64(7), float64(12.5), float64(0.1), uint8(1), float64(3.5), int64(4), float64(0.2)}
}

// projectSubject mints a CHAOS-3898 "project.v2:<provider>:<project_id>"
// subject via identity.Derive, mirroring ci_test.go's ciRunSubject.
func projectSubject(provider, projectID string) contextfabric.SubjectRef {
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("projectSubject(%q, %q): identity.Derive failed: omitted=%v err=%v", provider, projectID, omitted, err))
	}
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID, Label: projectID}
}

func teamMetricsRow(teamID string) []any {
	return []any{teamID, "2026-02-21", int64(10), int64(2), int64(1), float64(0.2), float64(0.1)}
}

// projectRollupRow shapes one row of the team_project_ownership join
// output: (project_key, team_id, team_name, day, commits_count,
// after_hours_commits_count, weekend_commits_count,
// after_hours_commit_ratio, weekend_commit_ratio).
func projectRollupRow(provider, projectID, teamID, teamName string, commits, afterHoursCommits, weekendCommits int64, afterHoursRatio, weekendRatio float64) []any {
	return []any{provider + ":" + projectID, teamID, teamName, "2026-02-21", commits, afterHoursCommits, weekendCommits, afterHoursRatio, weekendRatio}
}

// TestMetricsProviderTeamHappyPath is CHAOS-4347's team widening: a
// genuinely team-scoped read of team_metrics_daily, not a repository proxy.
func TestMetricsProviderTeamHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("team-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["commits_count"].Integer == nil || *fact.Fields["commits_count"].Integer != 10 {
		t.Fatalf("commits_count = %#v", fact.Fields["commits_count"])
	}
	if fact.Fields["after_hours_commit_ratio"].Number == nil || *fact.Fields["after_hours_commit_ratio"].Number != 0.2 {
		t.Fatalf("after_hours_commit_ratio = %#v", fact.Fields["after_hours_commit_ratio"])
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestMetricsProviderProjectRollupSumsCountsKeepsPerTeamRates is the
// contract CHAOS-4347's ticket named explicitly: counts sum across owning
// teams, rates are NEVER averaged -- each source row's own rate survives
// in the renderable team_breakdown table instead.
func TestMetricsProviderProjectRollupSumsCountsKeepsPerTeamRates(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: [][]any{
		projectRollupRow("linear", "proj-1", "team-1", "Team One", 10, 4, 2, 0.4, 0.2),
		projectRollupRow("linear", "proj-1", "team-2", "Team Two", 5, 0, 0, 0.0, 0.0),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["commits_count"].Integer == nil || *fact.Fields["commits_count"].Integer != 15 {
		t.Fatalf("commits_count = %#v, want summed 15", fact.Fields["commits_count"])
	}
	if fact.Fields["after_hours_commits_count"].Integer == nil || *fact.Fields["after_hours_commits_count"].Integer != 4 {
		t.Fatalf("after_hours_commits_count = %#v, want summed 4", fact.Fields["after_hours_commits_count"])
	}
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 2 {
		t.Fatalf("team_count = %#v, want 2", fact.Fields["team_count"])
	}
	if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "team_project_ownership_sum" {
		t.Fatalf("rollup_basis = %#v", fact.Fields["rollup_basis"])
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2", rows)
	}
	// Nothing here may equal an averaged rate ((0.4+0.0)/2 = 0.2): each
	// row must carry its OWN team's ratio, unmodified.
	if got := rows[0].Fields["after_hours_commit_ratio"].Number; got == nil || *got != 0.4 {
		t.Fatalf("row[0].after_hours_commit_ratio = %#v, want team-1's own 0.4, not an average", got)
	}
	if got := rows[1].Fields["after_hours_commit_ratio"].Number; got == nil || *got != 0.0 {
		t.Fatalf("row[1].after_hours_commit_ratio = %#v, want team-2's own 0.0, not an average", got)
	}
	if len(fact.EvidenceRefIDs) != 3 {
		t.Fatalf("evidence_ref_ids = %#v, want project + 2 teams", fact.EvidenceRefIDs)
	}
}

// TestMetricsProviderProjectRollupDedupesTeamOwnedByTwoSources pins the
// team_project_ownership shape CHAOS-4347's package doc comment names: the
// same team can legitimately own a project through more than one `source`
// row at once, and must be counted exactly once.
func TestMetricsProviderProjectRollupDedupesTeamOwnedByTwoSources(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: [][]any{
		projectRollupRow("linear", "proj-1", "team-1", "Team One", 10, 4, 2, 0.4, 0.2),
		projectRollupRow("linear", "proj-1", "team-1", "Team One", 10, 4, 2, 0.4, 0.2),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if *fact.Fields["commits_count"].Integer != 10 {
		t.Fatalf("commits_count = %v, want 10 (deduped, not 20)", *fact.Fields["commits_count"].Integer)
	}
	if *fact.Fields["team_count"].Integer != 1 {
		t.Fatalf("team_count = %v, want 1", *fact.Fields["team_count"].Integer)
	}
}

// TestMetricsProviderProjectRollupNoOwningTeamsHasNoFactEntry mirrors
// TestMetricsProviderZeroRowSubjectHasNoFactEntry for the project path.
func TestMetricsProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

// TestMetricsProviderAllThreeSubjectKindsInOneQuery proves the three
// branches compose: one ReadFacts call naming a repository, a team, and a
// project subject gets a fact for each, from three separate queries.
func TestMetricsProviderAllThreeSubjectKindsInOneQuery(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		// team_project_ownership's own statement ALSO contains "FROM
		// team_metrics_daily" as its inner join, so the more specific
		// match must be checked first -- fakeClient.Query returns the
		// first table whose match is a substring of the statement.
		{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}},
		{match: "FROM team_project_ownership", rows: [][]any{
			projectRollupRow("linear", "proj-1", "team-1", "Team One", 10, 4, 2, 0.4, 0.2),
		}},
		{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("team-1")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics,
		Subjects: []contextfabric.SubjectRef{
			repoSubject("repo-1"), teamSubject("team-1"), projectSubject("linear", "proj-1"),
		},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 3 {
		t.Fatalf("facts = %#v, want 3 (one per subject kind)", result.Facts)
	}
	byKind := map[contextfabric.SubjectKind]bool{}
	for _, fact := range result.Facts {
		byKind[fact.Subject.Kind] = true
	}
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam, contextfabric.SubjectProject} {
		if !byKind[kind] {
			t.Fatalf("facts = %#v, missing a fact for subject kind %q", result.Facts, kind)
		}
	}
}

// TestMetricsProviderHappyPath is CHAOS-4418's own pin: a repository
// metrics fact now carries a real per-day series (Rows), not a flat
// scalar snapshot of the latest day -- see readRepositoryMetrics' own doc
// comment for why this can no longer go through readers.ReadRepositoryMetrics
// (that shared reader collapses to exactly one row per repository).
func TestMetricsProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["day_count"].Integer == nil || *fact.Fields["day_count"].Integer != 1 {
		t.Fatalf("day_count = %#v", fact.Fields["day_count"])
	}
	dailyMetrics := fact.Fields["daily_metrics"].Rows
	if len(dailyMetrics) != 1 {
		t.Fatalf("daily_metrics = %#v, want 1 row", dailyMetrics)
	}
	row := dailyMetrics[0].Fields
	if row["day"].String == nil || *row["day"].String != "2026-02-21" {
		t.Fatalf("row day = %#v", row["day"])
	}
	if row["commits_count"].Integer == nil || *row["commits_count"].Integer != 42 {
		t.Fatalf("row fields = %#v", row)
	}
	if row["mttr_hours"].Number == nil || *row["mttr_hours"].Number != 3.5 {
		t.Fatalf("row fields = %#v", row)
	}
	// canonicalFieldRows (model_runtime.go) fails closed on more than one
	// Rows-shaped field per fact -- this fact must carry exactly one.
	rowsFieldCount := 0
	for _, value := range fact.Fields {
		if len(value.Rows) > 0 {
			rowsFieldCount++
		}
	}
	if rowsFieldCount != 1 {
		t.Fatalf("rows-shaped field count = %d, want exactly 1 (fields = %#v)", rowsFieldCount, fact.Fields)
	}
}

func TestMetricsProviderNoMTTROmitsField(t *testing.T) {
	t.Parallel()
	row := metricsRow("repo-1")
	row[6] = uint8(0)
	row[7] = float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	dailyMetrics := result.Facts[0].Fields["daily_metrics"].Rows
	if len(dailyMetrics) != 1 {
		t.Fatalf("daily_metrics = %#v, want 1 row", dailyMetrics)
	}
	if _, ok := dailyMetrics[0].Fields["mttr_hours"]; ok {
		t.Fatalf("row fields = %#v, want mttr_hours omitted", dailyMetrics[0].Fields)
	}
}

// TestMetricsProviderMultipleDaysBuildOneSeries pins the actual CHAOS-4418
// fix directly: multiple repo_metrics_daily rows for the SAME repository
// (different days) must land as multiple rows inside ONE CanonicalFact's
// daily_metrics series, not as multiple separate CanonicalFacts (the
// parent-commit shape, which silently discarded every day but the first
// one lookupCanonicalFact happened to find -- CHAOS-4355's own
// lookupCanonicalFact takes the FIRST (kind, subject) match, so a second
// same-subject fact was always unreachable dead data before this fix).
func TestMetricsProviderMultipleDaysBuildOneSeries(t *testing.T) {
	t.Parallel()
	day1 := metricsRow("repo-1")
	day2 := metricsRow("repo-1")
	day2[1] = "2026-02-22"
	day2[2] = int64(7)
	// fakeClient replays rows verbatim in the order given (it is not a
	// real SQL engine) -- day2 (the LATER day) first, day1 second,
	// mirroring the real query's own `ORDER BY repo_id, day DESC`.
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{day2, day1}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1 CanonicalFact for repo-1 (not one per day)", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["day_count"].Integer == nil || *fact.Fields["day_count"].Integer != 2 {
		t.Fatalf("day_count = %#v, want 2", fact.Fields["day_count"])
	}
	dailyMetrics := fact.Fields["daily_metrics"].Rows
	if len(dailyMetrics) != 2 {
		t.Fatalf("daily_metrics = %#v, want 2 rows", dailyMetrics)
	}
	if dailyMetrics[0].Fields["day"].String == nil || *dailyMetrics[0].Fields["day"].String != "2026-02-22" {
		t.Fatalf("dailyMetrics[0].day = %#v, want the MOST RECENT day first (day DESC) -- truncation on an oversized series must drop the oldest days, not the freshest", dailyMetrics[0].Fields["day"])
	}
	if dailyMetrics[1].Fields["commits_count"].Integer == nil || *dailyMetrics[1].Fields["commits_count"].Integer != 42 {
		t.Fatalf("dailyMetrics[1].commits_count = %#v, want 42 (day1, the older/second row)", dailyMetrics[1].Fields["commits_count"])
	}
}

// windowBinding returns the named ClickHouse binding's time.Time value from
// the last captured query, failing the test if it is missing or the wrong
// type.
func windowBinding(t *testing.T, client *fakeClient, name string) time.Time {
	t.Helper()
	last := client.queries[len(client.queries)-1]
	for _, binding := range last.bindings {
		if binding.Name == name {
			value, ok := binding.Value.(time.Time)
			if !ok {
				t.Fatalf("binding %q = %#v, want time.Time", name, binding.Value)
			}
			return value
		}
	}
	t.Fatalf("no %q binding in query %q", name, last.statement)
	return time.Time{}
}

// TestMetricsProviderSeriesUsesExplicitEvidenceWindow is CHAOS-4418's
// team-lead-ruled correction: the per-day series must span the CALLER's
// own requested evidence window when one is given explicitly (Start AND
// End), never a devhealthfacts-invented number.
func TestMetricsProviderSeriesUsesExplicitEvidenceWindow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{
			Axis:           contextfabric.TemporalCurrent,
			EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end},
		},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := windowBinding(t, client, "series_window_start"); !got.Equal(start) {
		t.Fatalf("series_window_start = %v, want the caller's own window Start %v, not a re-derived one", got, start)
	}
	if got := windowBinding(t, client, "series_window_end"); !got.Equal(end) {
		t.Fatalf("series_window_end = %v, want the caller's own window End %v", got, end)
	}
}

// TestMetricsProviderSeriesFallsBackToPlatformDefaultWindow pins the "no
// window at all" case: no historical bound, no explicit current-axis
// Start/End (a RelativeID alone does not count -- resolving one to
// absolute bounds is exclusively window.go's job). The series must still
// use the platform's own default evidence-window width (90 days,
// window.go's windowDefaultPolicy = WindowDefaultPolicy90D), not an
// independently-chosen number.
func TestMetricsProviderSeriesFallsBackToPlatformDefaultWindow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	before := time.Now().UTC()
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	end := windowBinding(t, client, "series_window_end")
	if end.Before(before) || end.After(after) {
		t.Fatalf("series_window_end = %v, want between %v and %v (now)", end, before, after)
	}
	start := windowBinding(t, client, "series_window_start")
	width := end.Sub(start)
	if width != 90*24*time.Hour {
		t.Fatalf("series window width = %v, want exactly 90 days (the platform's own default evidence-window policy width, window.go's windowDefaultPolicy)", width)
	}
}

func TestMetricsProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestMetricsProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

// TestMetricsProviderScopedToOrgAndRequestedSubjects is the guard-sensitive
// org-scope AND subject-scope test (AC-3780-2/AC-3780-5): it checks both the
// captured bindings and the statement text, so it fails if either guard is
// removed, not just if the wrong value is bound.
func TestMetricsProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestMetricsProviderRepositorySeriesCapsPerRepositoryNotJustQueryWide is
// Codex R1's own finding (confirmed): before this cap, requesting metrics
// for MULTIPLE repositories at once let a handful of repositories with wide
// day-ranges exhaust the shared, query-wide maxFactRowsPerQuery (200)
// budget entirely, leaving whichever repositories sort later (repo_id,
// day DESC) with NO canonical fact at all -- not a truncated series, a
// MISSING one, exactly the gap this ticket exists to close, reintroduced
// for a multi-repository caller. The fakeClient cannot simulate
// ClickHouse's actual per-group LIMIT BY semantics (it replays canned rows
// verbatim, not a real query planner), so this pins the STATEMENT TEXT
// carries the per-repository sub-cap, mirroring this file's own
// TestMetricsProviderTruncatesWhenRowCountReachesLimit precedent for the
// shared cap.
func TestMetricsProviderRepositorySeriesCapsPerRepositoryNotJustQueryWide(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1"), repoSubject("repo-2")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	statement := strings.ToUpper(client.queries[len(client.queries)-1].statement)
	if !strings.Contains(statement, "LIMIT 100 BY REPO_ID") {
		t.Fatalf("statement = %q, want a `LIMIT 100 BY repo_id` per-repository sub-cap ahead of the shared query-wide LIMIT", client.queries[len(client.queries)-1].statement)
	}
}

// TestMetricsProviderRowForUnrequestedRepositoryNeverAppears is the F5
// result-content guard: even though the fake client can return a row for
// ANY repository (it doesn't execute the SQL's own org/id filters), the
// provider itself must never surface a fact for a subject the caller did
// not ask about.
func TestMetricsProviderRowForUnrequestedRepositoryNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-other-org")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	for _, fact := range result.Facts {
		if fact.Subject.CanonicalID == "repository:repo-other-org" {
			t.Fatalf("facts = %#v, want no fact for the unrequested repository", result.Facts)
		}
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty", result.Facts)
	}
}

const maxMetricsRowsPerQueryForTest = 200

func metricsRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = metricsRow("repo-" + strconv.Itoa(i))
	}
	return rows
}

func TestMetricsProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: metricsRows(maxMetricsRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
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

func TestMetricsProviderNotTruncatedBelowLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repo_metrics_daily", rows: metricsRows(maxMetricsRowsPerQueryForTest - 1)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactMetrics)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactMetrics, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if result.Truncated {
		t.Fatalf("result.Truncated = true, want false when the row count is below the limit")
	}
}
