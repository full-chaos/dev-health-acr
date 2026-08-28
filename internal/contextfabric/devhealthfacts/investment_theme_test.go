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

// themeMixRow matches readers.ReadTeamThemeMix's own scan order: team_id,
// team_name, kind, key, weighted_effort.
func themeMixRow(teamID, teamName, kind, key string, weightedEffort float64) []any {
	return []any{teamID, teamName, kind, key, weightedEffort}
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
		{match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "Fullchaos", "theme", "feature_delivery", 60),
			themeMixRow("CHAOS", "Fullchaos", "theme", "operational", 20),
			themeMixRow("CHAOS", "Fullchaos", "theme", "maintenance", 10),
			themeMixRow("CHAOS", "Fullchaos", "theme", "quality", 6),
			themeMixRow("CHAOS", "Fullchaos", "theme", "risk", 4),
			themeMixRow("CHAOS", "Fullchaos", "subcategory", "quality.bugfix", 3),
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
		{match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "Fullchaos", "theme", "feature_delivery", 60),
			themeMixRow("CHAOS", "Fullchaos", "theme", "operational", 20),
			themeMixRow("CHAOS", "Fullchaos", "theme", "maintenance", 10),
			themeMixRow("CHAOS", "Fullchaos", "theme", "quality", 6),
			themeMixRow("CHAOS", "Fullchaos", "theme", "risk", 4),
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
// explicit TemporalRange query issues a SECOND theme-mix statement for the
// prior comparable window, and the resulting fact carries normalized
// prior_theme_* shares.
func TestInvestmentProviderThemeMixReadsPriorWindowOnExplicitRange(t *testing.T) {
	t.Parallel()
	current := [][]any{
		themeMixRow("CHAOS", "Fullchaos", "theme", "feature_delivery", 60),
		themeMixRow("CHAOS", "Fullchaos", "theme", "operational", 20),
		themeMixRow("CHAOS", "Fullchaos", "theme", "maintenance", 10),
		themeMixRow("CHAOS", "Fullchaos", "theme", "quality", 6),
		themeMixRow("CHAOS", "Fullchaos", "theme", "risk", 4),
	}
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: current}}}
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
	if themeMixQueries != 2 {
		t.Fatalf("theme mix queries = %d, want exactly 2 (current + prior window)", themeMixQueries)
	}
	found := false
	for _, fact := range result.Facts {
		if _, has := fact.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeFeatureDelivery)]; has {
			found = true
			// The fake client returns the SAME rows for every query it
			// matches, so prior shares equal current shares here -- the
			// assertion is presence and shape, not a specific number.
			if fact.Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeFeatureDelivery)].Number == nil {
				t.Fatalf("prior_theme_feature_delivery has no number value: %#v", fact.Fields)
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: nil}}}
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err == nil {
		t.Fatal("ReadFacts() error = nil, want a failure")
	}
}

// TestInvestmentProviderThemeMixMergesOntoExistingLegacyFactNeverShadowed
// is the codex round-2 RED-first regression guard: a team with BOTH a
// legacy investment_metrics_daily row (readTeamInvestment) AND canonical
// theme-mix data must end up with exactly ONE FactInvestment fact carrying
// BOTH the legacy fields (investment_area) and the canonical fields
// (theme_feature_delivery) -- never two separate facts. Two separate facts
// would let synthesis's own evidence-closure check (model_runtime.go's
// lookupCanonicalFact, first-match by (Kind, Subject)) resolve a claim
// citing a theme_* field against the WRONG (legacy) fact, which lacks that
// field, and reject an otherwise-valid claim.
func TestInvestmentProviderThemeMixMergesOntoExistingLegacyFactNeverShadowed(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM investment_metrics_daily", rows: [][]any{investmentRow("CHAOS")}},
		{match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "Fullchaos", "theme", "feature_delivery", 60),
			themeMixRow("CHAOS", "Fullchaos", "theme", "operational", 20),
			themeMixRow("CHAOS", "Fullchaos", "theme", "maintenance", 10),
			themeMixRow("CHAOS", "Fullchaos", "theme", "quality", 6),
			themeMixRow("CHAOS", "Fullchaos", "theme", "risk", 4),
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
		t.Fatalf("facts = %#v, want exactly 1 (legacy + canonical MERGED, never two separate facts)", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["investment_area"].String == nil || *fact.Fields["investment_area"].String != "product" {
		t.Fatalf("merged fact lost its legacy investment_area field: %#v", fact.Fields)
	}
	themeField, ok := fact.Fields[contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery)]
	if !ok || themeField.Number == nil {
		t.Fatalf("merged fact does not carry the canonical theme_feature_delivery field: %#v", fact.Fields)
	}
}
