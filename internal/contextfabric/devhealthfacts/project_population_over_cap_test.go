package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// overCapSizes are the project counts the over-cap cases run at: one past the
// breakdown scan budget, and the most the answer projection carries.
var overCapSizes = []int{201, 250}

func overCapProjectID(i int) string { return fmt.Sprintf("p%03d", i) }

func overCapSubjects(n int) []contextfabric.SubjectRef {
	subjects := make([]contextfabric.SubjectRef, n)
	for i := range subjects {
		subjects[i] = projectSubject("linear", overCapProjectID(i))
	}
	return subjects
}

func readOverCap(t *testing.T, client *fakeClient, kind contextfabric.FactKind, subjects []contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts(%s) error = %v", kind, err)
	}
	return result
}

func overCapFact(t *testing.T, result contextfabric.FactProviderResult, projectID string) contextfabric.CanonicalFact {
	t.Helper()
	want := projectSubject("linear", projectID).CanonicalID
	for _, fact := range result.Facts {
		if fact.Subject.CanonicalID == want {
			return fact
		}
	}
	t.Fatalf("no fact for the over-cap project %s among %d facts", projectID, len(result.Facts))
	return contextfabric.CanonicalFact{}
}

// assertOverCapProject pins what a project whose rows sit past the shared scan
// budget must serve: a fact of its own, the uncapped team count, no breakdown
// table (it holds no row), and the omitted count that says why.
func assertOverCapProject(t *testing.T, result contextfabric.FactProviderResult, n int) {
	t.Helper()
	if len(result.Facts) != n {
		t.Fatalf("facts = %d, want %d: the projects past the scan budget must still be served", len(result.Facts), n)
	}
	if !result.Truncated {
		t.Fatalf("Truncated = false, want true: the scan reached its row cap")
	}
	late := overCapFact(t, result, overCapProjectID(n-1))
	if _, has := late.Fields["team_breakdown"]; has {
		t.Fatalf("over-cap project carries a team_breakdown table although none of its rows was read")
	}
	if got := late.Fields["team_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_count = %#v, want 1 from the uncapped aggregate", late.Fields["team_count"])
	}
	if got := late.Fields["team_breakdown_omitted_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_breakdown_omitted_count = %#v, want 1: the list was cut and must say so", late.Fields["team_breakdown_omitted_count"])
	}
	first := overCapFact(t, result, overCapProjectID(0))
	if _, has := first.Fields["team_breakdown"]; !has {
		t.Fatalf("a project the scan reached lost its team_breakdown")
	}
	if _, has := first.Fields["team_breakdown_omitted_count"]; has {
		t.Fatalf("a project the scan fully reached reports an omitted count")
	}
}

func TestFlowProviderProjectPastScanBudgetStillServed(t *testing.T) {
	t.Parallel()
	for _, n := range overCapSizes {
		t.Run(fmt.Sprintf("projects=%d", n), func(t *testing.T) {
			population := make([][]any, n)
			scanned := make([][]any, 0, maxOverCapScanRows)
			for i := 0; i < n; i++ {
				id := overCapProjectID(i)
				population[i] = []any{"linear:" + id, int64(1), int64(5), int64(3)}
				if i < maxOverCapScanRows {
					scanned = append(scanned, projectWorkItemMetricsRow("linear", id, "team-"+id, "scope", 5, 3))
				}
			}
			client := &fakeClient{tables: []fakeTable{
				{match: projectPopulationMarker, rows: population, honorLimit: true},
				{match: "work_scope_id ORDER BY day DESC", rows: scanned},
			}}
			result := readOverCap(t, client, contextfabric.FactFlow, overCapSubjects(n))
			assertOverCapProject(t, result, n)
			late := overCapFact(t, result, overCapProjectID(n-1))
			if got := late.Fields["items_started_latest_day"].Integer; got == nil || *got != 5 {
				t.Fatalf("items_started_latest_day = %#v, want 5 from the aggregate", late.Fields["items_started_latest_day"])
			}
		})
	}
}

const maxOverCapScanRows = 200

func TestReadinessProviderProjectPastScanBudgetStillServed(t *testing.T) {
	t.Parallel()
	for _, n := range overCapSizes {
		t.Run(fmt.Sprintf("projects=%d", n), func(t *testing.T) {
			population := make([][]any, n)
			scanned := make([][]any, 0, maxOverCapScanRows)
			for i := 0; i < n; i++ {
				id := overCapProjectID(i)
				population[i] = []any{"linear:" + id, int64(1), int64(1)}
				if i < maxOverCapScanRows {
					scanned = append(scanned, readinessProjectRollupRow("linear", id, "team-"+id, "Team", "scope", "linear", 4, 1, 5, 0.8))
				}
			}
			client := &fakeClient{tables: []fakeTable{
				{match: projectPopulationMarker, rows: population, honorLimit: true},
				{match: readinessOriginalQueryMatch, rows: scanned},
			}}
			assertOverCapProject(t, readOverCap(t, client, contextfabric.FactReadiness, overCapSubjects(n)), n)
		})
	}
}

func TestLandscapeProviderProjectPastScanBudgetStillServed(t *testing.T) {
	t.Parallel()
	for _, n := range overCapSizes {
		t.Run(fmt.Sprintf("projects=%d", n), func(t *testing.T) {
			population := make([][]any, n)
			scanned := make([][]any, 0, maxOverCapScanRows)
			for i := 0; i < n; i++ {
				id := overCapProjectID(i)
				population[i] = []any{"linear:" + id, int64(1)}
				if i < maxOverCapScanRows {
					scanned = append(scanned, landscapeProjectRow("linear", id, "team-"+id, 4, 1200, 30, 18.5, 6))
				}
			}
			client := &fakeClient{tables: []fakeTable{
				{match: projectPopulationMarker, rows: population, honorLimit: true},
				{match: "sumIf(il.one_churn", rows: scanned},
			}}
			assertOverCapProject(t, readOverCap(t, client, contextfabric.FactLandscape, overCapSubjects(n)), n)
		})
	}
}

// The workload and health producers already take their project population
// from an uncapped aggregate; these two cases execute the over-cap read so
// that stays true.
func TestWorkloadProviderProjectPastScanBudgetStillServed(t *testing.T) {
	t.Parallel()
	for _, n := range overCapSizes {
		t.Run(fmt.Sprintf("projects=%d", n), func(t *testing.T) {
			aggregate := make([][]any, n)
			scanned := make([][]any, 0, maxOverCapScanRows)
			for i := 0; i < n; i++ {
				id := overCapProjectID(i)
				aggregate[i] = workloadP50MaxRow("linear", id, 0, 0, 1, 0, "", "", false, false)
				if i < maxOverCapScanRows {
					scanned = append(scanned, workloadProjectRollupRow("linear", id, "team-"+id, "Team", "scope", 3.2, 0.8, 120, 0))
				}
			}
			client := &fakeClient{tables: []fakeTable{
				{match: workloadP50MaxMatch, rows: aggregate, honorLimit: true},
				{match: workloadBaseQueryMatch, rows: scanned},
			}}
			result := readOverCap(t, client, contextfabric.FactWorkload, overCapSubjects(n))
			if len(result.Facts) != n || !result.Truncated {
				t.Fatalf("facts = %d truncated = %v, want %d and truncated", len(result.Facts), result.Truncated, n)
			}
			late := overCapFact(t, result, overCapProjectID(n-1))
			if got := late.Fields["team_breakdown_rows_shown"].Integer; got == nil || *got != 0 {
				t.Fatalf("team_breakdown_rows_shown = %#v, want 0", late.Fields["team_breakdown_rows_shown"])
			}
			if got := late.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 1 {
				t.Fatalf("team_breakdown_rows_total = %#v, want 1 from the aggregate", late.Fields["team_breakdown_rows_total"])
			}
		})
	}
}

func TestHealthProviderProjectPastScanBudgetStillServed(t *testing.T) {
	t.Parallel()
	for _, n := range overCapSizes {
		t.Run(fmt.Sprintf("projects=%d", n), func(t *testing.T) {
			aggregate := make([][]any, n)
			scanned := make([][]any, 0, maxOverCapScanRows)
			for i := 0; i < n; i++ {
				id := overCapProjectID(i)
				aggregate[i] = healthSeverityMaxRow("linear", id, 1, 1, "team", "team-"+id, "low")
				if i < maxOverCapScanRows {
					scanned = append(scanned, healthProjectRollupRow("linear", id, "team", "team-"+id, "Team", "low", 0.05))
				}
			}
			client := &fakeClient{tables: []fakeTable{
				{match: healthProjectRollupMatch, rows: scanned},
				{match: healthSeverityMaxMatch, rows: aggregate, honorLimit: true},
			}}
			result := readOverCap(t, client, contextfabric.FactHealth, overCapSubjects(n))
			if len(result.Facts) != n || !result.Truncated {
				t.Fatalf("facts = %d truncated = %v, want %d and truncated", len(result.Facts), result.Truncated, n)
			}
			late := overCapFact(t, result, overCapProjectID(n-1))
			if got := late.Fields["risk_breakdown_rows_shown"].Integer; got == nil || *got != 0 {
				t.Fatalf("risk_breakdown_rows_shown = %#v, want 0", late.Fields["risk_breakdown_rows_shown"])
			}
			if got := late.Fields["risk_breakdown_rows_total"].Integer; got == nil || *got != 1 {
				t.Fatalf("risk_breakdown_rows_total = %#v, want 1 from the aggregate", late.Fields["risk_breakdown_rows_total"])
			}
		})
	}
}

// The investment mix is project-grain (one row per project), so the cap cuts
// the project list itself. A cohort of the projection maximum is served in
// full; one past it is cut, and the project the cap cut off is named cut, never
// a project with no mix.
func TestInvestmentProviderProjectsUpToTheProjectionMaximumAreServed(t *testing.T) {
	t.Parallel()
	for _, n := range []int{201, 250} {
		t.Run(fmt.Sprintf("projects=%d", n), func(t *testing.T) {
			t.Parallel()
			groups := make([][]nativePhasedUnit, n)
			for i := range groups {
				groups[i] = nativeMixUnitsFor(overCapProjectID(i), 1, 1, 0, 0, 1, 0, 0)
			}
			client := &fakeClient{tables: nativePhasedTables(groups...)}
			result := readOverCap(t, client, contextfabric.FactInvestment, overCapSubjects(n))
			if len(result.Facts) != n {
				t.Fatalf("facts = %d, want %d: every project of a cohort within the projection maximum is served", len(result.Facts), n)
			}
			for key, reason := range result.SubjectReasons {
				if strings.Contains(reason, "investment_project_mix_cut_by_row_cap") || strings.Contains(reason, "investment_project_no_canonical_mix") {
					t.Fatalf("subject %s carries %q although every project was served", key, reason)
				}
			}
		})
	}
}

func TestInvestmentProviderProjectPastRowCapIsCutNotNoMix(t *testing.T) {
	t.Parallel()
	n := 251
	groups := make([][]nativePhasedUnit, n)
	for i := range groups {
		groups[i] = nativeMixUnitsFor(overCapProjectID(i), 1, 1, 0, 0, 1, 0, 0)
	}
	client := &fakeClient{tables: nativePhasedTables(groups...)}
	subjects := overCapSubjects(n)
	result := readOverCap(t, client, contextfabric.FactInvestment, subjects)
	if len(result.Facts) != n-1 {
		t.Fatalf("facts = %d, want %d: the served list is cut at the projection maximum", len(result.Facts), n-1)
	}
	if !result.Truncated {
		t.Fatalf("Truncated = false, want true")
	}
	reason := result.SubjectReasons[contextfabric.FactSubjectKey(subjects[n-1])]
	if !strings.Contains(reason, "investment_project_mix_cut_by_row_cap") {
		t.Fatalf("reason of the cut project = %q, want the row cap reason", reason)
	}
	for key, reason := range result.SubjectReasons {
		if strings.Contains(reason, "investment_project_no_canonical_mix") {
			t.Fatalf("subject %s was named as having no mix: %q", key, reason)
		}
	}
}
