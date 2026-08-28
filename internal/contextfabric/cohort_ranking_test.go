package contextfabric

import (
	"reflect"
	"testing"
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
	got, event := RankCohort(nil, nil, Coverage{})
	if got != nil {
		t.Fatalf("RankCohort(nil, ...) = %v, want nil", got)
	}
	if event.MemberCount != 0 {
		t.Fatalf("event = %#v, want a zero-value event", event)
	}

	empty := &Cohort{Kind: SubjectTeam, Members: nil}
	got, event = RankCohort(empty, []CanonicalFact{healthFact("A", "high")}, Coverage{})
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
func TestRankCohort_SingleSignalExactScore(t *testing.T) {
	t.Parallel()
	cohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("A")}}
	facts := []CanonicalFact{healthFact("A", "elevated")} // value 0.5, weight 25 -- ONLY available signal
	got, event := RankCohort(cohort, facts, deficiencyPrunedCoverage())
	if len(got.Members) != 1 {
		t.Fatalf("members = %#v, want 1", got.Members)
	}
	member := got.Members[0]
	if score := mustScore(t, member); score != 50 {
		t.Fatalf("Score = %v, want 50 (100 * 0.5 / 1.0 renormalized over the one available signal)", score)
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
	if !reflect.DeepEqual(member.RankingBasis, []string{RankingSignalHealthRisk}) {
		t.Fatalf("RankingBasis = %#v, want [%q]", member.RankingBasis, RankingSignalHealthRisk)
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
	got, _ := RankCohort(cohort, nil, deficiencyPrunedCoverage())
	member := got.Members[0]
	if !member.RankingComputed {
		t.Fatal("RankingComputed = false, want true (ranking ran, it just found nothing)")
	}
	if member.Score != nil {
		t.Fatalf("Score = %v, want nil", *member.Score)
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
	facts := []CanonicalFact{healthFact("SCORED", "high")}
	coverage := deficiencyPrunedCoverage()
	got, _ := RankCohort(cohort, facts, coverage)
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
	got, event := RankCohort(cohort, facts, availableCoverage())

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
	got, _ := RankCohort(cohort, facts, Coverage{})
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
			value, labels, _, available := investmentMixSignal([]CanonicalFact{investmentFact("A", tc.themes, tc.bugfix)}, Coverage{})
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
	_, labels, _, available := investmentMixSignal([]CanonicalFact{investmentFactWithPrior("A", current, priorTowardOperational, 0)}, Coverage{})
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
	_, labels, _, available = investmentMixSignal([]CanonicalFact{investmentFactWithPrior("B", currentTowardFeature, priorFeatureLow, 0)}, Coverage{})
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
	_, labels, _, available = investmentMixSignal([]CanonicalFact{investmentFactWithPrior("C", currentTowardQuality, priorQualityLow, 0)}, Coverage{})
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
	_, labels, _, available := investmentMixSignal([]CanonicalFact{investmentFactWithPrior("A", current, prior, 0)}, Coverage{})
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
	// Team A is the clear LOW end of a wide spread -> low pressure.
	wideCohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("A"), rankTestMember("B"), rankTestMember("C")}}
	wideFacts := []CanonicalFact{workloadFact("A", 10), workloadFact("B", 50), workloadFact("C", 90)}
	got, _ := RankCohort(wideCohort, wideFacts, Coverage{})
	var scoreWide float64
	for _, m := range got.Members {
		if m.Subject.CanonicalID == "team:A" {
			scoreWide = mustScore(t, m)
		}
	}

	// Team A is near the MIDDLE of a narrow spread -> higher relative pressure.
	narrowCohort := &Cohort{Kind: SubjectTeam, Members: []CohortMember{rankTestMember("A"), rankTestMember("B"), rankTestMember("C")}}
	narrowFacts := []CanonicalFact{workloadFact("A", 10), workloadFact("B", 9), workloadFact("C", 11)}
	got2, _ := RankCohort(narrowCohort, narrowFacts, Coverage{})
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
	value, available := deficiencySeveritySignal([]CanonicalFact{
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
	value, available := deficiencySeveritySignal(nil, availableCoverage())
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
	if _, available := deficiencySeveritySignal(nil, Coverage{}); !available {
		t.Fatal("deficiencySeveritySignal(nil, Coverage{}) available = false, want true (no coverage entry defaults permissive)")
	}
}

// TestDeficiencySeveritySignal_TruncatedBatchWithZeroRowsIsMissing proves a
// Truncated batch does NOT get the available-zero exception: it cannot
// promise there were truly zero fired rules (one could exist past the cap).
func TestDeficiencySeveritySignal_TruncatedBatchWithZeroRowsIsMissing(t *testing.T) {
	t.Parallel()
	coverage := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:operational_deficiencies", State: SourceTruncated}}}
	if _, available := deficiencySeveritySignal(nil, coverage); available {
		t.Fatal("deficiencySeveritySignal(nil, truncated) available = true, want false")
	}
}

// TestHealthRiskSignal_UnknownSeverityIsUnavailable proves "unknown" (the
// compounding_risk_daily Enum8's own zero value) reads as a data gap, not
// a favorable low-risk 0.
func TestHealthRiskSignal_UnknownSeverityIsUnavailable(t *testing.T) {
	t.Parallel()
	if _, available := healthRiskSignal([]CanonicalFact{healthFact("A", "unknown")}, Coverage{}); available {
		t.Fatal("healthRiskSignal() available = true for severity=unknown, want false")
	}
	if value, available := healthRiskSignal([]CanonicalFact{healthFact("A", "high")}, Coverage{}); !available || value != 1.0 {
		t.Fatalf("value = %v, available = %v, want (1.0, true)", value, available)
	}
}

// TestHealthRiskSignal_PrunedBatchIsUnavailableEvenWithARow proves a
// Pruned/Unavailable batch state overrides row presence -- a batch that
// failed cannot have a trustworthy row, whatever facts happens to carry.
func TestHealthRiskSignal_PrunedBatchIsUnavailableEvenWithARow(t *testing.T) {
	t.Parallel()
	coverage := Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourcePruned}}}
	if _, available := healthRiskSignal([]CanonicalFact{healthFact("A", "high")}, coverage); available {
		t.Fatal("healthRiskSignal() available = true against a pruned batch, want false")
	}
}

// TestReadinessGapSignal_AggregatesWorstAcrossScopes proves multiple
// FactReadiness facts (readiness partitions by provider/work-scope) are
// aggregated by the WORST (lowest) coverage ratio, never an average or the
// first row.
func TestReadinessGapSignal_AggregatesWorstAcrossScopes(t *testing.T) {
	t.Parallel()
	value, available := readinessGapSignal([]CanonicalFact{
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
	days, ok := workloadWorstDays([]CanonicalFact{
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
	got, _ := RankCohort(cohort, facts, availableCoverage())
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
	// Only health and deficiency available; investment/readiness/workload
	// have no facts at all (deficiency's own available-zero exception
	// still fires since availableCoverage marks it SourceAvailable).
	facts := []CanonicalFact{healthFact("A", "high")}
	cohort := &Cohort{Kind: SubjectTeam, Rationale: "r", Members: []CohortMember{rankTestMember("A")}}
	got, _ := RankCohort(cohort, facts, availableCoverage())
	member := got.Members[0]
	mustScore(t, member)

	basisFamilies := map[string]bool{}
	for _, entry := range member.RankingBasis {
		basisFamilies[entry] = true // only family-name entries exist here (no investment_mix, so no sub-labels either)
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
		got, _ := RankCohort(cohort, facts, availableCoverage())
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
		got, _ := RankCohort(cohort, facts, availableCoverage())
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
	got, _ := RankCohort(cohort, facts, availableCoverage())
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
	got, _ := RankCohort(cohort, nil, deficiencyPrunedCoverage())
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
