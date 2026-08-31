package devhealthfacts_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// readinessDailySeriesMatch distinguishes CHAOS-4645's daily-series queries
// (queryTeamReadinessDailySeries / queryProjectReadinessDailySeries) from
// readers.ReadTeamReadiness/readers.ReadProjectReadiness's own latest-row
// query (readiness_test.go's readinessOriginalQueryMatch) -- both read
// estimate_coverage_metrics_daily, so a broad "FROM
// estimate_coverage_metrics_daily" match would route the daily query's rows
// through the SAME canned fixture shaped for the 9-column original query,
// panicking via fakeScanner's unchecked type assertion (the exact bug class
// flow.go's own daily-series queries were found and fixed for; see
// flow_test.go's flowDailySeriesMatch for the identical precedent).
const readinessDailySeriesMatch = "provider, work_scope_id, day ORDER BY computed_at DESC"

// readinessDailySeriesRow shapes one queryTeamReadinessDailySeries /
// queryProjectReadinessDailySeries output row: (team_id or "provider:id",
// day, estimated_count, unestimated_count, backlog_size).
func readinessDailySeriesRow(key, day string, estimated, unestimated, backlog int64) []any {
	return []any{key, day, estimated, unestimated, backlog}
}

// TestReadinessProviderTeamReadsDailyReadinessSeries is CHAOS-4645's core
// team shape (design doc §5.2): a genuine time_series alongside the
// existing scalars, additive -- the pre-existing basis/estimated_count/
// estimate_coverage_ratio fields must stay exactly as before this ticket.
func TestReadinessProviderTeamReadsDailyReadinessSeries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: [][]any{readinessRow("CHAOS")}},
		{match: readinessDailySeriesMatch, rows: [][]any{
			readinessDailySeriesRow("CHAOS", "2026-02-22", 18, 2, 20),
			readinessDailySeriesRow("CHAOS", "2026-02-21", 10, 10, 20),
		}},
	}}
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
	// Additive: the pre-existing scalars are untouched.
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "estimate_coverage" {
		t.Fatalf("basis = %#v, want unchanged", fact.Fields["basis"])
	}
	if fact.Fields["estimate_coverage_ratio"].Number == nil || *fact.Fields["estimate_coverage_ratio"].Number != 0.9 {
		t.Fatalf("estimate_coverage_ratio = %#v, want unchanged at 0.9", fact.Fields["estimate_coverage_ratio"])
	}
	table := fact.Fields["daily_readiness"].Table
	if table == nil {
		t.Fatal("daily_readiness field is missing")
	}
	if table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_readiness.Shape = %q, want time_series", table.Shape)
	}
	if err := fact.Fields["daily_readiness"].Validate(); err != nil {
		t.Fatalf("daily_readiness fails FactValue.Validate(): %v", err)
	}
	rows := fact.Fields["daily_readiness"].Rows
	if len(rows) != 2 {
		t.Fatalf("daily_readiness rows = %d, want 2", len(rows))
	}
	// row 0 (day 2026-02-22): estimated=18, unestimated=2 -> ratio 0.9,
	// computed in Go from the SUMMED counts, never carried off any one
	// scope's own ratio.
	if got := rows[0].Fields["estimate_coverage_ratio"].Number; got == nil || *got != 0.9 {
		t.Fatalf("row[0].estimate_coverage_ratio = %#v, want 0.9", got)
	}
	// row 1 (day 2026-02-21): estimated=10, unestimated=10 -> ratio 0.5.
	if got := rows[1].Fields["estimate_coverage_ratio"].Number; got == nil || *got != 0.5 {
		t.Fatalf("row[1].estimate_coverage_ratio = %#v, want 0.5", got)
	}
}

// TestReadinessProviderTeamDailySeriesOmitsRatioOnZeroDenominator pins the
// divide-by-zero guard (design doc §5.2's explicit requirement): a day with
// zero estimated+unestimated counts must OMIT estimate_coverage_ratio, never
// emit NaN -- contextfabric.NumberFactValue's own Validate() rejects a
// non-finite number outright (model.go's FactValue.validate).
func TestReadinessProviderTeamDailySeriesOmitsRatioOnZeroDenominator(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: [][]any{readinessRow("CHAOS")}},
		{match: readinessDailySeriesMatch, rows: [][]any{
			readinessDailySeriesRow("CHAOS", "2026-02-22", 0, 0, 0),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	rows := result.Facts[0].Fields["daily_readiness"].Rows
	if len(rows) != 1 {
		t.Fatalf("daily_readiness rows = %d, want 1", len(rows))
	}
	if _, ok := rows[0].Fields["estimate_coverage_ratio"]; ok {
		t.Fatalf("row fields = %#v, want estimate_coverage_ratio omitted on zero denominator", rows[0].Fields)
	}
	if err := result.Facts[0].Fields["daily_readiness"].Validate(); err != nil {
		t.Fatalf("daily_readiness fails FactValue.Validate(): %v", err)
	}
}

// TestReadinessProviderTeamDailySeriesAttachedOnlyToFirstScopeFact proves
// readTeamReadiness's own documented choice: a team can mint several
// FactReadiness facts (one per (work_scope, provider) row), and
// daily_readiness -- already summed ACROSS a team's scopes -- is attached to
// only the FIRST one, never duplicated onto every sibling fact.
func TestReadinessProviderTeamDailySeriesAttachedOnlyToFirstScopeFact(t *testing.T) {
	t.Parallel()
	row2 := readinessRow("CHAOS")
	row2[1] = "scope-2" // second (work_scope, provider) row for the SAME team
	client := &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: [][]any{readinessRow("CHAOS"), row2}},
		{match: readinessDailySeriesMatch, rows: [][]any{readinessDailySeriesRow("CHAOS", "2026-02-22", 18, 2, 20)}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 (one per work_scope row)", result.Facts)
	}
	_, firstHas := result.Facts[0].Fields["daily_readiness"]
	_, secondHas := result.Facts[1].Fields["daily_readiness"]
	if !firstHas {
		t.Fatalf("facts[0] = %#v, want daily_readiness on the FIRST fact", result.Facts[0].Fields)
	}
	if secondHas {
		t.Fatalf("facts[1] = %#v, want daily_readiness NOT duplicated onto the second fact", result.Facts[1].Fields)
	}
}

// TestReadinessProviderProjectReadsDailyReadinessSeries mirrors the team
// case for the project rollup (CHAOS-4645, design doc §5.2), additive
// alongside the existing team_breakdown.
func TestReadinessProviderProjectReadsDailyReadinessSeries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: [][]any{
			readinessProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", "linear", 18, 2, 20, 0.9),
		}},
		{match: readinessDailySeriesMatch, rows: [][]any{
			readinessDailySeriesRow("linear:proj-1", "2026-02-22", 18, 2, 20),
		}},
	}}
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
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 1 {
		t.Fatalf("team_count = %#v, want unchanged", fact.Fields["team_count"])
	}
	if len(fact.Fields["team_breakdown"].Rows) != 1 {
		t.Fatalf("team_breakdown rows = %#v, want unchanged at 1", fact.Fields["team_breakdown"].Rows)
	}
	table := fact.Fields["daily_readiness"].Table
	if table == nil {
		t.Fatal("daily_readiness field is missing")
	}
	if table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_readiness.Shape = %q, want time_series", table.Shape)
	}
	if err := fact.Fields["daily_readiness"].Validate(); err != nil {
		t.Fatalf("daily_readiness fails FactValue.Validate(): %v", err)
	}
}

// TestReadinessProviderProjectRollupBasisMovesToFactLevelScalar pins the
// CHAOS-4645 F3 fix: "basis" is no longer a per-row column of
// team_breakdown (constant "estimate_coverage" on every row -- the Fable F3
// violation CHAOS-4633 disclosed and deliberately deferred). It is now a
// sibling scalar field on the fact itself, at the same level as
// rollup_basis/team_count. The row count and every other row value are
// UNCHANGED -- only the field's LOCATION moves.
func TestReadinessProviderProjectRollupBasisMovesToFactLevelScalar(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: readinessOriginalQueryMatch, rows: [][]any{
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
	// Fact-level scalar, sibling to rollup_basis/team_count.
	if fact.Fields["basis"].String == nil || *fact.Fields["basis"].String != "estimate_coverage" {
		t.Fatalf("fact-level basis = %#v, want \"estimate_coverage\"", fact.Fields["basis"])
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2 (row count unaffected by the F3 fix)", rows)
	}
	for i, row := range rows {
		if _, has := row.Fields["basis"]; has {
			t.Fatalf("team_breakdown row[%d] = %#v, want NO row-level basis column", i, row.Fields)
		}
	}
	// Every other row value is byte-identical to before the fix.
	if got := rows[0].Fields["estimated_count"].Integer; got == nil || *got != 18 {
		t.Fatalf("row[0].estimated_count = %#v, want team-1's own 18", got)
	}
	if got := rows[1].Fields["estimate_coverage_ratio"].Number; got == nil || *got != 0.25 {
		t.Fatalf("row[1].estimate_coverage_ratio = %#v, want team-2's own 0.25", got)
	}
	// The declared Key no longer names "basis" -- a column constant across
	// the whole table must never be declared into Key or Measures
	// (model.go's FactTable.Validate would reject a row missing a declared
	// Key column, and "basis" is no longer a row column at all).
	if err := fact.Fields["team_breakdown"].Validate(); err != nil {
		t.Fatalf("team_breakdown fails FactValue.Validate(): %v", err)
	}
	table := fact.Fields["team_breakdown"].Table
	for _, key := range table.Key {
		if key == "basis" {
			t.Fatalf("team_breakdown.Table.Key = %#v, want \"basis\" dropped", table.Key)
		}
	}
}

// rankCohortReadinessFact builds a FactReadiness CanonicalFact the way
// readTeamReadiness actually produces one (CHAOS-4645): basis, the scalar
// counts, estimate_coverage_ratio, and -- when withDaily is true -- the new
// daily_readiness time_series field alongside it, additive.
func rankCohortReadinessFact(id string, ratio float64, withDaily bool) contextfabric.CanonicalFact {
	fields := map[string]contextfabric.FactValue{
		"basis":                   contextfabric.StringFactValue("estimate_coverage"),
		"work_scope_id":           contextfabric.StringFactValue("scope-1"),
		"provider":                contextfabric.StringFactValue("linear"),
		"day":                     contextfabric.StringFactValue("2026-02-22"),
		"estimated_count":         contextfabric.IntegerFactValue(18),
		"unestimated_count":       contextfabric.IntegerFactValue(2),
		"backlog_size":            contextfabric.IntegerFactValue(20),
		"estimate_coverage_ratio": contextfabric.NumberFactValue(ratio),
	}
	if withDaily {
		fields["daily_readiness"] = contextfabric.TableFactValue(contextfabric.FactTable{
			Shape:    contextfabric.FactTableTimeSeries,
			Key:      []string{"day"},
			Measures: []string{"estimated_count", "unestimated_count", "backlog_size", "estimate_coverage_ratio"},
			Grain:    contextfabric.GrainDay,
			Rows: []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{
				"day":                     contextfabric.StringFactValue("2026-02-22"),
				"estimated_count":         contextfabric.IntegerFactValue(18),
				"unestimated_count":       contextfabric.IntegerFactValue(2),
				"backlog_size":            contextfabric.IntegerFactValue(20),
				"estimate_coverage_ratio": contextfabric.NumberFactValue(ratio),
			}}},
		})
	}
	return contextfabric.CanonicalFact{
		Kind:    contextfabric.FactReadiness,
		Subject: teamSubject(id),
		Fields:  fields,
	}
}

// rankCohortHealthFact/rankCohortInvestmentFact/rankCohortDeficiencyFact/
// rankCohortWorkloadFact rebuild the OTHER four RankCohort signal families'
// minimal fixtures (internal/contextfabric/cohort_ranking_test.go's
// healthFact/investmentFact/deficiencyFact/workloadFact), which are
// unexported and therefore unusable from this external _test package. Only
// enough signal families to clear RankCohort's 50-point qualification
// threshold (design doc §8) are needed -- the SAME five families
// TestRankCohort_DeterministicOrderAcrossThreeMembers uses.
func rankCohortHealthFact(id, severity string) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{Kind: contextfabric.FactHealth, Subject: teamSubject(id), Fields: map[string]contextfabric.FactValue{
		"severity": contextfabric.StringFactValue(severity),
	}}
}

func rankCohortInvestmentFact(id string, themes map[string]float64, bugfix float64) contextfabric.CanonicalFact {
	fields := make(map[string]contextfabric.FactValue, len(themes)+1)
	for theme, share := range themes {
		fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(share)
	}
	fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(bugfix)
	return contextfabric.CanonicalFact{Kind: contextfabric.FactInvestment, Subject: teamSubject(id), Fields: fields}
}

func rankCohortDeficiencyFact(id, severity string) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{Kind: contextfabric.FactOperationalDeficiencies, Subject: teamSubject(id), Fields: map[string]contextfabric.FactValue{
		"severity": contextfabric.StringFactValue(severity),
	}}
}

func rankCohortWorkloadFact(id string, days int64) contextfabric.CanonicalFact {
	return contextfabric.CanonicalFact{Kind: contextfabric.FactWorkload, Subject: teamSubject(id), Fields: map[string]contextfabric.FactValue{
		"forecast_p50_days": contextfabric.IntegerFactValue(days),
	}}
}

func rankCohortAvailableCoverage() contextfabric.Coverage {
	kinds := []contextfabric.FactKind{
		contextfabric.FactInvestment, contextfabric.FactHealth, contextfabric.FactOperationalDeficiencies,
		contextfabric.FactReadiness, contextfabric.FactWorkload,
	}
	sources := make([]contextfabric.SourceObservation, 0, len(kinds))
	for _, kind := range kinds {
		sources = append(sources, contextfabric.SourceObservation{Source: "canonical_fact:" + string(kind), State: contextfabric.SourceAvailable})
	}
	return contextfabric.Coverage{Sources: sources}
}

// TestRankCohortReadinessUntouchedByDailySeriesField is the REQUIRED
// RankCohort-untouched pin (CHAOS-4645): readinessGapSignal
// (internal/contextfabric/cohort_ranking.go) reads
// fact.Fields["estimate_coverage_ratio"] via a plain map lookup by field
// name, across every FactReadiness fact for the subject. Adding
// daily_readiness alongside it -- and, independently, moving "basis" from a
// project-rollup ROW to a fact-level scalar -- touches neither the field
// name nor its value, so RankCohort's score/basis/ranking for an otherwise
// identical fact set must be BYTE IDENTICAL with and without the new field.
func TestRankCohortReadinessUntouchedByDailySeriesField(t *testing.T) {
	t.Parallel()
	buildCohort := func() *contextfabric.Cohort {
		return &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{
			{Subject: teamSubject("CHAOS"), Rank: 1, InclusionReasons: []string{"matched"}},
		}}
	}
	buildFacts := func(withDaily bool) []contextfabric.CanonicalFact {
		return []contextfabric.CanonicalFact{
			rankCohortInvestmentFact("CHAOS", map[string]float64{
				contextfabric.ThemeFeatureDelivery: 0.1, contextfabric.ThemeOperational: 0.5,
				contextfabric.ThemeMaintenance: 0.1, contextfabric.ThemeQuality: 0.2, contextfabric.ThemeRisk: 0.1,
			}, 0.1),
			rankCohortHealthFact("CHAOS", "high"),
			rankCohortDeficiencyFact("CHAOS", "critical"),
			rankCohortReadinessFact("CHAOS", 0.1, withDaily),
			rankCohortWorkloadFact("CHAOS", 90),
		}
	}

	baseline, _, _ := contextfabric.RankCohort(buildCohort(), buildFacts(false), rankCohortAvailableCoverage())
	augmented, _, _ := contextfabric.RankCohort(buildCohort(), buildFacts(true), rankCohortAvailableCoverage())

	if len(baseline.Members) != 1 || len(augmented.Members) != 1 {
		t.Fatalf("members = %d/%d, want 1/1", len(baseline.Members), len(augmented.Members))
	}
	base, aug := baseline.Members[0], augmented.Members[0]
	if !base.RankingComputed || !aug.RankingComputed {
		t.Fatalf("RankingComputed = %v/%v, want both true (member must clear the qualification threshold)", base.RankingComputed, aug.RankingComputed)
	}
	if base.Score == nil || aug.Score == nil || *base.Score != *aug.Score {
		t.Fatalf("Score = %v/%v, want identical -- daily_readiness must not move the ranking numbers", base.Score, aug.Score)
	}
	if base.AttentionRank != aug.AttentionRank {
		t.Fatalf("AttentionRank = %d/%d, want identical", base.AttentionRank, aug.AttentionRank)
	}
	if base.DataCompleteness != aug.DataCompleteness {
		t.Fatalf("DataCompleteness = %q/%q, want identical", base.DataCompleteness, aug.DataCompleteness)
	}
	if !reflect.DeepEqual(base.RankingBasis, aug.RankingBasis) {
		t.Fatalf("RankingBasis = %#v / %#v, want identical", base.RankingBasis, aug.RankingBasis)
	}
}
