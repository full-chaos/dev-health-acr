package devhealthfacts_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
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
	if fact.Fields["commits_count"].Integer == nil || *fact.Fields["commits_count"].Integer != 42 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["mttr_hours"].Number == nil || *fact.Fields["mttr_hours"].Number != 3.5 {
		t.Fatalf("fields = %#v", fact.Fields)
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
	if _, ok := result.Facts[0].Fields["mttr_hours"]; ok {
		t.Fatalf("fields = %#v, want mttr_hours omitted", result.Facts[0].Fields)
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
