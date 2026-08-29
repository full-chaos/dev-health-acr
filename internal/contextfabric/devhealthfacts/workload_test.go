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

func workloadRow(teamID, workScopeID string) []any {
	return []any{teamID, workScopeID, float64(3.2), float64(0.8), uint8(1), int64(14), uint8(0), uint8(1), int64(120), "2026-07-27 04:00:00"}
}

func TestWorkloadProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{workloadRow("CHAOS", "scope-a")}}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{workloadRow("CHAOS", "scope-a"), rowB}}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: nil}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", err: errors.New("boom")}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{workloadRow("CHAOS", "scope-a")}}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: nil}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{workloadRow("other-team", "scope-a")}}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: [][]any{
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: nil}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM capacity_forecasts", rows: workloadRows(maxWorkloadRowsPerQueryForTest)}}}
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
