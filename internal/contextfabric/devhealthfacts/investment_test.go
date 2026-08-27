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

func investmentRow(teamID string) []any {
	// churn_loc is uint64, matching the production column -- the reader
	// scans it raw and range-checks rather than wrapping it in SQL.
	return []any{teamID, "product", "growth", "2026-02-22", int64(30), int64(12), int64(4), uint64(850), float64(18.5)}
}

func TestInvestmentProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["investment_area"].String == nil || *fact.Fields["investment_area"].String != "product" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["delivery_units"].Integer == nil || *fact.Fields["delivery_units"].Integer != 30 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
}

// TestInvestmentProviderMultipleAreasProduceMultipleFacts proves one team
// can carry several (investment_area, project_stream) facts at once -- this
// provider is a passthrough, not a summary.
func TestInvestmentProviderMultipleAreasProduceMultipleFacts(t *testing.T) {
	t.Parallel()
	rowB := investmentRow("CHAOS")
	rowB[1] = "quality"
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("CHAOS"), rowB}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2", result.Facts)
	}
}

func TestInvestmentProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvestmentProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestInvestmentProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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

// TestInvestmentProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestInvestmentProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("other-team")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested team", result.Facts)
	}
}

// investmentProjectRollupRow shapes one row of the project rollup join
// output: (project_key, team_id, team_name, investment_area, project_stream,
// day, delivery_units, work_items_completed, prs_merged, churn_loc,
// cycle_p50_hours).
func investmentProjectRollupRow(provider, projectID, teamID, teamName, area, stream string, deliveryUnits, workItemsCompleted, prsMerged int64, churnLOC uint64, cycleP50Hours float64) []any {
	return []any{provider + ":" + projectID, teamID, teamName, area, stream, "2026-02-22", deliveryUnits, workItemsCompleted, prsMerged, churnLOC, cycleP50Hours}
}

// TestInvestmentProviderProjectRollupBreaksDownByTeamNeverSums pins CHAOS-4363's
// contract: unlike metrics.go's commit counts, investment counts are NEVER
// summed across owning teams -- each team's own (area, stream) rows survive
// verbatim in the renderable team_breakdown table.
func TestInvestmentProviderProjectRollupBreaksDownByTeamNeverSums(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: [][]any{
		investmentProjectRollupRow("linear", "proj-1", "team-1", "Team One", "product", "growth", 30, 12, 4, 850, 18.5),
		investmentProjectRollupRow("linear", "proj-1", "team-2", "Team Two", "quality", "", 10, 5, 2, 100, 4.0),
	}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "team_project_ownership_breakdown" {
		t.Fatalf("rollup_basis = %#v", fact.Fields["rollup_basis"])
	}
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 2 {
		t.Fatalf("team_count = %#v, want 2", fact.Fields["team_count"])
	}
	if _, hasSum := fact.Fields["delivery_units"]; hasSum {
		t.Fatalf("fields = %#v, want no project-level delivery_units sum -- investment areas are not additive", fact.Fields)
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2", rows)
	}
	if got := rows[0].Fields["delivery_units"].Integer; got == nil || *got != 30 {
		t.Fatalf("row[0].delivery_units = %#v, want team-1's own 30", got)
	}
	if got := rows[1].Fields["delivery_units"].Integer; got == nil || *got != 10 {
		t.Fatalf("row[1].delivery_units = %#v, want team-2's own 10, not summed", got)
	}
	if len(fact.EvidenceRefIDs) != 3 {
		t.Fatalf("evidence_ref_ids = %#v, want project + 2 teams", fact.EvidenceRefIDs)
	}
}

// TestInvestmentProviderProjectRollupNoOwningTeamsHasNoFactEntry mirrors
// metrics.go's identical guard for the investment project path.
func TestInvestmentProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
		t.Fatalf("result = %+v", result)
	}
}

const maxInvestmentRowsPerQueryForTest = 200

func investmentRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = investmentRow("CHAOS")
		rows[i][2] = "stream-" + strconv.Itoa(i)
	}
	return rows
}

func TestInvestmentProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: investmentRows(maxInvestmentRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
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
