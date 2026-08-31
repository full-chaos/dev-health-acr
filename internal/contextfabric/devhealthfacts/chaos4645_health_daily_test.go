package devhealthfacts_test

import (
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// chaos4645RankSubject mirrors cohort_ranking_test.go's own unexported
// rankTestSubject -- that helper lives in package contextfabric and cannot
// be imported from here, so this is a small equivalent.
func chaos4645RankSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
}

// chaos4645RankFacts builds the SAME 4-signal fact bundle
// cohort_ranking_test.go's TestRankCohort_DeterministicOrderAcrossThreeMembers
// uses for its "STRUGGLING" member -- enough available signal weight
// (investment_mix + health + readiness + workload, well over the 50-point
// qualification threshold) to actually compute a Score/RankingBasis rather
// than fall to insufficient_evidence, which a single-signal bundle always
// does (see TestRankCohort_SingleSignalIsInsufficientEvidence in that same
// file). withDailyHealth controls ONLY whether the health fact ALSO carries
// this ticket's new additive "daily_health" field -- every other field,
// byte-for-byte the same.
func chaos4645RankFacts(id string, withDailyHealth bool) []contextfabric.CanonicalFact {
	healthFields := map[string]contextfabric.FactValue{
		"severity": contextfabric.StringFactValue("high"),
	}
	if withDailyHealth {
		healthFields["daily_health"] = contextfabric.TableFactValue(contextfabric.FactTable{
			Shape:    contextfabric.FactTableTimeSeries,
			Key:      []string{"day"},
			Measures: []string{"compounding_risk", "severity"},
			Rows: []contextfabric.FactValueRow{
				{Fields: map[string]contextfabric.FactValue{
					"day":              contextfabric.StringFactValue("2026-02-21"),
					"compounding_risk": contextfabric.NumberFactValue(0.61),
					"severity":         contextfabric.StringFactValue("high"),
				}},
			},
		})
	}
	investmentFields := map[string]contextfabric.FactValue{
		contextfabric.FactFieldTheme(contextfabric.ThemeFeatureDelivery): contextfabric.NumberFactValue(0.1),
		contextfabric.FactFieldTheme(contextfabric.ThemeOperational):     contextfabric.NumberFactValue(0.5),
		contextfabric.FactFieldTheme(contextfabric.ThemeMaintenance):     contextfabric.NumberFactValue(0.1),
		contextfabric.FactFieldTheme(contextfabric.ThemeQuality):         contextfabric.NumberFactValue(0.2),
		contextfabric.FactFieldTheme(contextfabric.ThemeRisk):            contextfabric.NumberFactValue(0.1),
		contextfabric.FactFieldThemeQualityBugfix:                        contextfabric.NumberFactValue(0.1),
	}
	return []contextfabric.CanonicalFact{
		{Kind: contextfabric.FactInvestment, Subject: chaos4645RankSubject(id), Fields: investmentFields},
		{Kind: contextfabric.FactHealth, Subject: chaos4645RankSubject(id), Fields: healthFields},
		{Kind: contextfabric.FactReadiness, Subject: chaos4645RankSubject(id), Fields: map[string]contextfabric.FactValue{
			"estimate_coverage_ratio": contextfabric.NumberFactValue(0.1),
		}},
		{Kind: contextfabric.FactWorkload, Subject: chaos4645RankSubject(id), Fields: map[string]contextfabric.FactValue{
			"forecast_p50_days": contextfabric.IntegerFactValue(90),
		}},
	}
}

// chaos4645Coverage reports every RankCohort signal family as a clean,
// successful read -- mirroring cohort_ranking_test.go's own
// availableCoverage, which cannot be imported here for the same reason
// chaos4645RankSubject cannot.
func chaos4645Coverage() contextfabric.Coverage {
	kinds := []contextfabric.FactKind{
		contextfabric.FactInvestment, contextfabric.FactHealth,
		contextfabric.FactOperationalDeficiencies, contextfabric.FactReadiness, contextfabric.FactWorkload,
	}
	sources := make([]contextfabric.SourceObservation, 0, len(kinds))
	for _, kind := range kinds {
		sources = append(sources, contextfabric.SourceObservation{Source: "canonical_fact:" + string(kind), State: contextfabric.SourceAvailable})
	}
	return contextfabric.Coverage{Sources: sources}
}

// TestHealthDailySeriesNeverMovesRankCohort pins CHAOS-4645's design doc
// §5.2 promise verbatim: "additive -- the scalar stays, so RankCohort's
// inputs are untouched and the ranking numbers cannot move".
// cohort_ranking.go's healthRiskSignal reads ONLY fact.Fields["severity"]
// via a plain map lookup (stringField), so adding a SIBLING "daily_health"
// field to the SAME CanonicalFact can structurally never change what it
// returns -- this test PROVES that rather than merely asserting it, by
// running the full contextfabric.RankCohort formula (healthRiskSignal
// itself is unexported and unreachable from this _test package) twice over
// otherwise-identical fact bundles and requiring an IDENTICAL Score,
// RankingBasis and DataCompleteness.
func TestHealthDailySeriesNeverMovesRankCohort(t *testing.T) {
	t.Parallel()
	without := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{
		{Subject: chaos4645RankSubject("A"), Rank: 1, InclusionReasons: []string{"matched"}},
	}}
	with := &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{
		{Subject: chaos4645RankSubject("A"), Rank: 1, InclusionReasons: []string{"matched"}},
	}}
	coverage := chaos4645Coverage()

	gotWithout, _, _ := contextfabric.RankCohort(without, chaos4645RankFacts("A", false), coverage)
	gotWith, _, _ := contextfabric.RankCohort(with, chaos4645RankFacts("A", true), coverage)

	memberWithout, memberWith := gotWithout.Members[0], gotWith.Members[0]
	if !memberWithout.RankingComputed || !memberWith.RankingComputed {
		t.Fatalf("want both ranked: without=%v with=%v", memberWithout.RankingComputed, memberWith.RankingComputed)
	}
	if memberWithout.Score == nil || memberWith.Score == nil || *memberWithout.Score != *memberWith.Score {
		t.Fatalf("Score moved: without=%v with=%v -- daily_health must be purely additive", memberWithout.Score, memberWith.Score)
	}
	if !reflect.DeepEqual(memberWithout.RankingBasis, memberWith.RankingBasis) {
		t.Fatalf("RankingBasis moved: without=%#v with=%#v", memberWithout.RankingBasis, memberWith.RankingBasis)
	}
	if memberWithout.DataCompleteness != memberWith.DataCompleteness {
		t.Fatalf("DataCompleteness moved: without=%q with=%q", memberWithout.DataCompleteness, memberWith.DataCompleteness)
	}
	if memberWithout.AttentionRank != memberWith.AttentionRank {
		t.Fatalf("AttentionRank moved: without=%d with=%d", memberWithout.AttentionRank, memberWith.AttentionRank)
	}
}
