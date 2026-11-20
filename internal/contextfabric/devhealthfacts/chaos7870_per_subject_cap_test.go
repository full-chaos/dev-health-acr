package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos7870Days(team string, n int) [][]any {
	rows := make([][]any, 0, n)
	for i := 0; i < n; i++ {
		day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
		rows = append(rows, readinessDailySeriesRow(team, day, 5, 1, 9))
	}
	return rows
}

func chaos7870ReadReadiness(t *testing.T, series [][]any, teams ...string) contextfabric.FactProviderResult {
	t.Helper()
	var scalars [][]any
	var subjects []contextfabric.SubjectRef
	for _, team := range teams {
		scalars = append(scalars, readinessRow(team))
		subjects = append(subjects, teamSubject(team))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: scalars},
		{match: readinessDailySeriesMatch, rows: series, honorLimit: true},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	return result
}

// One team that fills the cap must not starve the next team's daily table.
func TestChaos7870ReadinessDailyCapIsPerTeam(t *testing.T) {
	t.Parallel()
	series := append(chaos7870Days("CHECKOUT", 230), chaos7870Days("IDENTITY", 28)...)
	result := chaos7870ReadReadiness(t, series, "CHECKOUT", "IDENTITY")
	for _, fact := range result.Facts {
		table, ok := fact.Fields["daily_readiness"]
		if !ok {
			t.Errorf("%s has no daily_readiness table", fact.Subject.CanonicalID)
			continue
		}
		_, cut := fact.Fields["daily_readiness_omitted_count"]
		wantCut := fact.Subject.CanonicalID == "team:CHECKOUT"
		if cut != wantCut {
			t.Errorf("%s daily_readiness_omitted_count present = %v, want %v", fact.Subject.CanonicalID, cut, wantCut)
		}
		if !wantCut && len(table.Table.Rows) != 28 {
			t.Errorf("%s daily_readiness rows = %d, want 28", fact.Subject.CanonicalID, len(table.Table.Rows))
		}
		wantState := contextfabric.SourceAvailable
		if wantCut {
			wantState = contextfabric.SourceTruncated
		}
		if fact.SourceState != wantState {
			t.Errorf("%s source state = %q, want %q", fact.Subject.CanonicalID, fact.SourceState, wantState)
		}
	}
	if !result.Truncated || !result.TruncationSubjectLocal {
		t.Errorf("Truncated=%v TruncationSubjectLocal=%v, want both true: only one team's display cap cut a table", result.Truncated, result.TruncationSubjectLocal)
	}
}

// One team whose daily table is cut: coverage names the cut table, so the
// scalars read whole.
func TestChaos7870ReadinessSingleTeamCutIsSubjectLocal(t *testing.T) {
	t.Parallel()
	result := chaos7870ReadReadiness(t, chaos7870Days("CHECKOUT", 230), "CHECKOUT")
	if !result.Truncated || !result.TruncationSubjectLocal {
		t.Fatalf("Truncated=%v TruncationSubjectLocal=%v, want both true", result.Truncated, result.TruncationSubjectLocal)
	}
}

func TestChaos7870FlowDailyCapIsPerTeam(t *testing.T) {
	t.Parallel()
	var series [][]any
	for i := 0; i < 230; i++ {
		day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
		series = append(series, flowDailySeriesRow("team-a", day, 3, 2, 1))
	}
	for i := 0; i < 28; i++ {
		day := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
		series = append(series, flowDailySeriesRow("team-b", day, 3, 2, 1))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{
			workItemMetricsDailyRow("team-a", "scope-a", 10, 6, 4),
			workItemMetricsDailyRow("team-b", "scope-b", 10, 6, 4),
		}},
		{match: flowDailySeriesMatch, rows: series, honorLimit: true},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-a"), teamSubject("team-b")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(result.Facts))
	}
	for _, fact := range result.Facts {
		if _, ok := fact.Fields["daily_flow"]; !ok {
			t.Errorf("%s has no daily_flow table", fact.Subject.CanonicalID)
		}
		_, cut := fact.Fields["daily_flow_omitted_count"]
		wantCut := fact.Subject.CanonicalID == "team:team-a"
		if cut != wantCut {
			t.Errorf("%s daily_flow_omitted_count present = %v, want %v", fact.Subject.CanonicalID, cut, wantCut)
		}
		wantState := contextfabric.SourceAvailable
		if wantCut {
			wantState = contextfabric.SourceTruncated
		}
		if fact.SourceState != wantState {
			t.Errorf("%s source state = %q, want %q", fact.Subject.CanonicalID, fact.SourceState, wantState)
		}
	}
	if !result.Truncated || !result.TruncationSubjectLocal || result.OmittedCount != 0 {
		t.Errorf("Truncated=%v TruncationSubjectLocal=%v OmittedCount=%d, want true true 0", result.Truncated, result.TruncationSubjectLocal, result.OmittedCount)
	}
}

// Every team daily-series statement caps each team on its own.
func TestChaos7870TeamDailySeriesStatementsCapPerTeam(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind  contextfabric.FactKind
		match string
	}{
		{contextfabric.FactFlow, flowDailySeriesMatch},
		{contextfabric.FactReadiness, readinessDailySeriesMatch},
		{contextfabric.FactWorkload, "GROUP BY team_id, toDate(computed_at)"},
		{contextfabric.FactHealth, healthDailySeriesMatch},
	} {
		client := &fakeClient{}
		provider := findProvider(t, devhealthfacts.NewProviders(client), tc.kind)
		_, _ = provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: tc.kind, Subjects: []contextfabric.SubjectRef{teamSubject("a"), teamSubject("b")},
		})
		found := false
		for _, q := range client.queries {
			if strings.Contains(q.statement, tc.match) {
				found = true
				if !strings.Contains(q.statement, "\nLIMIT 200 BY ") || !strings.HasSuffix(q.statement, "\nLIMIT 400") {
					t.Errorf("%s daily series is not capped per team: %q", tc.kind, q.statement[len(q.statement)-60:])
				}
			}
		}
		if !found {
			t.Errorf("%s daily series statement not issued", tc.kind)
		}
	}
}

func chaos7870DayString(i int) string {
	return time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format("2006-01-02")
}

func chaos7870ExpectCut(t *testing.T, result contextfabric.FactProviderResult, cutField, cutSubject string, otherSubjects ...string) {
	t.Helper()
	if !result.Truncated || !result.TruncationSubjectLocal {
		t.Errorf("Truncated=%v TruncationSubjectLocal=%v, want both true", result.Truncated, result.TruncationSubjectLocal)
	}
	for _, fact := range result.Facts {
		_, cut := fact.Fields[cutField]
		want := fact.Subject.CanonicalID == cutSubject
		if cut != want {
			t.Errorf("%s %s present = %v, want %v", fact.Subject.CanonicalID, cutField, cut, want)
		}
		wantState := contextfabric.SourceAvailable
		if want {
			wantState = contextfabric.SourceTruncated
		}
		if fact.SourceState != wantState {
			t.Errorf("%s source state = %q, want %q", fact.Subject.CanonicalID, fact.SourceState, wantState)
		}
	}
}

func TestChaos7870WorkloadDisplayCutIsSubjectLocal(t *testing.T) {
	t.Parallel()
	var series [][]any
	for i := 0; i < 74; i++ {
		series = append(series, workloadDailySeriesRow("a", chaos7870DayString(i), 5, 1.5, 0.5))
	}
	for i := 0; i < 28; i++ {
		series = append(series, workloadDailySeriesRow("b", chaos7870DayString(i), 5, 1.5, 0.5))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("a", "s1"), workloadRow("b", "s1")}},
		{match: workloadDailySeriesMatch, rows: series, honorLimit: true},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("a"), teamSubject("b")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	chaos7870ExpectCut(t, result, "daily_workload_omitted_count", "team:a")
}

// A team that reads exactly the per-team cap is not a cut by itself, and the
// other team is read whole.
func TestChaos7870HealthDailyAtTheCapDoesNotTruncateTheOtherTeam(t *testing.T) {
	t.Parallel()
	var series [][]any
	for i := 0; i < 200; i++ {
		series = append(series, healthTeamDailySeriesRow("A", chaos7870DayString(i), "high", uint8(1), 0.61))
	}
	for i := 0; i < 28; i++ {
		series = append(series, healthTeamDailySeriesRow("B", chaos7870DayString(i), "high", uint8(1), 0.61))
	}
	client := &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("A"), healthRow("B")}},
		{match: healthDailySeriesMatch, rows: series, honorLimit: true},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("A"), teamSubject("B")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	chaos7870ExpectCut(t, result, "daily_health_omitted_count", "team:A")
}

// A scope breakdown cut is not a daily display cut: the kind stays truncated
// for every subject.
func TestChaos7870FlowScopeBreakdownCutStaysKindWide(t *testing.T) {
	t.Parallel()
	var scopes [][]any
	for i := 0; i < contextfabric.MaxFactValueRows+1; i++ {
		scopes = append(scopes, workItemMetricsDailyRow("team-a", "scope-"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('A'+i/26)), 10, 6, 4))
	}
	scopes = append(scopes, workItemMetricsDailyRow("team-b", "scope-b", 10, 6, 4))
	client := &fakeClient{tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: scopes},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("team-a", "2026-02-21", 1, 1, 1), flowDailySeriesRow("team-b", "2026-02-21", 1, 1, 1)}, honorLimit: true},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{teamSubject("team-a"), teamSubject("team-b")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Truncated || result.TruncationSubjectLocal {
		t.Errorf("Truncated=%v TruncationSubjectLocal=%v, want true false: a scope breakdown cut is kind-wide", result.Truncated, result.TruncationSubjectLocal)
	}
}
