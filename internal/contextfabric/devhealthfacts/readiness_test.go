package devhealthfacts_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func readinessRow(teamID string) []any {
	return []any{teamID, "scope-1", "linear", "2026-02-22", int64(18), int64(2), int64(20), uint8(1), float64(0.9)}
}

func TestReadinessProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: [][]any{readinessRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["estimate_coverage_ratio"].Number == nil || *fact.Fields["estimate_coverage_ratio"].Number != 0.9 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["backlog_size"].Integer == nil || *fact.Fields["backlog_size"].Integer != 20 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	// Semantic-honesty guard: a readiness fact must state, in its own
	// structure, that it is backlog estimate coverage, never a general
	// release/ship-readiness verdict (team-lead review requirement).
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "estimate_coverage" {
		t.Fatalf("fields = %#v, want basis=estimate_coverage", fact.Fields)
	}
}

func TestReadinessProviderNoRatioOmitsField(t *testing.T) {
	t.Parallel()
	row := readinessRow("CHAOS")
	row[7] = uint8(0)
	row[8] = float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if _, ok := result.Facts[0].Fields["estimate_coverage_ratio"]; ok {
		t.Fatalf("fields = %#v, want estimate_coverage_ratio omitted", result.Facts[0].Fields)
	}
}

func TestReadinessProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestReadinessProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestReadinessProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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

// TestReadinessProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestReadinessProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: [][]any{readinessRow("other-team")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested team", result.Facts)
	}
}

// readinessProjectRollupRow shapes one row of the project rollup join
// output: (project_key, team_id, team_name, work_scope_id, provider, day,
// estimated_count, unestimated_count, backlog_size, hasRatio, ratio).
func readinessProjectRollupRow(provider, projectID, teamID, teamName, workScopeID, sourceProvider string, estimated, unestimated, backlogSize int64, ratio float64) []any {
	return []any{provider + ":" + projectID, teamID, teamName, workScopeID, sourceProvider, "2026-02-22", estimated, unestimated, backlogSize, uint8(1), ratio}
}

// TestReadinessProviderProjectRollupBreaksDownByTeamNeverSums pins
// CHAOS-4363's contract: estimate-coverage counts are never summed across
// owning teams tracking different work scopes -- each team's own latest
// per-scope coverage row survives verbatim in team_breakdown.
func TestReadinessProviderProjectRollupBreaksDownByTeamNeverSums(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: [][]any{
		readinessProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", "linear", 18, 2, 20, 0.9),
		readinessProjectRollupRow("linear", "proj-1", "team-2", "Team Two", "scope-b", "gitlab", 5, 15, 20, 0.25),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
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
	if _, hasTop := fact.Fields["estimated_count"]; hasTop {
		t.Fatalf("fields = %#v, want no project-level estimated_count sum", fact.Fields)
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2", rows)
	}
	if got := rows[0].Fields["estimated_count"].Integer; got == nil || *got != 18 {
		t.Fatalf("row[0].estimated_count = %#v, want team-1's own 18", got)
	}
	if got := rows[1].Fields["estimate_coverage_ratio"].Number; got == nil || *got != 0.25 {
		t.Fatalf("row[1].estimate_coverage_ratio = %#v, want team-2's own 0.25, not averaged", got)
	}
}

func TestReadinessProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

const maxReadinessRowsPerQueryForTest = 200

func readinessRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = readinessRow("CHAOS")
		rows[i][1] = "scope-" + strconv.Itoa(i)
	}
	return rows
}

func TestReadinessProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM estimate_coverage_metrics_daily", rows: readinessRows(maxReadinessRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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
