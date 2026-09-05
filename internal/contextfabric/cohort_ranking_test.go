package contextfabric

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func rankTestSubject(id string) SubjectRef {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: "team:" + id, Label: id}
}

func rankTestMember(id string) CohortMember {
	return CohortMember{Subject: rankTestSubject(id), Rank: 1, InclusionReasons: []string{"matched"}}
}

func investmentFact(id string, themes map[string]float64, bugfix float64) CanonicalFact {
	fields := make(map[string]FactValue, len(themes)+1)
	for theme, share := range themes {
		fields[FactFieldTheme(theme)] = NumberFactValue(share)
	}
	fields[FactFieldThemeQualityBugfix] = NumberFactValue(bugfix)
	return CanonicalFact{Kind: FactInvestment, Subject: rankTestSubject(id), Fields: fields}
}

func investmentFactWithPrior(id string, themes, prior map[string]float64, bugfix float64) CanonicalFact {
	fact := investmentFact(id, themes, bugfix)
	for theme, share := range prior {
		fact.Fields[FactFieldPriorTheme(theme)] = NumberFactValue(share)
	}
	return fact
}

func healthFact(id, severity string) CanonicalFact {
	return CanonicalFact{Kind: FactHealth, Subject: rankTestSubject(id), Fields: map[string]FactValue{
		"severity": StringFactValue(severity),
	}}
}

func deficiencyFact(id, severity string) CanonicalFact {
	return CanonicalFact{Kind: FactOperationalDeficiencies, Subject: rankTestSubject(id), Fields: map[string]FactValue{
		"severity": StringFactValue(severity),
	}}
}

func readinessFact(id string, ratio float64) CanonicalFact {
	return CanonicalFact{Kind: FactReadiness, Subject: rankTestSubject(id), Fields: map[string]FactValue{
		"estimate_coverage_ratio": NumberFactValue(ratio),
	}}
}

func workloadFact(id string, days int64) CanonicalFact {
	return CanonicalFact{Kind: FactWorkload, Subject: rankTestSubject(id), Fields: map[string]FactValue{
		"forecast_p50_days": IntegerFactValue(days),
	}}
}

func balancedThemes() map[string]float64 {
	return map[string]float64{
		ThemeFeatureDelivery: 0.2, ThemeOperational: 0.2, ThemeMaintenance: 0.2, ThemeQuality: 0.2, ThemeRisk: 0.2,
	}
}

// availableCoverage builds a Coverage whose Sources report SourceAvailable
// for every one of the 5 ranking-formula kinds -- the common case tests
// want when they need familyBatchAdmits to admit rows unconditionally
// (rather than relying on the "no coverage entry" permissive fallback,
// which some tests below deliberately do NOT want).
func availableCoverage() Coverage {
	kinds := []FactKind{FactInvestment, FactHealth, FactOperationalDeficiencies, FactReadiness, FactWorkload}
	sources := make([]SourceObservation, 0, len(kinds))
	for _, kind := range kinds {
		sources = append(sources, SourceObservation{Source: "canonical_fact:" + string(kind), State: SourceAvailable})
	}
	return Coverage{Sources: sources}
}

// deficiencyPrunedCoverage marks operational_deficiencies as an
// UNsuccessful batch read, so deficiencySeveritySignal's available-zero
// exception does NOT fire -- a test isolating a different signal in
// otherwise-empty facts would, without this, always find deficiency
// available too (the "no coverage entry" permissive default resolves an
// absent deficiency batch to "clean, zero fired rules" -- see
// familyBatchAdmits/deficiencySeveritySignal's own doc comments).
func deficiencyPrunedCoverage() Coverage {
	return Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourcePruned}}}
}

func mustScore(t *testing.T, member CohortMember) float64 {
	t.Helper()
	if !member.RankingComputed || member.Score == nil {
		t.Fatalf("member %#v was not ranked", member)
	}
	return *member.Score
}

func TestRankCohort_NilAndEmptyAreNoOps(t *testing.T) {
	t.Parallel()
	got, event, _ := RankCohort(nil, nil, Coverage{})
	if got != nil {
		t.Fatalf("RankCohort(nil, ...) = %v, want nil", got)
	}
	if event.MemberCount != 0 {
		t.Fatalf("event = %#v, want a zero-value event", event)
	}

	empty := &Cohort{Kind: SubjectTeam, Members: nil}
	got, event, _ = RankCohort(empty, []CanonicalFact{healthFact("A", "high")}, Coverage{})
	if got != empty {
		t.Fatalf("RankCohort(empty, ...) did not return the same cohort pointer")
	}
	if len(got.Members) != 0 || event.MemberCount != 0 {
		t.Fatalf("RankCohort mutated an empty cohort: %#v / %#v", got, event)
	}
}

// TestRankCohort_SingleSignalExactScore pins the formula's arithmetic for
// the simplest possible case: ONE available signal family (health), so the
// renormalization denominator is just that family's own weight and the
// Score is exactly 100 * value.
// TestRankCohort_SingleSignalIsInsufficientEvidence is design doc §8: a
// single signal family can NEVER clear the 50-point qualification
// threshold (the largest single weight, investment_mix, is only 30), so
// Outcome is always insufficient_evidence and Score/RankingBasis/Drivers
// stay empty -- DataCompleteness (a pure data-availability measure) still
// reads degraded, independently of Outcome.
func TestRankCohort_SingleSignalIsInsufficientEvidence(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("A")}}
	facts := []CanonicalFact{healthFact("A", "elevated")} // value 0.5, weight 25 -- ONLY available signal
	got, event, _ := RankCohort(cohort, facts, deficiencyPrunedCoverage())
	if len(got.Members) != 1 {
		t.Fatalf("members = %#v, want 1", got.Members)
	}
	member := got.Members[0]
	if member.Score != nil {
		t.Fatalf("Score = %v, want nil (25 weight < 50 qualification threshold)", *member.Score)
	}
	if member.Outcome != CohortOutcomeInsufficientEvidence {
		t.Fatalf("Outcome = %q, want insufficient_evidence", member.Outcome)
	}
	if !reflect.DeepEqual(member.MissingSignals, []string{RankingSignalInvestmentMix, RankingSignalDeficiencySeverity, RankingSignalReadinessGap, RankingSignalWorkloadPressure}) {
		t.Fatalf("MissingSignals = %#v, want the other 4 families", member.MissingSignals)
	}
	if member.AttentionRank != 1 {
		t.Fatalf("AttentionRank = %d, want 1", member.AttentionRank)
	}
	if member.Rank != 1 {
		t.Fatalf("Rank = %d, want UNCHANGED pool-order value 1 (RankCohort never touches Rank)", member.Rank)
	}
	if member.DataCompleteness != CohortDataDegraded {
		t.Fatalf("DataCompleteness = %q, want degraded (1 of 5 families available)", member.DataCompleteness)
	}
	if len(member.RankingBasis) != 0 {
		t.Fatalf("RankingBasis = %#v, want empty (Score is nil)", member.RankingBasis)
	}
	if len(member.Drivers) != 0 {
		t.Fatalf("Drivers = %#v, want empty (Score is nil)", member.Drivers)
	}
	if event.MemberCount != 1 || event.FormulaVersion != RankingFormulaVersion || event.DegradedMemberCount != 1 {
		t.Fatalf("event = %#v", event)
	}
	if event.SignalsAvailable[RankingSignalHealthRisk] != 1 {
		t.Fatalf("SignalsAvailable = %#v, want health_risk: 1", event.SignalsAvailable)
	}
}

// TestRankCohort_ZeroAvailableSignalsIsNilScoreDegradedEmptyBasis proves the
// design's own zero-signal-family resolution (§5b): Score is nil (never a
// fabricated 0, which would render the least-observed team as the
// healthiest), DataCompleteness still reads degraded, RankingBasis is
// empty ("nothing contributed" is what that combination means, not a
// producer bug), and RankingComputed is still true (ranking DID run, it
// just found nothing) -- the contract validator must accept this shape.
func TestRankCohort_ZeroAvailableSignalsIsNilScoreDegradedEmptyBasis(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "matched by kind census", Members: []CohortMember{rankTestMember("A")}}
	got, _, _ := RankCohort(cohort, nil, deficiencyPrunedCoverage())
	member := got.Members[0]
	if !member.RankingComputed {
		t.Fatal("RankingComputed = false, want true (ranking ran, it just found nothing)")
	}
	if member.Score != nil {
		t.Fatalf("Score = %v, want nil", *member.Score)
	}
	if member.Outcome != CohortOutcomeNotApplicable {
		t.Fatalf("Outcome = %q, want not_applicable (zero applicable signals)", member.Outcome)
	}
	if len(member.MissingSignals) != 5 {
		t.Fatalf("MissingSignals = %#v, want all 5 families", member.MissingSignals)
	}
	if member.AttentionRank != 1 {
		t.Fatalf("AttentionRank = %d, want 1 (sole member, placed last among null-Score members trivially)", member.AttentionRank)
	}
	if member.DataCompleteness != CohortDataDegraded {
		t.Fatalf("DataCompleteness = %q, want degraded", member.DataCompleteness)
	}
	if len(member.RankingBasis) != 0 {
		t.Fatalf("RankingBasis = %#v, want empty", member.RankingBasis)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil (nil Score + degraded + empty basis is valid)", err)
	}
}

// TestRankCohort_NilScoreMembersRankLastTiedByPoolOrder proves a zero-
// signal member's AttentionRank is placed strictly after every real-scored
// member, and that among several nil-Score members the tie is broken by
// pool order (design doc §5b).
func TestRankCohort_NilScoreMembersRankLastTiedByPoolOrder(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{
		rankTestMember("NIL_FIRST"), rankTestMember("SCORED"), rankTestMember("NIL_SECOND"),
	}}
	// investment_mix(30)+health(25)=55 clears the 50-point qualification
	// threshold (design doc §8) -- a single family (max weight 30) never
	// would.
	facts := []CanonicalFact{healthFact("SCORED", "high"), investmentFact("SCORED", balancedThemes(), 0)}
	coverage := deficiencyPrunedCoverage()
	got, _, _ := RankCohort(cohort, facts, coverage)
	byID := map[string]CohortMember{}
	for _, m := range got.Members {
		byID[m.Subject.CanonicalID] = m
	}
	scored, nilFirst, nilSecond := byID["team:SCORED"], byID["team:NIL_FIRST"], byID["team:NIL_SECOND"]
	if scored.Score == nil {
		t.Fatal("SCORED member has a nil Score, want a real one")
	}
	if nilFirst.Score != nil || nilSecond.Score != nil {
		t.Fatalf("NIL_FIRST/NIL_SECOND Score = %v/%v, want nil/nil", nilFirst.Score, nilSecond.Score)
	}
	if scored.AttentionRank != 1 {
		t.Fatalf("SCORED AttentionRank = %d, want 1 (the only real-scored member)", scored.AttentionRank)
	}
	if nilFirst.AttentionRank != 2 || nilSecond.AttentionRank != 3 {
		t.Fatalf("NIL_FIRST/NIL_SECOND AttentionRank = %d/%d, want 2/3 (nil-Score members rank last, tied by pool order)", nilFirst.AttentionRank, nilSecond.AttentionRank)
	}
}

// TestRankCohort_NotRankedFieldsAllAbsent proves RankingComputed=false
// keeps every other ranking field at its absent/zero value, and the
// contract validator accepts that shape (the "ranking has not run" case --
// this test constructs it directly since RankCohort itself always sets
// RankingComputed=true for any non-empty cohort it is actually called on).
func TestRankCohort_NotRankedFieldsAllAbsent(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "offers only", Members: []CohortMember{rankTestMember("A")}}
	if err := cohort.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for an unranked member", err)
	}
	if cohort.Members[0].RankingComputed || cohort.Members[0].Score != nil {
		t.Fatalf("unranked member = %#v, want RankingComputed=false, Score=nil", cohort.Members[0])
	}
}

// TestRankCohort_DeterministicOrderAcrossThreeMembers is the plan's own
// acceptance case: 3 synthetic members with varying mix, deterministic
// score/AttentionRank, zero model calls (RankCohort takes no ModelRuntime
// -- there is nothing to call). Members stays in POOL order; AttentionRank
// carries the score-derived order.
func TestRankCohort_DeterministicOrderAcrossThreeMembers(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{
		rankTestMember("STRUGGLING"), rankTestMember("MIDDLING"), rankTestMember("HEALTHY"),
	}}
	facts := []CanonicalFact{
		investmentFact("STRUGGLING", map[string]float64{
			ThemeFeatureDelivery: 0.1, ThemeOperational: 0.5, ThemeMaintenance: 0.1, ThemeQuality: 0.2, ThemeRisk: 0.1,
		}, 0.1),
		healthFact("STRUGGLING", "high"),
		deficiencyFact("STRUGGLING", "critical"),
		readinessFact("STRUGGLING", 0.1),
		workloadFact("STRUGGLING", 90),

		investmentFact("MIDDLING", balancedThemes(), 0.05),
		healthFact("MIDDLING", "elevated"),
		readinessFact("MIDDLING", 0.6),
		workloadFact("MIDDLING", 30),

		investmentFact("HEALTHY", map[string]float64{
			ThemeFeatureDelivery: 0.6, ThemeOperational: 0.05, ThemeMaintenance: 0.15, ThemeQuality: 0.1, ThemeRisk: 0.1,
		}, 0.02),
		healthFact("HEALTHY", "low"),
		readinessFact("HEALTHY", 0.95),
		workloadFact("HEALTHY", 5),
	}
	got, event, _ := RankCohort(cohort, facts, availableCoverage())

	// Pool order (array order) must be UNCHANGED.
	var poolOrder []string
	for _, member := range got.Members {
		poolOrder = append(poolOrder, member.Subject.CanonicalID)
	}
	wantPoolOrder := []string{"team:STRUGGLING", "team:MIDDLING", "team:HEALTHY"}
	if !reflect.DeepEqual(poolOrder, wantPoolOrder) {
		t.Fatalf("pool order = %#v, want unchanged %#v", poolOrder, wantPoolOrder)
	}
	for _, member := range got.Members {
		if member.Rank != 1 {
			t.Fatalf("member %q Rank = %d, want unchanged pool-order value 1 (every rankTestMember starts at Rank 1)", member.Subject.CanonicalID, member.Rank)
		}
	}

	byID := map[string]CohortMember{}
	for _, m := range got.Members {
		byID[m.Subject.CanonicalID] = m
	}
	struggling, middling, healthy := byID["team:STRUGGLING"], byID["team:MIDDLING"], byID["team:HEALTHY"]
	if struggling.AttentionRank != 1 || middling.AttentionRank != 2 || healthy.AttentionRank != 3 {
		t.Fatalf("AttentionRank = %d,%d,%d, want 1,2,3 (STRUGGLING > MIDDLING > HEALTHY)", struggling.AttentionRank, middling.AttentionRank, healthy.AttentionRank)
	}
	if mustScore(t, struggling) <= mustScore(t, middling) || mustScore(t, middling) <= mustScore(t, healthy) {
		t.Fatalf("scores did not strictly decrease: %v > %v > %v ?", mustScore(t, struggling), mustScore(t, middling), mustScore(t, healthy))
	}
	// All three are "complete": MIDDLING/HEALTHY have no deficiency FACT,
	// but availableCoverage() reports operational_deficiencies as a clean
	// SourceAvailable batch, so the design doc's available-zero exception
	// applies -- zero fired rules IS a successful, complete read ("no
	// risk"), not a missing signal.
	if struggling.DataCompleteness != CohortDataComplete || middling.DataCompleteness != CohortDataComplete || healthy.DataCompleteness != CohortDataComplete {
		t.Fatalf("completeness = %q/%q/%q, want complete/complete/complete (all 5 families available -- deficiency's available-zero exception covers MIDDLING/HEALTHY)", struggling.DataCompleteness, middling.DataCompleteness, healthy.DataCompleteness)
	}
	if event.MemberCount != 3 || event.DegradedMemberCount != 0 {
		t.Fatalf("event = %#v", event)
	}
}

// TestRankCohort_TiesKeepPoolOrderInAttentionRank proves sort.SliceStable's
// own guarantee is what the design's "ties keep pool order" requirement
// rests on -- expressed via AttentionRank now, since Members itself never
// reorders.
func TestRankCohort_TiesKeepPoolOrderInAttentionRank(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{
		rankTestMember("FIRST"), rankTestMember("SECOND"), rankTestMember("THIRD"),
	}}
	facts := []CanonicalFact{
		healthFact("FIRST", "elevated"), healthFact("SECOND", "elevated"), healthFact("THIRD", "elevated"),
	}
	got, _, _ := RankCohort(cohort, facts, Coverage{})
	for i, member := range got.Members {
		if member.AttentionRank != i+1 {
			t.Fatalf("member %d (%q) AttentionRank = %d, want %d (tied scores keep pool order)", i, member.Subject.CanonicalID, member.AttentionRank, i+1)
		}
	}
}

// TestRankCohort_InvestmentMixThresholdLabels pins the four sub-signal
// labels firing exactly on their own threshold-crossing conditions.
func TestRankCohort_InvestmentMixThresholdLabels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		themes map[string]float64
		bugfix float64
		want   []string
	}{
		{
			name: "reactive share high",
			themes: map[string]float64{
				ThemeFeatureDelivery: 0.25, ThemeOperational: 0.45, ThemeMaintenance: 0.1, ThemeQuality: 0.1, ThemeRisk: 0.1,
			},
			bugfix: 0,
			want:   []string{DriverReactiveShareHigh},
		},
		{
			name: "deliberate share low",
			themes: map[string]float64{
				ThemeFeatureDelivery: 0.1, ThemeOperational: 0.3, ThemeMaintenance: 0.3, ThemeQuality: 0.2, ThemeRisk: 0.1,
			},
			bugfix: 0,
			want:   []string{DriverDeliberateShareLow},
		},
		{
			// feature_delivery=0.6 is BOTH the deliberate share (well above
			// the 0.20 "low" threshold, so deliberate_share_low does NOT
			// fire) and the max share (above the 0.55 concentration
			// threshold) -- only mix_concentrated fires.
			name: "mix concentrated",
			themes: map[string]float64{
				ThemeFeatureDelivery: 0.6, ThemeOperational: 0.1, ThemeMaintenance: 0.1, ThemeQuality: 0.1, ThemeRisk: 0.1,
			},
			bugfix: 0,
			want:   []string{DriverMixConcentrated},
		},
		{
			name:   "nothing crosses a threshold",
			themes: balancedThemes(),
			bugfix: 0,
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, labels, _, _, _, available, _ := investmentMixSignal([]CanonicalFact{investmentFact("A", tc.themes, tc.bugfix)}, Coverage{})
			if !available {
				t.Fatalf("investmentMixSignal() available = false, want true")
			}
			if !reflect.DeepEqual(labels, tc.want) {
				t.Fatalf("labels = %#v, want %#v (value=%v)", labels, tc.want, value)
			}
		})
	}
}

// TestRankCohort_MixShiftDirectionLabels pins the three mix-shift direction
// labels: toward_operational when operational carries the largest positive
// signed delta, toward_feature when feature_delivery does, and
// mix_shift_other when the largest mover is a third theme.
func TestRankCohort_MixShiftDirectionLabels(t *testing.T) {
	t.Parallel()
	current := map[string]float64{
		ThemeFeatureDelivery: 0.1, ThemeOperational: 0.6, ThemeMaintenance: 0.1, ThemeQuality: 0.1, ThemeRisk: 0.1,
	}
	priorTowardOperational := map[string]float64{
		ThemeFeatureDelivery: 0.4, ThemeOperational: 0.1, ThemeMaintenance: 0.2, ThemeQuality: 0.2, ThemeRisk: 0.1,
	}
	_, labels, _, _, _, available, _ := investmentMixSignal([]CanonicalFact{investmentFactWithPrior("A", current, priorTowardOperational, 0)}, Coverage{})
	if !available {
		t.Fatal("investmentMixSignal() available = false")
	}
	if !containsString(labels, DriverMixShiftTowardOperational) {
		t.Fatalf("labels = %#v, want %q", labels, DriverMixShiftTowardOperational)
	}

	currentTowardFeature := map[string]float64{
		ThemeFeatureDelivery: 0.6, ThemeOperational: 0.1, ThemeMaintenance: 0.1, ThemeQuality: 0.1, ThemeRisk: 0.1,
	}
	priorFeatureLow := map[string]float64{
		ThemeFeatureDelivery: 0.1, ThemeOperational: 0.3, ThemeMaintenance: 0.3, ThemeQuality: 0.2, ThemeRisk: 0.1,
	}
	_, labels, _, _, _, available, _ = investmentMixSignal([]CanonicalFact{investmentFactWithPrior("B", currentTowardFeature, priorFeatureLow, 0)}, Coverage{})
	if !available {
		t.Fatal("investmentMixSignal() available = false")
	}
	if !containsString(labels, DriverMixShiftTowardFeature) {
		t.Fatalf("labels = %#v, want %q", labels, DriverMixShiftTowardFeature)
	}

	// The largest positive mover is `quality` (+0.5) -- neither operational
	// nor feature_delivery -- so the label must be the third, "other" value.
	currentTowardQuality := map[string]float64{
		ThemeFeatureDelivery: 0.1, ThemeOperational: 0.1, ThemeMaintenance: 0.1, ThemeQuality: 0.6, ThemeRisk: 0.1,
	}
	priorQualityLow := map[string]float64{
		ThemeFeatureDelivery: 0.2, ThemeOperational: 0.3, ThemeMaintenance: 0.4, ThemeQuality: 0.1, ThemeRisk: 0.0,
	}
	_, labels, _, _, _, available, _ = investmentMixSignal([]CanonicalFact{investmentFactWithPrior("C", currentTowardQuality, priorQualityLow, 0)}, Coverage{})
	if !available {
		t.Fatal("investmentMixSignal() available = false")
	}
	if !containsString(labels, DriverMixShiftOther) {
		t.Fatalf("labels = %#v, want %q", labels, DriverMixShiftOther)
	}
}

// TestRankCohort_MixShiftTieBreaksByTaxonomyOrder proves a tie on the
// largest positive delta resolves to the fixed taxonomy order
// (feature_delivery, operational, maintenance, quality, risk), never map
// iteration order.
func TestRankCohort_MixShiftTieBreaksByTaxonomyOrder(t *testing.T) {
	t.Parallel()
	// operational and quality both gain +0.25; operational comes first in
	// canonicalThemes, so it must win the tie.
	current := map[string]float64{
		ThemeFeatureDelivery: 0.1, ThemeOperational: 0.35, ThemeMaintenance: 0.1, ThemeQuality: 0.35, ThemeRisk: 0.1,
	}
	prior := map[string]float64{
		ThemeFeatureDelivery: 0.1, ThemeOperational: 0.1, ThemeMaintenance: 0.5, ThemeQuality: 0.1, ThemeRisk: 0.2,
	}
	_, labels, _, _, _, available, _ := investmentMixSignal([]CanonicalFact{investmentFactWithPrior("A", current, prior, 0)}, Coverage{})
	if !available {
		t.Fatal("investmentMixSignal() available = false")
	}
	if !containsString(labels, DriverMixShiftTowardOperational) {
		t.Fatalf("labels = %#v, want %q (tie broken toward the fixed taxonomy's earlier theme)", labels, DriverMixShiftTowardOperational)
	}
	if containsString(labels, DriverMixShiftOther) {
		t.Fatalf("labels = %#v, must not ALSO carry mix_shift_other", labels)
	}
}

// TestRankCohort_WorkloadMinMaxIsRelativeToTheCohort proves the workload
// pressure signal is min-max normalized WITHIN the cohort, not against any
// fixed scale: the same raw forecast_p50_days ranks differently depending
// on which other members are in the cohort.
func TestRankCohort_WorkloadMinMaxIsRelativeToTheCohort(t *testing.T) {
	t.Parallel()
	// Team A also carries investment_mix+health (30+25=55, plus workload's
	// 10 = 65) so its OWN available weight clears the 50-point
	// qualification threshold (design doc §8) in both scenarios --
	// identical in both, so it cannot skew the min-max comparison itself;
	// workload alone (weight 10) never would.
	extraForA := []CanonicalFact{investmentFact("A", balancedThemes(), 0), healthFact("A", "elevated")}

	// Team A is the clear LOW end of a wide spread -> low pressure.
	wideCohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("A"), rankTestMember("B"), rankTestMember("C")}}
	wideFacts := append([]CanonicalFact{workloadFact("A", 10), workloadFact("B", 50), workloadFact("C", 90)}, extraForA...)
	got, _, _ := RankCohort(wideCohort, wideFacts, Coverage{})
	var scoreWide float64
	for _, m := range got.Members {
		if m.Subject.CanonicalID == "team:A" {
			scoreWide = mustScore(t, m)
		}
	}

	// Team A is near the MIDDLE of a narrow spread -> higher relative pressure.
	narrowCohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("A"), rankTestMember("B"), rankTestMember("C")}}
	narrowFacts := append([]CanonicalFact{workloadFact("A", 10), workloadFact("B", 9), workloadFact("C", 11)}, extraForA...)
	got2, _, _ := RankCohort(narrowCohort, narrowFacts, Coverage{})
	var scoreNarrow float64
	for _, m := range got2.Members {
		if m.Subject.CanonicalID == "team:A" {
			scoreNarrow = mustScore(t, m)
		}
	}
	if scoreWide >= scoreNarrow {
		t.Fatalf("team A's Score (raw 10 days, both cases) = %v vs %v -- want strictly lower pressure (lower score) when it is the min-end outlier of a wide-spread cohort", scoreWide, scoreNarrow)
	}
}

func TestNormalizeWorkloadMinMax(t *testing.T) {
	t.Parallel()
	if got := normalizeWorkloadMinMax(30, 30, 30); got != 0.5 {
		t.Fatalf("normalizeWorkloadMinMax(30,30,30) = %v, want 0.5 (zero-spread cohort)", got)
	}
	if got := normalizeWorkloadMinMax(0, 0, 100); got != 0 {
		t.Fatalf("normalizeWorkloadMinMax(0,0,100) = %v, want 0", got)
	}
	if got := normalizeWorkloadMinMax(100, 0, 100); got != 1 {
		t.Fatalf("normalizeWorkloadMinMax(100,0,100) = %v, want 1", got)
	}
}

// TestDeficiencySeveritySignal_TakesMaxAcrossFiredRules proves a team with
// MULTIPLE fired-rule facts (one per rule, never one per team) is scored by
// the WORST one.
func TestDeficiencySeveritySignal_TakesMaxAcrossFiredRules(t *testing.T) {
	t.Parallel()
	value, available, _ := deficiencySeveritySignal([]CanonicalFact{
		deficiencyFact("A", "warning"),
		deficiencyFact("A", "critical"),
	}, availableCoverage())
	if !available || value != 1.0 {
		t.Fatalf("value = %v, available = %v, want (1.0, true)", value, available)
	}
}

// TestDeficiencySeveritySignal_AvailableZeroException proves zero fired-rule
// facts with a clean SourceAvailable batch reads as a real 0 ("no risk"),
// not as missing -- the design doc's own available-zero exception.
func TestDeficiencySeveritySignal_AvailableZeroException(t *testing.T) {
	t.Parallel()
	value, available, _ := deficiencySeveritySignal(nil, availableCoverage())
	if !available || value != 0 {
		t.Fatalf("value = %v, available = %v, want (0, true) -- SourceAvailable batch, zero fired rules", value, available)
	}
}

// TestDeficiencySeveritySignal_NoCoverageEntryDefaultsToAvailable mirrors
// familyBatchAdmits' own "no telemetry, don't demand it" convention: a
// caller (most unit tests, and any legitimate caller with no coverage
// telemetry) that supplies no coverage entry for this kind at all still
// gets the available-zero exception, not a hard missing.
func TestDeficiencySeveritySignal_NoCoverageEntryDefaultsToAvailable(t *testing.T) {
	t.Parallel()
	if _, available, _ := deficiencySeveritySignal(nil, Coverage{}); !available {
		t.Fatal("deficiencySeveritySignal(nil, Coverage{}) available = false, want true (no coverage entry defaults permissive)")
	}
}

// TestDeficiencySeveritySignal_TruncatedBatchWithZeroRowsIsMissing proves a
// Truncated batch does NOT get the available-zero exception: it cannot
// promise there were truly zero fired rules (one could exist past the cap).
func TestDeficiencySeveritySignal_TruncatedBatchWithZeroRowsIsMissing(t *testing.T) {
	t.Parallel()
	coverage := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourceTruncated}}}
	if _, available, _ := deficiencySeveritySignal(nil, coverage); available {
		t.Fatal("deficiencySeveritySignal(nil, truncated) available = true, want false")
	}
}

// TestHealthRiskSignal_UnknownSeverityIsUnavailable proves "unknown" (the
// compounding_risk_daily Enum8's own zero value) reads as a data gap, not
// a favorable low-risk 0.
func TestHealthRiskSignal_UnknownSeverityIsUnavailable(t *testing.T) {
	t.Parallel()
	if _, available, _ := healthRiskSignal([]CanonicalFact{healthFact("A", "unknown")}, Coverage{}); available {
		t.Fatal("healthRiskSignal() available = true for severity=unknown, want false")
	}
	if value, available, _ := healthRiskSignal([]CanonicalFact{healthFact("A", "high")}, Coverage{}); !available || value != 1.0 {
		t.Fatalf("value = %v, available = %v, want (1.0, true)", value, available)
	}
}

// TestHealthRiskSignal_PrunedBatchIsUnavailableEvenWithARow proves a
// Pruned/Unavailable batch state overrides row presence -- a batch that
// failed cannot have a trustworthy row, whatever facts happens to carry.
func TestHealthRiskSignal_PrunedBatchIsUnavailableEvenWithARow(t *testing.T) {
	t.Parallel()
	coverage := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourcePruned}}}
	if _, available, _ := healthRiskSignal([]CanonicalFact{healthFact("A", "high")}, coverage); available {
		t.Fatal("healthRiskSignal() available = true against a pruned batch, want false")
	}
}

// TestReadinessGapSignal_AggregatesWorstAcrossScopes proves multiple
// FactReadiness facts (readiness partitions by provider/work-scope) are
// aggregated by the WORST (lowest) coverage ratio, never an average or the
// first row.
func TestReadinessGapSignal_AggregatesWorstAcrossScopes(t *testing.T) {
	t.Parallel()
	value, available, _ := readinessGapSignal([]CanonicalFact{
		readinessFact("A", 0.9),
		readinessFact("A", 0.2),
	}, Coverage{})
	if !available {
		t.Fatal("readinessGapSignal() available = false")
	}
	want := 1 - 0.2
	if value != want {
		t.Fatalf("value = %v, want %v (gap from the WORST/lowest ratio)", value, want)
	}
}

// TestWorkloadWorstDays_AggregatesMaxAcrossScopes proves multiple
// FactWorkload facts (workload partitions by work scope) are aggregated by
// the WORST (longest) forecast, never an average or the first row.
func TestWorkloadWorstDays_AggregatesMaxAcrossScopes(t *testing.T) {
	t.Parallel()
	days, ok, _ := workloadWorstDays([]CanonicalFact{
		workloadFact("A", 5),
		workloadFact("A", 40),
	}, Coverage{})
	if !ok || days != 40 {
		t.Fatalf("days = %v, ok = %v, want (40, true)", days, ok)
	}
}

// ---------------------------------------------------------------------
// CHAOS-4398 PR2: Drivers (evidence-bearing breakdown of Score)
// ---------------------------------------------------------------------

// TestRankCohort_DriversSumToScore is PR2's central invariant: for a member
// with every one of the 5 families available, Sum(Drivers.WeightContributed)
// must reconstruct Score exactly (within float64 rounding) -- the same
// property internal/contracts/v1's validateDrivers enforces on write, now
// proven for RankCohort's own real output, not just a hand-built fixture.
func TestRankCohort_DriversSumToScore(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		investmentFact("A", balancedThemes(), 0),
		healthFact("A", "elevated"),
		deficiencyFact("A", "critical"),
		readinessFact("A", 0.6),
		workloadFact("A", 20),
	}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	got, _, _ := RankCohort(cohort, facts, availableCoverage())
	member := got.Members[0]
	score := mustScore(t, member)
	if len(member.Drivers) != 5 {
		t.Fatalf("len(Drivers) = %d, want 5 (all families available)", len(member.Drivers))
	}
	var sum float64
	for _, d := range member.Drivers {
		sum += d.WeightContributed
	}
	if diff := sum - score; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("Sum(WeightContributed) = %v, want Score %v", sum, score)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestRankCohort_DriversMatchRankingBasisFamilies proves Drivers' Signal
// set is EXACTLY the family-name subset of RankingBasis, for a PARTIAL
// (not all 5 available) member -- both the family-name entries themselves
// and the driver set must agree on which signals actually contributed.
func TestRankCohort_DriversMatchRankingBasisFamilies(t *testing.T) {
	t.Parallel()
	// health + deficiency (available-zero exception, availableCoverage
	// marks it SourceAvailable) + investment_mix available; readiness/
	// workload have no facts at all. 25+20+30=75 clears the 50-point
	// qualification threshold (design doc §8) -- health+deficiency alone
	// (45) would not.
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	got, _, _ := RankCohort(cohort, facts, availableCoverage())
	member := got.Members[0]
	mustScore(t, member)

	basisFamilies := map[string]bool{}
	for _, entry := range member.RankingBasis {
		basisFamilies[entry] = true // only family-name entries exist here (balancedThemes crosses no sub-label threshold)
	}
	driverSignals := map[string]bool{}
	for _, d := range member.Drivers {
		driverSignals[d.Signal] = true
	}
	if len(basisFamilies) != len(driverSignals) {
		t.Fatalf("RankingBasis families = %v, Drivers signals = %v -- must be the same set", basisFamilies, driverSignals)
	}
	for family := range basisFamilies {
		if !driverSignals[family] {
			t.Fatalf("RankingBasis names %q with no matching driver", family)
		}
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestRankCohort_DriverWindowReflectsPriorComparison proves the
// investment_mix driver's Window is "current_vs_prior" exactly when a real
// prior-window comparison was made, and "current" (including for every
// OTHER family, which never has a prior-window concept) otherwise.
func TestRankCohort_DriverWindowReflectsPriorComparison(t *testing.T) {
	t.Parallel()
	t.Run("with prior window", func(t *testing.T) {
		t.Parallel()
		current := map[string]float64{ThemeFeatureDelivery: 0.1, ThemeOperational: 0.6, ThemeMaintenance: 0.1, ThemeQuality: 0.1, ThemeRisk: 0.1}
		prior := balancedThemes()
		facts := []CanonicalFact{investmentFactWithPrior("A", current, prior, 0)}
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
		got, _, _ := RankCohort(cohort, facts, availableCoverage())
		member := got.Members[0]
		mustScore(t, member)
		var mixDriver *CohortMemberDriver
		for i := range member.Drivers {
			if member.Drivers[i].Signal == RankingSignalInvestmentMix {
				mixDriver = &member.Drivers[i]
			}
		}
		if mixDriver == nil {
			t.Fatal("no investment_mix driver found")
		}
		if mixDriver.Window != DriverWindowCurrentVsPrior {
			t.Fatalf("Window = %q, want %q", mixDriver.Window, DriverWindowCurrentVsPrior)
		}
	})
	t.Run("without prior window", func(t *testing.T) {
		t.Parallel()
		facts := []CanonicalFact{investmentFact("A", balancedThemes(), 0)}
		cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
		got, _, _ := RankCohort(cohort, facts, availableCoverage())
		member := got.Members[0]
		mustScore(t, member)
		var mixDriver *CohortMemberDriver
		for i := range member.Drivers {
			if member.Drivers[i].Signal == RankingSignalInvestmentMix {
				mixDriver = &member.Drivers[i]
			}
		}
		if mixDriver == nil {
			t.Fatal("no investment_mix driver found")
		}
		if mixDriver.Window != DriverWindowCurrent {
			t.Fatalf("Window = %q, want %q", mixDriver.Window, DriverWindowCurrent)
		}
	})
}

// TestRankCohort_ThresholdLabelsMirrorMixDriverLabels proves the
// investment_mix driver's ThresholdLabels is exactly the sub-label set
// investmentMixSignal fired (the same labels RankCohort appends to
// RankingBasis), so a consumer can read WHY investment_mix fired straight
// off the driver, not by cross-referencing RankingBasis separately.
func TestRankCohort_ThresholdLabelsMirrorMixDriverLabels(t *testing.T) {
	t.Parallel()
	// A team that's almost entirely operational + concentrated fires
	// reactive_share_high, mix_concentrated, and deliberate_share_low.
	themes := map[string]float64{ThemeFeatureDelivery: 0.02, ThemeOperational: 0.9, ThemeMaintenance: 0.03, ThemeQuality: 0.03, ThemeRisk: 0.02}
	facts := []CanonicalFact{investmentFact("A", themes, 0)}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	got, _, _ := RankCohort(cohort, facts, availableCoverage())
	member := got.Members[0]
	mustScore(t, member)
	var mixDriver *CohortMemberDriver
	for i := range member.Drivers {
		if member.Drivers[i].Signal == RankingSignalInvestmentMix {
			mixDriver = &member.Drivers[i]
		}
	}
	if mixDriver == nil {
		t.Fatal("no investment_mix driver found")
	}
	want := []string{DriverReactiveShareHigh, DriverDeliberateShareLow, DriverMixConcentrated}
	if !reflect.DeepEqual(mixDriver.ThresholdLabels, want) {
		t.Fatalf("ThresholdLabels = %v, want %v", mixDriver.ThresholdLabels, want)
	}
	// Same labels, in the same order, must also be the tail of RankingBasis.
	gotTail := member.RankingBasis[len(member.RankingBasis)-len(want):]
	if !reflect.DeepEqual(gotTail, want) {
		t.Fatalf("RankingBasis tail = %v, want %v", gotTail, want)
	}
}

// TestRankCohort_ZeroAvailableSignalsHasNoDrivers proves the §5b nil-Score
// shape carries an empty Drivers slice, mirroring the existing empty-
// RankingBasis rule.
func TestRankCohort_ZeroAvailableSignalsHasNoDrivers(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "matched by kind census", Members: []CohortMember{rankTestMember("A")}}
	got, _, _ := RankCohort(cohort, nil, deficiencyPrunedCoverage())
	member := got.Members[0]
	if member.Score != nil {
		t.Fatalf("Score = %v, want nil", *member.Score)
	}
	if len(member.Drivers) != 0 {
		t.Fatalf("Drivers = %#v, want empty", member.Drivers)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------
// CHAOS-4398 PR3b (team-lead ruling): "minting follows citation, not
// ranking" -- RankCohort computes but never mints a ClaimedFact;
// narrateCohortDriverJudgments (post-synthesis) mints one ONLY for a
// driver it actually narrates. See cohortMemberSignalCitations' own doc
// comment for the byte-budget reasoning.
// ---------------------------------------------------------------------

// TestRankCohort_ComputesCitationsButNeverMintsOrSetsProvenance is the
// central PR3b proof at the RankCohort layer: for a single-signal member
// (health.compounding_risk, severity=high), RankCohort must (a) compute a
// citation in its returned cohortMemberSignalCitations map whose
// Kind/Field/Value cite the REAL canonical row this driver's Value was
// computed from -- not a re-derived or normalized number -- and (b) leave
// the driver's own SourceClaimedFactIDs EMPTY: minting is
// narrateCohortDriverJudgments' job now, never RankCohort's.
func TestRankCohort_ComputesCitationsButNeverMintsOrSetsProvenance(t *testing.T) {
	t.Parallel()
	// health(25) + investment_mix(30) = 55 clears the 50-point
	// qualification threshold (design doc §8) with 2 families -- a single
	// family alone (health=25) would land in insufficient_evidence, where
	// Drivers stays empty regardless of what was read (see
	// TestRankCohort_SingleSignalIsInsufficientEvidence).
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	got, _, citations := RankCohort(cohort, facts, availableCoverage())
	member := got.Members[0]
	var driver *CohortMemberDriver
	for i, d := range member.Drivers {
		if d.Signal == RankingSignalHealthRisk {
			driver = &member.Drivers[i]
		}
	}
	if driver == nil {
		t.Fatalf("no health driver found among %+v", member.Drivers)
	}
	if len(driver.SourceClaimedFactIDs) != 0 {
		t.Fatalf("SourceClaimedFactIDs = %v, want EMPTY -- RankCohort must never mint or set provenance itself", driver.SourceClaimedFactIDs)
	}
	citation := citations[member.Subject.CanonicalID][RankingSignalHealthRisk]
	if citation == nil {
		t.Fatalf("no citation computed for (subject=%s, signal=%s)", member.Subject.CanonicalID, RankingSignalHealthRisk)
	}
	if citation.kind != FactHealth {
		t.Fatalf("citation.kind = %q, want %q", citation.kind, FactHealth)
	}
	if citation.field != "severity" {
		t.Fatalf("citation.field = %q, want %q (the RAW field this driver's Value was computed from, not a re-derived name)", citation.field, "severity")
	}
	if citation.value.String == nil || *citation.value.String != "high" {
		t.Fatalf("citation.value = %+v, want the raw string \"high\" -- the actual canonical severity, not driver.Value's mapped 1.0", citation.value)
	}
}

// TestRankCohort_DeficiencyAvailableZeroRanksButNeverCites is the
// available-zero exception's own provenance proof, updated for codex R1
// (CHAOS-4398 PR3b, team-lead ruling superseding the earlier
// "fired_rules_count" citation): OperationalDeficienciesProvider only ever
// emits a CanonicalFact for an actually-fired rule, so zero fired rules
// means ZERO rows -- there is no real fact and no real field anywhere
// this case could cite. The driver still RANKS (Value=0 counts for Score,
// exactly like every other family) but its citation must be nil -- never a
// field invented for a fact that does not exist. narrateCohortDriverJudgments
// already skips any driver with a nil citation, so this driver can never
// be narrated or become a minted ClaimedFact; it stays ranking-only.
func TestRankCohort_DeficiencyAvailableZeroRanksButNeverCites(t *testing.T) {
	t.Parallel()
	// health(25) + investment_mix(30) = 55 clears the qualification
	// threshold -- deficiency itself has NO facts at all (the
	// available-zero case), availableCoverage() marks its batch
	// SourceAvailable.
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	got, _, citations := RankCohort(cohort, facts, availableCoverage())
	member := got.Members[0]
	var deficiencyDriver *CohortMemberDriver
	for i, d := range member.Drivers {
		if d.Signal == RankingSignalDeficiencySeverity {
			deficiencyDriver = &member.Drivers[i]
		}
	}
	if deficiencyDriver == nil {
		t.Fatalf("no deficiency driver found among %+v -- the available-zero exception should still produce one", member.Drivers)
	}
	if deficiencyDriver.Value != 0 {
		t.Fatalf("deficiency driver Value = %v, want 0 -- the available-zero case still ranks as a real zero", deficiencyDriver.Value)
	}
	if len(deficiencyDriver.SourceClaimedFactIDs) != 0 {
		t.Fatalf("deficiency driver SourceClaimedFactIDs = %v, want EMPTY -- RankCohort never mints", deficiencyDriver.SourceClaimedFactIDs)
	}
	if citation := citations[member.Subject.CanonicalID][RankingSignalDeficiencySeverity]; citation != nil {
		t.Fatalf("citation = %+v, want nil -- no real CanonicalFact exists to cite when zero rules fired", citation)
	}
}

// TestNarrateCohortDriverJudgments_NeverNarratesTheDeficiencyAvailableZeroDriver
// is the narration-side half of the same proof: even when this driver is
// the top-weighted (would otherwise be Standing=principal), the absence of
// a citation must make it uncitable, never silently fabricated.
func TestNarrateCohortDriverJudgments_NeverNarratesTheDeficiencyAvailableZeroDriver(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{healthFact("A", "high"), investmentFact("A", balancedThemes(), 0)}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	judgments, minted, _ := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	for _, j := range judgments {
		if j.Category == "operational_deficiency" {
			t.Fatalf("narrated a judgment for the available-zero deficiency driver: %+v", j)
		}
	}
	for _, c := range minted {
		if c.Kind == FactOperationalDeficiencies {
			t.Fatalf("minted a claim for the available-zero deficiency driver: %+v", c)
		}
	}
}

// TestRankCohort_ResultLevelClaimedFactsSatisfyTheWritePathValidator is the
// full end-to-end proof this ruling exists for: running the REAL pipeline
// (RankCohort, then narrateCohortDriverJudgments over its citations) and
// assembling a real InvestigationResult from both outputs must pass the
// FULL write-path Validate(), including the cross-reference check
// (validateCohortDriverClaimedFacts) that a bare Cohort.Validate() cannot
// exercise (it has no ClaimedFacts to check against).
func TestRankCohort_ResultLevelClaimedFactsSatisfyTheWritePathValidator(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		investmentFact("A", balancedThemes(), 0),
		healthFact("A", "elevated"),
		deficiencyFact("A", "critical"),
		readinessFact("A", 0.6),
		workloadFact("A", 20),
	}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())
	narratedJudgments, mintedClaims, _ := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})

	result := InvestigationResult{
		SchemaVersion: InvestigationResultSchemaV1,
		ResultID:      "result_pr3b_claims01", RequestID: "request_pr3b_claims01",
		GeneratedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), Status: InvestigationComplete,
		Question: "Which team is struggling most?",
		Interpretation: InterpretedQuestion{
			Shape: ShapeDiscoveredCohort, RequestedJudgment: "attention_ranking",
			TimeContext:      TimeContext{Axis: TemporalCurrent},
			FactRequirements: []FactRequirement{{Kind: FactHealth}},
		},
		SubjectResolution:   SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		DirectJudgment:      "Team A is the sole ranked member.",
		DeterministicAnswer: "Team A is the sole ranked member.",
		StrongestPressures:  []string{}, Drivers: narratedJudgments,
		RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
		Paths: []RelationshipPath{}, Conflicts: []Finding{},
		Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts: mintedClaims,
		Coverage:     Coverage{Sources: []SourceObservation{}},
		Versions: VersionSet{
			ServiceVersion: "test", ContractVersion: InvestigationResultSchemaV1, Backend: "test",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
		},
		Warnings: []string{},
		Cohort:   ranked,
	}
	if len(mintedClaims) == 0 {
		t.Fatal("mintedClaims = empty, want at least one narrated citation to have minted")
	}
	result.Completeness = ComputeAnswerCompleteness(result)
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() = %v, want nil -- the real RankCohort+narration pipeline's output must satisfy the full write-path validator, including cross-reference", err)
	}
}

// TestRankCohort_MintedClaimIDsAreDeterministicAcrossRepeatedRuns is
// team-lead's own confirmation requirement for the PR body: two full
// RankCohort+narrateCohortDriverJudgments passes over IDENTICAL
// facts/coverage for the same member must mint the IDENTICAL ClaimIDs --
// never a random or process-specific component -- so a replay or an
// answer-reuse hit reproduces the exact citation a fresh pass would also
// mint.
func TestRankCohort_MintedClaimIDsAreDeterministicAcrossRepeatedRuns(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		investmentFact("A", balancedThemes(), 0),
		healthFact("A", "elevated"),
		deficiencyFact("A", "critical"),
		readinessFact("A", 0.6),
		workloadFact("A", 20),
	}
	newCohort := func() *Cohort {
		member := rankTestMember("A")
		member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
		return &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	}
	runOnce := func() (driverIDs, claimIDs []string) {
		ranked, _, citations := RankCohort(newCohort(), facts, availableCoverage())
		_, minted, _ := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
		for _, d := range ranked.Members[0].Drivers {
			driverIDs = append(driverIDs, d.SourceClaimedFactIDs...)
		}
		for _, c := range minted {
			claimIDs = append(claimIDs, c.ClaimID)
		}
		sort.Strings(driverIDs)
		sort.Strings(claimIDs)
		return driverIDs, claimIDs
	}

	firstDriverIDs, firstClaimIDs := runOnce()
	secondDriverIDs, secondClaimIDs := runOnce()

	if len(firstDriverIDs) == 0 {
		t.Fatal("firstDriverIDs = empty, want at least one narrated driver to have minted provenance")
	}
	if !reflect.DeepEqual(firstDriverIDs, secondDriverIDs) {
		t.Fatalf("run 1 driver ClaimIDs = %v, run 2 = %v -- must be identical for identical input", firstDriverIDs, secondDriverIDs)
	}
	if !reflect.DeepEqual(firstClaimIDs, secondClaimIDs) {
		t.Fatalf("run 1 minted claim IDs = %v, run 2 = %v -- must be identical for identical input", firstClaimIDs, secondClaimIDs)
	}
}

// TestCohortDriverClaimID_StaysWithinContractBoundsForMaxLengthCanonicalID
// is codex R1's finding (CHAOS-4398 PR3b): the earlier concatenation-based
// ClaimID ("claim_cohort_" + CanonicalID + "_" + signal + "_" + window +
// "_" + RankingFormulaVersion) could exceed
// ContextFabricModelMintedIDMaxLength (256) for a legal-but-long
// CanonicalID (up to ContextFabricSubjectRefCanonicalIDMaxLength=256
// itself), rejecting an otherwise-valid cohort at result.Validate(). A
// hashed ID must stay well within bounds regardless of CanonicalID length.
func TestCohortDriverClaimID_StaysWithinContractBoundsForMaxLengthCanonicalID(t *testing.T) {
	t.Parallel()
	longCanonicalID := "team:" + strings.Repeat("x", contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength-len("team:"))
	if len(longCanonicalID) != contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength {
		t.Fatalf("test setup: longCanonicalID length = %d, want exactly %d", len(longCanonicalID), contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength)
	}
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: longCanonicalID, Label: "Long"}

	id := cohortDriverClaimID(subject, RankingSignalHealthRisk, DriverWindowCurrent)

	if len(id) < contractsv1.ContextFabricModelMintedIDMinLength || len(id) > contractsv1.ContextFabricModelMintedIDMaxLength {
		t.Fatalf("cohortDriverClaimID length = %d, want within [%d,%d] even for a max-length CanonicalID",
			len(id), contractsv1.ContextFabricModelMintedIDMinLength, contractsv1.ContextFabricModelMintedIDMaxLength)
	}

	// Determinism must survive the hash: same input, same ID, every time.
	if again := cohortDriverClaimID(subject, RankingSignalHealthRisk, DriverWindowCurrent); again != id {
		t.Fatalf("cohortDriverClaimID(%v) = %q, then %q on a repeat call -- must be deterministic", subject, id, again)
	}
}

// ---------------------------------------------------------------------
// validateMintedClaimsGrounded -- codex R1's structural fix (CHAOS-4398
// PR3b, team-lead ruling): every narration-minted claim must re-verify
// against the real canonical fact bundle before it is ever appended, the
// same grounding guarantee SynthesisDraft.ValidateAgainst gives a
// model-authored claim (which narration's own claims never reach).
// ---------------------------------------------------------------------

func groundingTestSubject() SubjectRef {
	return SubjectRef{Kind: SubjectTeam, CanonicalID: "team:CHAOS", Label: "CHAOS"}
}

// TestValidateMintedClaimsGrounded_RejectsAFieldAbsentFromTheSourceFact is
// the red-first proof for the exact bug codex caught: a claim citing a
// field name that exists on NO real CanonicalFact (the now-removed
// "fired_rules_count" invention) must be rejected, even when the claim's
// Kind and Subject both correctly match a real fact.
func TestValidateMintedClaimsGrounded_RejectsAFieldAbsentFromTheSourceFact(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{deficiencyFact("CHAOS", "critical")} // real fact has "severity", not "fired_rules_count"
	claims := []ClaimedFact{{
		ClaimID: "claim_cohort_test01", Kind: FactOperationalDeficiencies, Subject: groundingTestSubject(),
		Field: "fired_rules_count", Value: ScalarValue{Number: func() *float64 { v := 0.0; return &v }()},
	}}
	if err := validateMintedClaimsGrounded(claims, facts); err == nil {
		t.Fatal("validateMintedClaimsGrounded returned nil for a claim citing a field absent from every real fact, want an error")
	}
}

// TestValidateMintedClaimsGrounded_RejectsAValueMismatch proves resolving
// the right Kind/Subject/Field is not sufficient: the VALUE must match too
// -- a claim asserting a different value than what was actually read is
// still ungrounded.
func TestValidateMintedClaimsGrounded_RejectsAValueMismatch(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{deficiencyFact("CHAOS", "critical")} // severity="critical"
	claims := []ClaimedFact{{
		ClaimID: "claim_cohort_test02", Kind: FactOperationalDeficiencies, Subject: groundingTestSubject(),
		Field: "severity", Value: ScalarValue{String: func() *string { v := "warning"; return &v }()}, // wrong value
	}}
	if err := validateMintedClaimsGrounded(claims, facts); err == nil {
		t.Fatal("validateMintedClaimsGrounded returned nil for a claim whose Value does not match the real fact's field, want an error")
	}
}

// TestValidateMintedClaimsGrounded_RejectsAClaimForTheWrongSubject proves
// a real (Kind, Field, Value) match for a DIFFERENT subject's fact does
// not ground a claim about this subject.
func TestValidateMintedClaimsGrounded_RejectsAClaimForTheWrongSubject(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{deficiencyFact("PLATFORM", "critical")} // a different team
	claims := []ClaimedFact{{
		ClaimID: "claim_cohort_test03", Kind: FactOperationalDeficiencies, Subject: groundingTestSubject(), // team:CHAOS
		Field: "severity", Value: ScalarValue{String: func() *string { v := "critical"; return &v }()},
	}}
	if err := validateMintedClaimsGrounded(claims, facts); err == nil {
		t.Fatal("validateMintedClaimsGrounded returned nil for a claim about a subject with no matching fact, want an error")
	}
}

// TestValidateMintedClaimsGrounded_AcceptsARealGroundedClaim is the
// red-first pair's positive case: a claim whose (Kind, Subject, Field,
// Value) all match a real CanonicalFact passes cleanly.
func TestValidateMintedClaimsGrounded_AcceptsARealGroundedClaim(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{deficiencyFact("CHAOS", "critical")}
	claims := []ClaimedFact{{
		ClaimID: "claim_cohort_test04", Kind: FactOperationalDeficiencies, Subject: groundingTestSubject(),
		Field: "severity", Value: ScalarValue{String: func() *string { v := "critical"; return &v }()},
	}}
	if err := validateMintedClaimsGrounded(claims, facts); err != nil {
		t.Fatalf("validateMintedClaimsGrounded(%v) = %v, want nil for a claim that genuinely matches a real fact", claims, err)
	}
}

// TestNarrateCohortDriverJudgments_EveryMintedClaimIsGrounded is the
// end-to-end proof: running the real pipeline (RankCohort, then
// narrateCohortDriverJudgments) over a realistic multi-family scenario
// must produce ONLY claims validateMintedClaimsGrounded accepts against
// the SAME facts RankCohort itself read.
func TestNarrateCohortDriverJudgments_EveryMintedClaimIsGrounded(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		investmentFact("A", balancedThemes(), 0),
		healthFact("A", "elevated"),
		deficiencyFact("A", "critical"),
		readinessFact("A", 0.6),
		workloadFact("A", 20),
	}
	member := rankTestMember("A")
	member.EvidenceRefIDs = []string{"evidence_team_a_roster"}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{member}}
	ranked, _, citations := RankCohort(cohort, facts, availableCoverage())

	_, minted, _ := narrateCohortDriverJudgments(ranked, nil, 0, citations, ItemAllocation{})
	if len(minted) == 0 {
		t.Fatal("expected at least one minted claim from a realistic multi-family scenario")
	}
	if err := validateMintedClaimsGrounded(minted, facts); err != nil {
		t.Fatalf("validateMintedClaimsGrounded rejected a real pipeline's own minted claims: %v", err)
	}
}
