package devhealthfacts_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// chaos4645WorkloadSubject mirrors cohort_ranking_test.go's own
// rankTestSubject (unexported, package contextfabric -- not importable from
// here), so this test can build a Cohort/CohortMember pair using only
// contextfabric's exported API.
func chaos4645WorkloadSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
}

// chaos4645WorkloadCoverage mirrors cohort_ranking_test.go's own
// availableCoverage: a Coverage reporting SourceAvailable for all five
// ranking-formula FactKinds, so familyBatchAdmits (unexported, internal to
// RankCohort) admits every family unconditionally and a real, non-nil score
// gets computed -- the strongest form of this pin.
func chaos4645WorkloadCoverage() contextfabric.Coverage {
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

// chaos4645WorkloadMemberFacts builds one member's full 5-signal-family fact
// set (mirroring cohort_ranking_test.go's TestRankCohort_DeterministicOrderAcrossThreeMembers
// construction, via exported types only), so RankCohort actually computes a
// real, non-nil score rather than reporting insufficient evidence off a
// single signal.
func chaos4645WorkloadMemberFacts(id string, bugfixShare float64, severity string, readinessRatio float64, workloadDays int64) []contextfabric.CanonicalFact {
	subject := chaos4645WorkloadSubject(id)
	investmentFields := map[string]contextfabric.FactValue{
		contextfabric.FactFieldTheme("feature_delivery"): contextfabric.NumberFactValue(0.2),
		contextfabric.FactFieldTheme("operational"):      contextfabric.NumberFactValue(0.2),
		contextfabric.FactFieldTheme("maintenance"):      contextfabric.NumberFactValue(0.2),
		contextfabric.FactFieldTheme("quality"):          contextfabric.NumberFactValue(0.2),
		contextfabric.FactFieldTheme("risk"):             contextfabric.NumberFactValue(0.2),
		contextfabric.FactFieldThemeQualityBugfix:        contextfabric.NumberFactValue(bugfixShare),
	}
	return []contextfabric.CanonicalFact{
		{Kind: contextfabric.FactInvestment, Subject: subject, Fields: investmentFields},
		{Kind: contextfabric.FactHealth, Subject: subject, Fields: map[string]contextfabric.FactValue{
			"severity": contextfabric.StringFactValue(severity),
		}},
		{Kind: contextfabric.FactOperationalDeficiencies, Subject: subject, Fields: map[string]contextfabric.FactValue{
			"severity": contextfabric.StringFactValue(severity),
		}},
		{Kind: contextfabric.FactReadiness, Subject: subject, Fields: map[string]contextfabric.FactValue{
			"estimate_coverage_ratio": contextfabric.NumberFactValue(readinessRatio),
		}},
		{Kind: contextfabric.FactWorkload, Subject: subject, Fields: map[string]contextfabric.FactValue{
			"forecast_p50_days": contextfabric.IntegerFactValue(workloadDays),
		}},
	}
}

// TestRankCohort_WorkloadDailySeriesAndFactLevelBasisDoNotChangeRanking is
// the CHAOS-4645 RankCohort-untouched pin for workload.go's producer-side
// widening.
//
// cohort_ranking.go's workloadWorstDays reads fact.Fields["forecast_p50_days"]
// via a plain map lookup by field name, taking the MAXIMUM across every
// FactWorkload fact for a subject. This ticket's two workload.go changes
// are both structurally orthogonal to that field:
//
//   - readTeamWorkload/readProjectWorkload now also attach
//     fields["daily_workload"] -- a NEW field name, additive.
//   - readProjectWorkload's Fable-F3 fix moves "basis" from a
//     team_breakdown ROW column to a fact-level sibling SCALAR -- a
//     different field name from forecast_p50_days.
//
// So RankCohort's score, AttentionRank and DataCompleteness must be
// byte-identical whether or not daily_workload/basis are present on the
// FactWorkload facts it is handed. This builds a full 5-signal-family
// cohort (so a real score is computed, not "insufficient evidence"),
// widens only the FactWorkload facts with the two new fields, and asserts
// the two RankCohort runs agree exactly.
func TestRankCohort_WorkloadDailySeriesAndFactLevelBasisDoNotChangeRanking(t *testing.T) {
	t.Parallel()
	newCohort := func() *contextfabric.Cohort {
		return &contextfabric.Cohort{Kind: contextfabric.SubjectTeam, Members: []contextfabric.CohortMember{
			{Subject: chaos4645WorkloadSubject("STRUGGLING"), Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: chaos4645WorkloadSubject("MIDDLING"), Rank: 1, InclusionReasons: []string{"matched"}},
			{Subject: chaos4645WorkloadSubject("HEALTHY"), Rank: 1, InclusionReasons: []string{"matched"}},
		}}
	}

	var baseline []contextfabric.CanonicalFact
	baseline = append(baseline, chaos4645WorkloadMemberFacts("STRUGGLING", 0.1, "high", 0.1, 90)...)
	baseline = append(baseline, chaos4645WorkloadMemberFacts("MIDDLING", 0.05, "elevated", 0.6, 30)...)
	baseline = append(baseline, chaos4645WorkloadMemberFacts("HEALTHY", 0.02, "low", 0.95, 5)...)

	// A representative daily_workload time_series, shaped exactly the way
	// devhealthfacts/workload.go's workloadDailyTable builds one.
	dailySeries := contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:    contextfabric.FactTableTimeSeries,
		Key:      []string{"day"},
		Measures: []string{"backlog_size", "throughput_mean", "throughput_stddev"},
		Rows: []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{
			"day":               contextfabric.StringFactValue("2026-07-27"),
			"backlog_size":      contextfabric.IntegerFactValue(120),
			"throughput_mean":   contextfabric.NumberFactValue(3.2),
			"throughput_stddev": contextfabric.NumberFactValue(0.8),
		}}},
	})

	widened := make([]contextfabric.CanonicalFact, len(baseline))
	for i, fact := range baseline {
		if fact.Kind != contextfabric.FactWorkload {
			widened[i] = fact
			continue
		}
		widenedFields := make(map[string]contextfabric.FactValue, len(fact.Fields)+2)
		for field, value := range fact.Fields {
			widenedFields[field] = value
		}
		widenedFields["daily_workload"] = dailySeries
		// The F3 fix's fact-level sibling scalar (readProjectWorkload now
		// sets fields["basis"] alongside rollup_basis/team_count/
		// team_breakdown). A different field name from forecast_p50_days,
		// so workloadWorstDays must not observe it.
		widenedFields["basis"] = contextfabric.StringFactValue("capacity_forecast")
		widened[i] = contextfabric.CanonicalFact{Kind: fact.Kind, Subject: fact.Subject, Fields: widenedFields}
	}

	baselineGot, baselineEvent, _ := contextfabric.RankCohort(newCohort(), baseline, chaos4645WorkloadCoverage())
	widenedGot, widenedEvent, _ := contextfabric.RankCohort(newCohort(), widened, chaos4645WorkloadCoverage())

	if baselineEvent.MemberCount != widenedEvent.MemberCount || baselineEvent.DegradedMemberCount != widenedEvent.DegradedMemberCount {
		t.Fatalf("event diverged: baseline=%#v widened=%#v", baselineEvent, widenedEvent)
	}
	if len(baselineGot.Members) != len(widenedGot.Members) {
		t.Fatalf("member count diverged: baseline=%d widened=%d", len(baselineGot.Members), len(widenedGot.Members))
	}
	sawComputedScore := false
	for i := range baselineGot.Members {
		base, wide := baselineGot.Members[i], widenedGot.Members[i]
		if base.Subject.CanonicalID != wide.Subject.CanonicalID {
			t.Fatalf("member order diverged at %d: %q vs %q", i, base.Subject.CanonicalID, wide.Subject.CanonicalID)
		}
		if base.AttentionRank != wide.AttentionRank {
			t.Fatalf("%s: AttentionRank diverged: baseline=%d widened=%d", base.Subject.CanonicalID, base.AttentionRank, wide.AttentionRank)
		}
		if base.DataCompleteness != wide.DataCompleteness {
			t.Fatalf("%s: DataCompleteness diverged: baseline=%q widened=%q", base.Subject.CanonicalID, base.DataCompleteness, wide.DataCompleteness)
		}
		if (base.Score == nil) != (wide.Score == nil) {
			t.Fatalf("%s: Score nil-ness diverged: baseline=%v widened=%v", base.Subject.CanonicalID, base.Score, wide.Score)
		}
		if base.Score != nil && wide.Score != nil {
			if *base.Score != *wide.Score {
				t.Fatalf("%s: score diverged: baseline=%v widened=%v", base.Subject.CanonicalID, *base.Score, *wide.Score)
			}
			sawComputedScore = true
		}
	}
	if !sawComputedScore {
		t.Fatal("no member got a computed score -- this pin needs at least one real score comparison, not just nil==nil")
	}
}
