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
	return []any{teamID, "product", "growth", "2026-02-22", int64(30), int64(12), int64(4), uint64(850), float64(18.5), uint8(1), float64(18.5), uint8(1)}
}

func teamMixTables(teamID string) []fakeTable {
	return []fakeTable{watermarkTable(), ownsRepoTable(teamID), {match: "FROM work_unit_investments", rows: [][]any{
		themeMixRow(teamID, "", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 0),
	}}}
}

func TestInvestmentProviderHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: teamMixTables("CHAOS")}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || result.State != contextfabric.SourceAvailable || result.Reason != "" {
		t.Fatalf("result = %#v, want 1 fact, available, no reason", result)
	}
	fact := result.Facts[0]
	if got := fact.Fields["theme_feature_delivery"].Number; got == nil || *got < 0.6-1e-9 || *got > 0.6+1e-9 {
		t.Fatalf("fields = %#v, want theme_feature_delivery 0.6", fact.Fields)
	}
}

// TestInvestmentProviderTwoTeamsOneWithoutAMixServesOneAndDisclosesTheOther
// pins the loud-absence rule: no fact and no fabricated zero for the team
// without a mix, and the reason says how many are unavailable.
func TestInvestmentProviderTwoTeamsOneWithoutAMixServesOneAndDisclosesTheOther(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: teamMixTables("CHAOS")}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS"), teamSubject("ghost")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 || !strings.Contains(result.Reason, "investment mix unavailable for 1 of 2 requested teams") {
		t.Fatalf("result = %#v", result)
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
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestInvestmentProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_repo_ownership", err: errors.New("boom")}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_repo_ownership", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(client.queries) < 2 {
		t.Fatalf("queries = %d, want the ownership read and the watermark read", len(client.queries))
	}
	ownership := client.queries[0]
	for _, binding := range ownership.bindings {
		if binding.Name == "org_id" && binding.Value != "org-8" {
			t.Fatalf("ownership org_id binding = %v", binding.Value)
		}
		if binding.Name == "ids" {
			if ids, _ := binding.Value.([]string); len(ids) != 1 || ids[0] != "CHAOS" {
				t.Fatalf("ownership ids binding = %#v, want exactly the requested subject", binding.Value)
			}
		}
	}
	assertQueryScopedToOrgAndSubjects(t, ownership.statement)
	watermark := client.queries[len(client.queries)-1]
	if !strings.Contains(watermark.statement, "FROM work_unit_investments WHERE org_id = {org_id:String}") {
		t.Fatalf("watermark statement = %q, want it scoped to the org", watermark.statement)
	}
}

// TestInvestmentProviderRowForUnrequestedTeamNeverAppears is the F5
// result-content guard.
func TestInvestmentProviderRowForUnrequestedTeamNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: teamMixTables("other-team")}
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
	return []any{provider + ":" + projectID, teamID, teamName, area, stream, "2026-02-22", deliveryUnits, workItemsCompleted, prsMerged, churnLOC, cycleP50Hours, uint8(1), cycleP50Hours, uint8(1)}
}

// TestInvestmentProviderProjectRollupBreaksDownByTeamNeverSums pins CHAOS-4363's
// contract: unlike metrics.go's commit counts, investment counts are NEVER
// summed across owning teams -- each team's own (area, stream) rows survive
// verbatim in the renderable team_breakdown table.
func TestInvestmentProviderProjectRollupBreaksDownByTeamNeverSums(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: [][]any{
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

// TestInvestmentProviderProjectRollupCapsBreakdownAt64Rows is codex round-1
// P1: contextfabric.FactValue.Validate rejects a Rows table over 64 entries
// outright, so a project with more distinct (team, area, stream) rows than
// that must be CAPPED, with Truncated reported, never passed through
// verbatim as a hard read error.
func TestInvestmentProviderProjectRollupCapsBreakdownAt64Rows(t *testing.T) {
	t.Parallel()
	const rowsOverCap = 70
	rows := make([][]any, rowsOverCap)
	for i := 0; i < rowsOverCap; i++ {
		rows[i] = investmentProjectRollupRow("linear", "proj-1", "team-"+strconv.Itoa(i), "Team", "product", "growth", 1, 1, 0, 0, 0)
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: rows}}}
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
	breakdown := result.Facts[0].Fields["team_breakdown"].Rows
	if len(breakdown) != 64 {
		t.Fatalf("team_breakdown rows = %d, want capped at 64", len(breakdown))
	}
	if err := result.Facts[0].Fields["team_breakdown"].Validate(); err != nil {
		t.Fatalf("capped team_breakdown still fails FactValue.Validate(): %v -- the 64-row cap must make this always pass", err)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true when a project's breakdown is capped")
	}
}
