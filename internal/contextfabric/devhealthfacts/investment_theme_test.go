package devhealthfacts_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// themeMixRow shapes one repository-mix row.
//
// CHAOS-6559: the team mix is now the sum over the team's OWNED repositories,
// so a "team" row here is one owned repository "repo-"+teamID (the ownership
// table is faked by ownsRepoTable) and the scan order is window, repo_id, theme map,
// bugfix, work_units.
func themeMixRow(teamID, teamName string, themes map[string]float64, bugfix float64) []any {
	return []any{uint8(0), "repo-" + teamID, themes, bugfix, uint64(3)}
}

// watermarkTable answers the freshness read (max computed_at) ahead of the
// broader work_unit_investments table a test seeds, which would otherwise match
// the same "FROM work_unit_investments" text. It must be listed BEFORE it.
// watermarkTable matches the watermark statement only. CHAOS-7073: the theme
// mix statement now also contains max(computed_at) (the legacy membership
// per-node max), so a bare "max(computed_at)" match would answer the mix read
// with watermark rows.
func watermarkTable() fakeTable {
	return fakeTable{match: "SELECT toString(max(computed_at)), count() FROM work_unit_investments", rows: [][]any{{"2026-09-18 00:00:00.000", uint64(2)}}}
}

func ownsRepoTable(teamID string) fakeTable {
	return fakeTable{match: "FROM team_repo_ownership", rows: [][]any{{teamID, "repo-" + teamID}}}
}

// TestInvestmentProviderThemeMixReadsCanonicalSourceNeverLegacy is the
// RED-first regression guard CHAOS-4398 §0 asks for: the producer must
// read work_unit_investments' canonical theme_distribution_json (via
// readers.ReadTeamThemeMix), never investment_metrics_daily -- the
// deprecated legacy rule set whose investment_area values are not the
// canonical 5-theme taxonomy. Failing this test the way it would fail
// before this producer existed (an InvestmentProvider that only ever
// touched investment_metrics_daily) is the change this PR makes.
func TestInvestmentProviderThemeMixReadsCanonicalSourceNeverLegacy(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM investment_metrics_daily", rows: nil},
		ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "Fullchaos", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 3),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	var themeFact contextfabric.CanonicalFact
	found := false
	for _, fact := range result.Facts {
		if _, has := fact.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)]; has {
			themeFact, found = fact, true
			break
		}
	}
	if !found {
		t.Fatalf("no fact carries the canonical theme fields; facts = %#v", result.Facts)
	}

	// Sums to ~1.0 -- the plan's own acceptance bar for this producer.
	sum := 0.0
	for _, theme := range []string{contextfabric.ThemeFeatureDelivery, contextfabric.ThemeOperational, contextfabric.ThemeMaintenance, contextfabric.ThemeQuality, contextfabric.ThemeRisk} {
		value := themeFact.Fields[contextfabric.FactFieldTheme(theme)]
		if value.Number == nil {
			t.Fatalf("theme %q missing a number value: %#v", theme, themeFact.Fields)
		}
		sum += *value.Number
	}
	if sum < 0.999 || sum > 1.001 {
		t.Fatalf("theme shares sum to %v, want ~1.0", sum)
	}
	wantFeatureShare := 60.0 / 100.0
	if got := *themeFact.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)].Number; got != wantFeatureShare {
		t.Fatalf("feature_delivery share = %v, want %v", got, wantFeatureShare)
	}
	wantBugfixShare := 3.0 / 100.0
	if got := *themeFact.Fields[contextfabric.FactFieldThemeQualityBugfix].Number; got != wantBugfixShare {
		t.Fatalf("quality.bugfix share = %v, want %v", got, wantBugfixShare)
	}

	// The legacy table must never be the source of these fields.
	for _, query := range client.queries {
		if strings.Contains(query.statement, "investment_metrics_daily") && strings.Contains(query.statement, "theme_distribution_json") {
			t.Fatalf("a single statement referenced both the legacy and canonical tables: %q", query.statement)
		}
	}
}

// TestInvestmentProviderThemeMixOmitsPriorFieldsOnCurrentAxis proves the
// prior-window (mix-shift) query only fires when the caller supplied an
// EXPLICIT window (TemporalRange with both bounds) -- never inferred
// (CHAOS-4040). A TemporalCurrent query issues exactly one theme-mix
// statement, and the resulting fact carries no prior_theme_* fields.
func TestInvestmentProviderThemeMixOmitsPriorFieldsOnCurrentAxis(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "Fullchaos", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 0),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	themeMixQueries := 0
	for _, query := range client.queries {
		if strings.Contains(query.statement, "FROM work_unit_investments") {
			themeMixQueries++
		}
	}
	if themeMixQueries != 1 {
		t.Fatalf("theme mix queries = %d, want exactly 1 (no prior-window query on a current-axis request)", themeMixQueries)
	}
	for _, fact := range result.Facts {
		for field := range fact.Fields {
			if strings.HasPrefix(field, "prior_theme_") {
				t.Fatalf("current-axis fact carries a prior_theme_* field: %q", field)
			}
		}
	}
}

// TestInvestmentProviderThemeMixReadsPriorWindowOnExplicitRange proves an
// explicit TemporalRange query reads the current AND the prior comparable
// window in ONE theme-mix statement (CHAOS-6594: one pass over
// work_unit_investments per request), and the resulting fact carries
// normalized prior_theme_* shares taken from the prior window's own rows.
func TestInvestmentProviderThemeMixReadsPriorWindowOnExplicitRange(t *testing.T) {
	t.Parallel()
	prior := themeMixRow("CHAOS", "Fullchaos", map[string]float64{"feature_delivery": 30, "operational": 70}, 0)
	prior[0] = uint8(1)
	rows := [][]any{
		themeMixRow("CHAOS", "Fullchaos", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 0),
		prior,
	}
	client := &fakeClient{tables: []fakeTable{ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", rows: rows}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	start := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	themeMixQueries := 0
	for _, query := range client.queries {
		if strings.Contains(query.statement, "FROM work_unit_investments") {
			themeMixQueries++
		}
	}
	if themeMixQueries != 1 {
		t.Fatalf("theme mix queries = %d, want exactly 1 (current + prior window in one pass)", themeMixQueries)
	}
	found := false
	for _, fact := range result.Facts {
		if _, has := fact.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeFeatureDelivery)]; has {
			found = true
			number := fact.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeFeatureDelivery)].Number
			if number == nil || *number < 0.299 || *number > 0.301 {
				t.Fatalf("prior_theme_feature_delivery = %v, want 0.3 (30 of the prior window's 100)", number)
			}
		}
	}
	if !found {
		t.Fatalf("no fact carries a prior_theme_* field on an explicit-range request; facts = %#v", result.Facts)
	}
}

// TestInvestmentProviderThemeMixZeroCurrentEffortOmitsFact proves a team
// with no current-window weighted effort gets NO theme fields at all --
// never a fabricated 0.0 share (CHAOS-3781 degrade-not-fabricate).
func TestInvestmentProviderThemeMixZeroCurrentEffortOmitsFact(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	for _, fact := range result.Facts {
		if _, has := fact.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)]; has {
			t.Fatalf("a fact with zero source rows carries theme fields: %#v", fact.Fields)
		}
	}
}

// TestInvestmentProviderThemeMixQueryErrorReturnsFactReadFailure proves a
// theme-mix query error fails the whole read the same way the legacy
// investment_metrics_daily query error already does (readFailure), rather
// than degrading silently.
func TestInvestmentProviderThemeMixQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err == nil {
		t.Fatal("ReadFacts() error = nil, want a failure")
	}
}

// TestInvestmentProviderTeamMixIsStandaloneNeverMergedOntoALegacyDayRow
// (CHAOS-6559): a team with legacy investment_metrics_daily rows AND canonical
// mix data gets exactly ONE fact, the mix, carrying no legacy day-row field.
// The legacy rows are never read for a team: merged onto a day row, the mix was
// read by the model as that day's attribute and reported as "no shares".
func TestInvestmentProviderTeamMixIsStandaloneNeverMergedOntoALegacyDayRow(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("CHAOS")}},
		ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "Fullchaos", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 0),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want exactly 1 (the standalone mix)", result.Facts)
	}
	fact := result.Facts[0]
	if _, has := fact.Fields["investment_area"]; has {
		t.Fatalf("mix fact carries a legacy day-row field: %#v", fact.Fields)
	}
	themeField, ok := fact.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)]
	if !ok || themeField.Number == nil {
		t.Fatalf("mix fact does not carry the canonical theme_feature_delivery field: %#v", fact.Fields)
	}
	for _, query := range client.queries {
		if strings.Contains(query.statement, "FROM investment_metrics_daily") {
			t.Fatalf("a team read queried the deprecated investment_metrics_daily: %s", query.statement)
		}
	}
}

// CHAOS-6559: allocation_breakdown for a team needs a DECLARED breakdown
// table, not only scalar theme_* fields. Red on baseline: Capability().Tables
// has no team entry (fact_table_shape_undeclared) and the fact carries no
// theme_breakdown table.
func TestInvestmentProviderTeamDeclaresAndEmitsThemeBreakdownTable(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM investment_metrics_daily", rows: nil},
		ownsRepoTable("CHAOS"),
		{match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 0),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	shapes := provider.Capability().Tables[contextfabric.SubjectTeam]
	if len(shapes) != 1 || shapes[0] != contextfabric.FactTableBreakdown {
		t.Fatalf("team Tables = %v, want [breakdown]", shapes)
	}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	value, ok := result.Facts[0].Fields["theme_breakdown"]
	if !ok || value.Table == nil {
		t.Fatalf("theme_breakdown table missing: %#v", result.Facts[0].Fields)
	}
	if err := value.Table.Validate(); err != nil {
		t.Fatalf("table invalid: %v", err)
	}
	if value.Table.Shape != contextfabric.FactTableBreakdown || len(value.Table.Rows) != 5 {
		t.Fatalf("table = %#v", value.Table)
	}
	got := map[string]float64{}
	for _, row := range value.Table.Rows {
		got[*row.Fields["theme"].String] = *row.Fields["share"].Number
	}
	if got["feature_delivery"] != 0.6 || got["risk"] != 0.04 {
		t.Fatalf("shares = %v", got)
	}
	if result.Facts[0].Fields["mix_source"].String == nil || result.Facts[0].Fields["attribution_basis"].String == nil {
		t.Fatalf("provenance missing: %#v", result.Facts[0].Fields)
	}
}

// investmentSpanTable answers the earliest-stored-unit read of a windowed
// investment read; list it BEFORE the broader work_unit_investments table.
func investmentSpanTable() fakeTable {
	return fakeTable{match: "SELECT toString(min(from_ts)), count() FROM work_unit_investments", rows: [][]any{{"2026-01-01 00:00:00.000000", uint64(2)}}}
}
