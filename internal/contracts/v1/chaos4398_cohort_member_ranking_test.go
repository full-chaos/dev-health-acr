package v1

import (
	"testing"
	"time"
)

func baseRankedCohortMember() ContextFabricCohortMember {
	score := 42.0
	return ContextFabricCohortMember{
		Subject:          ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:             1,
		InclusionReasons: []string{"matched"},
		RankingComputed:  true,
		Score:            &score,
		AttentionRank:    1,
		RankingBasis:     []string{"health.compounding_risk"},
		DataCompleteness: ContextFabricCohortDataDegraded,
		Outcome:          ContextFabricCohortOutcomeProvisional,
		MissingSignals:   []string{"investment_mix", "operational_deficiencies.severity", "readiness.coverage_gap", "workload.forecast_pressure"},
	}
}

// TestContextFabricCohortMember_ScoreRejectsNonFinite is the codex round-1
// finding: `< 0`/`> 100` alone both evaluate false for NaN (and +/-Inf),
// so a non-finite Score must be explicitly rejected.
func TestContextFabricCohortMember_ScoreRejectsNonFinite(t *testing.T) {
	t.Parallel()
	cases := []float64{
		nan(), posInf(), negInf(),
	}
	for _, value := range cases {
		member := baseRankedCohortMember()
		member.Score = &value
		if err := member.Validate(); err == nil {
			t.Fatalf("Validate() = nil for non-finite score %v, want an error", value)
		}
	}
}

func nan() float64                { var zero float64; return zero / zero }
func posInf() float64             { var zero float64; return 1 / zero }
func negInf() float64             { var zero float64; return -1 / zero }
func floatPtr(v float64) *float64 { return &v }

// TestContextFabricCohortMember_RankingBasisRejectsOutOfVocabulary proves
// RankingBasis entries are checked against the closed vocabulary, not just
// bounded by length/count -- a stray value or model-authored prose must
// fail validation.
func TestContextFabricCohortMember_RankingBasisRejectsOutOfVocabulary(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.RankingBasis = []string{"this team is clearly struggling"}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for an out-of-vocabulary ranking basis entry, want an error")
	}
}

// TestContextFabricCohortMember_RankingBasisAcceptsEveryClosedLabel proves
// every family name and driver label RankCohort can actually emit
// (internal/contextfabric/cohort_ranking.go) is accepted.
func TestContextFabricCohortMember_RankingBasisAcceptsEveryClosedLabel(t *testing.T) {
	t.Parallel()
	// RankingBasis's sub-label tail must match the investment_mix driver's
	// own ThresholdLabels exactly (validateDrivers cross-checks both
	// directions) -- toward_operational/toward_feature are excluded here
	// since RankCohort's own investmentMixSignal fires at most ONE of the
	// three mutually exclusive mix_shift_* labels per member; their
	// acceptance is covered by TestRankCohort_MixShiftDirectionLabels.
	labels := []string{
		"investment_mix", "health.compounding_risk", "operational_deficiencies.severity",
		"readiness.coverage_gap", "workload.forecast_pressure",
		"investment_mix.reactive_share_high", "investment_mix.deliberate_share_low",
		"investment_mix.mix_concentrated", "investment_mix.mix_shift_other",
	}
	score := 60.0
	member := baseRankedCohortMember()
	member.Score = &score
	member.DataCompleteness = ContextFabricCohortDataComplete
	member.Outcome = ContextFabricCohortOutcomeQualified // all 5 families present
	member.MissingSignals = nil
	member.RankingBasis = labels
	// investment_mix claims ALL 4 threshold labels -- its own Value must
	// equal the exact sum of their sub-weights (codex R3 finding 3):
	// 0.35+0.30+0.15+0.20 == 1.0. WeightContributed values below are each
	// 100*Weight*Value/availableWeight (availableWeight==100, all 5
	// families present): 30, 10, 10, 6, 4 -- summing to Score (60).
	member.Drivers = []ContextFabricCohortMemberDriver{
		// At most 4 threshold labels can co-occur in practice
		// (RankCohort's own investmentMixSignal fires at most one of the
		// three mutually exclusive mix_shift_* labels) -- the bound is 4,
		// not 6, so this uses only the realistic subset.
		{Signal: "investment_mix", Value: 1.0, Weight: 30, WeightContributed: 30, Window: ContextFabricCohortMemberDriverWindowCurrentVsPrior, ThresholdLabels: []string{
			"investment_mix.reactive_share_high", "investment_mix.deliberate_share_low",
			"investment_mix.mix_concentrated", "investment_mix.mix_shift_other",
		}, Concentration: floatPtr(0.6), ConcentrationMethod: "max_share", SourceClaimedFactIDs: []string{"claim_test_investment_mix"}},
		{Signal: "health.compounding_risk", Value: 0.4, Weight: 25, WeightContributed: 10, Window: ContextFabricCohortMemberDriverWindowCurrent, SourceClaimedFactIDs: []string{"claim_test_health"}},
		{Signal: "operational_deficiencies.severity", Value: 0.5, Weight: 20, WeightContributed: 10, Window: ContextFabricCohortMemberDriverWindowCurrent, SourceClaimedFactIDs: []string{"claim_test_deficiency"}},
		{Signal: "readiness.coverage_gap", Value: 0.4, Weight: 15, WeightContributed: 6, Window: ContextFabricCohortMemberDriverWindowCurrent, SourceClaimedFactIDs: []string{"claim_test_readiness"}},
		{Signal: "workload.forecast_pressure", Value: 0.4, Weight: 10, WeightContributed: 4, Window: ContextFabricCohortMemberDriverWindowCurrent, SourceClaimedFactIDs: []string{"claim_test_workload"}},
	}
	if err := member.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for the full closed vocabulary", err)
	}
}

// TestContextFabricCohortMember_NilScoreValid proves the §5b zero-signal
// case (RankingComputed=true, Score=nil) is a valid, non-error shape.
func TestContextFabricCohortMember_NilScoreValid(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.Score = nil
	member.RankingBasis = nil
	member.Outcome = ContextFabricCohortOutcomeNotApplicable
	member.MissingSignals = []string{"investment_mix", "health.compounding_risk", "operational_deficiencies.severity", "readiness.coverage_gap", "workload.forecast_pressure"}
	if err := member.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a zero-signal (nil Score) member", err)
	}
}

// TestContextFabricCohortMember_RankingFieldsWithoutComputedRejected proves
// any ranking field set while RankingComputed is false is rejected -- the
// v1-compatibility case (a pre-CHAOS-4398 result) must have ALL of them
// absent together.
func TestContextFabricCohortMember_RankingFieldsWithoutComputedRejected(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.RankingComputed = false
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for ranking fields set without RankingComputed, want an error")
	}
}

// baseDriverForBasis returns a single valid driver for
// baseRankedCohortMember's own RankingBasis ("health.compounding_risk")
// whose WeightContributed exactly equals its Score (42.0). As the sole
// driver, availableWeight==Weight (25), so
// 100*Weight*Value/availableWeight == 100*Value -- Value=0.42 gives
// WeightContributed=42, matching both the aggregate sum AND (codex R1
// finding 2) the per-driver formula check.
func baseDriverForBasis() ContextFabricCohortMemberDriver {
	return ContextFabricCohortMemberDriver{
		Signal: "health.compounding_risk", Value: 0.42, Weight: 25, WeightContributed: 42.0,
		Window: ContextFabricCohortMemberDriverWindowCurrent,
		// SourceClaimedFactIDs (CHAOS-4398 PR3b): a placeholder citation --
		// this file only exercises ContextFabricCohortMember.Validate() and
		// ContextFabricCohortMemberDriver.Validate() in isolation, neither
		// of which has access to a result's ClaimedFacts to cross-reference
		// against (see validateCohortDriverClaimedFacts, tested separately
		// against a full ContextFabricInvestigationResult), so any
		// non-empty ID satisfies this file's shape-only check.
		SourceClaimedFactIDs: []string{"claim_test_health_default"},
	}
}

// TestContextFabricCohortMemberDriver_SignalRejectsOutOfVocabulary proves a
// driver naming a signal outside the five RankCohort families is rejected.
func TestContextFabricCohortMemberDriver_SignalRejectsOutOfVocabulary(t *testing.T) {
	t.Parallel()
	d := baseDriverForBasis()
	d.Signal = "investment_mix.reactive_share_high" // a RankingBasis sub-label, not a top-level family
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver naming a sub-label as its own signal, want an error")
	}
}

// TestContextFabricCohortMemberDriver_WeightMustMatchSignal proves a
// driver's Weight is checked against its signal's own fixed formula
// weight, not left to a bare length/range bound -- a formula-weight typo
// would otherwise silently corrupt every consumer's read of "how much did
// this family matter".
func TestContextFabricCohortMemberDriver_WeightMustMatchSignal(t *testing.T) {
	t.Parallel()
	d := baseDriverForBasis()
	d.Weight = 26 // health.compounding_risk's real weight is 25
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver weight that does not match its signal, want an error")
	}
}

// TestContextFabricCohortMemberDriver_ValueRejectsOutOfRange proves Value
// (the family's own [0,1] contribution) is bounds-checked, including
// non-finite values.
func TestContextFabricCohortMemberDriver_ValueRejectsOutOfRange(t *testing.T) {
	t.Parallel()
	for _, value := range []float64{-0.01, 1.01, nan(), posInf(), negInf()} {
		d := baseDriverForBasis()
		d.Value = value
		if err := d.Validate(); err == nil {
			t.Fatalf("Validate() = nil for driver value %v, want an error", value)
		}
	}
}

// TestContextFabricCohortMemberDriver_WindowRejectsOutOfVocabulary proves
// Window is a closed two-value vocabulary.
func TestContextFabricCohortMemberDriver_WindowRejectsOutOfVocabulary(t *testing.T) {
	t.Parallel()
	d := baseDriverForBasis()
	d.Window = "last_quarter"
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for an out-of-vocabulary window, want an error")
	}
}

// TestContextFabricCohortMemberDriver_ThresholdLabelMustBelongToSignal
// proves a driver cannot cite another family's threshold label -- e.g. an
// investment_mix driver claiming a health-risk label would misattribute
// why the family fired.
func TestContextFabricCohortMemberDriver_ThresholdLabelMustBelongToSignal(t *testing.T) {
	t.Parallel()
	d := baseDriverForBasis() // signal: health.compounding_risk
	d.ThresholdLabels = []string{"investment_mix.mix_concentrated"}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for a threshold label belonging to a different signal, want an error")
	}
}

// TestContextFabricCohortMember_DriversMustSumToScore is CHAOS-4398 PR2's
// central traceability invariant: a member's Drivers must reconstruct
// Score exactly (within float64 rounding) via Sum(WeightContributed) --
// this is what makes "no narration a human can't trace to a number" an
// enforced contract property.
func TestContextFabricCohortMember_DriversMustSumToScore(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember() // Score 42.0
	d := baseDriverForBasis()
	d.WeightContributed = 41.0 // does not sum to Score
	member.Drivers = []ContextFabricCohortMemberDriver{d}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for drivers that do not sum to score, want an error")
	}
}

// TestContextFabricCohortMember_DriverSignalNotInRankingBasisRejected
// proves a driver naming a family RankingBasis never listed is rejected --
// the two must describe the exact same set of available families.
func TestContextFabricCohortMember_DriverSignalNotInRankingBasisRejected(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember() // RankingBasis: ["health.compounding_risk"]
	member.Drivers = []ContextFabricCohortMemberDriver{
		{Signal: "readiness.coverage_gap", Value: 1.0, Weight: 15, WeightContributed: 42.0, Window: ContextFabricCohortMemberDriverWindowCurrent},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver naming a family absent from ranking_basis, want an error")
	}
}

// TestContextFabricCohortMember_RankingBasisFamilyWithoutDriverRejectedOnWrite
// proves the write path requires EVERY ranking_basis family to have a
// matching driver -- a partial Drivers set could only come from a broken
// producer on a freshly written result.
func TestContextFabricCohortMember_RankingBasisFamilyWithoutDriverRejectedOnWrite(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember() // RankingBasis names health.compounding_risk, no Drivers set
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for ranking_basis naming a family with no matching driver on the write path, want an error")
	}
}

// TestContextFabricCohortMember_DriversValid proves a correctly-shaped
// driver set (one entry, matching RankingBasis, summing to Score) is
// accepted.
func TestContextFabricCohortMember_DriversValid(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.Drivers = []ContextFabricCohortMemberDriver{baseDriverForBasis()}
	if err := member.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a valid drivers set", err)
	}
}

// TestContextFabricCohortMember_NilScoreRejectsNonEmptyDrivers proves the
// §5b zero-signal shape (nil Score) cannot carry any Drivers either --
// mirroring the existing empty-RankingBasis rule.
func TestContextFabricCohortMember_NilScoreRejectsNonEmptyDrivers(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.Score = nil
	member.RankingBasis = nil
	member.Drivers = []ContextFabricCohortMemberDriver{baseDriverForBasis()}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a nil-Score member carrying Drivers, want an error")
	}
}

// TestContextFabricCohortMember_PR1EraRowWithoutDriversStaysReadable is
// the backward-compatibility case: a row persisted before PR2 shipped has
// RankingBasis populated but Drivers entirely absent (the field did not
// exist yet). validateStored (the read/legacy path) must still accept it
// -- only the write path requires the correspondence.
func TestContextFabricCohortMember_PR1EraRowWithoutDriversStaysReadable(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember() // RankingBasis set, Drivers nil -- exactly PR1's shape
	if err := member.validateStored(); err != nil {
		t.Fatalf("validateStored() = %v, want nil for a PR1-era row with no drivers", err)
	}
	// The write path draws the opposite conclusion for the identical row --
	// proves the two paths are genuinely different, not both lenient.
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for the same PR1-era row on the write path, want an error")
	}
}

// ---------------------------------------------------------------------
// Codex R1 (dev-health-acr #319) regression tests -- 5 confirmed findings
// ---------------------------------------------------------------------

// TestContextFabricCohortMember_ScoreWithoutRankingBasisStillRequiresDrivers
// is codex R1 finding 1: the write path must reject a missing Drivers
// array whenever Score is present, NOT only when RankingBasis happens to
// also be non-empty -- RankCohort's real output always ties the two
// together, but the validator must not rely on a caller reproducing that
// coincidence.
func TestContextFabricCohortMember_ScoreWithoutRankingBasisStillRequiresDrivers(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.RankingBasis = nil // Score stays non-nil; only RankingBasis is (incorrectly) empty
	member.Drivers = nil
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a scored member with no ranking_basis and no drivers, want an error")
	}
}

// TestContextFabricCohortMemberDriver_WeightContributedMustMatchFormula is
// codex R1 finding 2: the aggregate Sum(WeightContributed)==Score check
// alone lets a driver with Value=0 (should contribute nothing) claim a
// nonzero WeightContributed, as long as some OTHER driver's error
// compensates. Each driver's own WeightContributed must equal
// 100*Weight*Value/availableWeight.
func TestContextFabricCohortMemberDriver_WeightContributedMustMatchFormula(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember() // Score 42.0, RankingBasis: ["health.compounding_risk"]
	member.Drivers = []ContextFabricCohortMemberDriver{
		{Signal: "health.compounding_risk", Value: 0, Weight: 25, WeightContributed: 42.0, Window: ContextFabricCohortMemberDriverWindowCurrent},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver whose Value=0 but WeightContributed=42 (sums to Score by coincidence), want an error")
	}
}

// TestContextFabricCohortMember_ThresholdLabelMustBeClaimedInRankingBasis
// is codex R1 finding 3: a driver's ThresholdLabels were checked for
// vocabulary and signal-prefix, but never cross-checked against
// RankingBasis's own sub-label entries. A driver claiming a same-signal
// label RankingBasis never listed must be rejected.
func TestContextFabricCohortMember_ThresholdLabelMustBeClaimedInRankingBasis(t *testing.T) {
	t.Parallel()
	score := 12.6
	member := ContextFabricCohortMember{
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:    1, InclusionReasons: []string{"matched"}, RankingComputed: true,
		Score: &score, AttentionRank: 1,
		RankingBasis:     []string{"investment_mix"}, // no sub-label entries at all
		DataCompleteness: ContextFabricCohortDataDegraded,
		Drivers: []ContextFabricCohortMemberDriver{
			{Signal: "investment_mix", Value: 0.42, Weight: 30, WeightContributed: 12.6, Window: ContextFabricCohortMemberDriverWindowCurrent,
				ThresholdLabels: []string{"investment_mix.mix_concentrated"}}, // claims a label ranking_basis never listed
		},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver claiming a threshold label absent from ranking_basis, want an error")
	}
}

// TestContextFabricCohortMember_RankingBasisSubLabelMustBeClaimedByADriver
// is the reverse direction of the same codex R1 finding 3: a sub-label
// present in RankingBasis but not claimed by any driver's ThresholdLabels
// must also be rejected.
func TestContextFabricCohortMember_RankingBasisSubLabelMustBeClaimedByADriver(t *testing.T) {
	t.Parallel()
	score := 12.6
	member := ContextFabricCohortMember{
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:    1, InclusionReasons: []string{"matched"}, RankingComputed: true,
		Score: &score, AttentionRank: 1,
		RankingBasis:     []string{"investment_mix", "investment_mix.mix_concentrated"},
		DataCompleteness: ContextFabricCohortDataDegraded,
		Drivers: []ContextFabricCohortMemberDriver{
			{Signal: "investment_mix", Value: 0.42, Weight: 30, WeightContributed: 12.6, Window: ContextFabricCohortMemberDriverWindowCurrent}, // no ThresholdLabels
		},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a ranking_basis threshold label no driver claims, want an error")
	}
}

// TestContextFabricCohortMemberDriver_CurrentVsPriorOnlyValidForInvestmentMix
// is codex R1 finding 4: only investment_mix's mix-shift sub-signal makes
// a real prior-window comparison (cohort_ranking.go's investmentMixSignal)
// -- every other signal's Window must be "current".
func TestContextFabricCohortMemberDriver_CurrentVsPriorOnlyValidForInvestmentMix(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember()
	member.Drivers = []ContextFabricCohortMemberDriver{
		{Signal: "health.compounding_risk", Value: 0.42, Weight: 25, WeightContributed: 42.0, Window: ContextFabricCohortMemberDriverWindowCurrentVsPrior},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a non-investment_mix driver claiming current_vs_prior, want an error")
	}
}

// TestContextFabricCohortMember_MultiDriverCompensatingErrorsRejected is a
// codex R2 coverage-gap request: with TWO drivers, a naive AGGREGATE-only
// check (Sum(WeightContributed)==Score) can be satisfied even when EVERY
// individual driver's own WeightContributed is wrong, as long as the
// errors happen to cancel out. availableWeight=40 (25+15); the true
// per-driver values are 31.25 (health, value=0.5) and 18.75 (readiness,
// value=0.5), summing to 50 -- this test instead claims 25/25 (each off
// by 6.25 in opposite directions, same aggregate sum), which the R1
// per-driver formula check (not just the aggregate one) must reject.
func TestContextFabricCohortMember_MultiDriverCompensatingErrorsRejected(t *testing.T) {
	t.Parallel()
	score := 50.0
	member := ContextFabricCohortMember{
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:    1, InclusionReasons: []string{"matched"}, RankingComputed: true,
		Score: &score, AttentionRank: 1,
		RankingBasis:     []string{"health.compounding_risk", "readiness.coverage_gap"},
		DataCompleteness: ContextFabricCohortDataDegraded, // 2 drivers -> degraded, per codex R3 finding 4
		Drivers: []ContextFabricCohortMemberDriver{
			{Signal: "health.compounding_risk", Value: 0.5, Weight: 25, WeightContributed: 25, Window: ContextFabricCohortMemberDriverWindowCurrent}, // true value 31.25
			{Signal: "readiness.coverage_gap", Value: 0.5, Weight: 15, WeightContributed: 25, Window: ContextFabricCohortMemberDriverWindowCurrent},  // true value 18.75
		},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for two drivers whose individually-wrong weight_contributed values happen to sum correctly, want an error")
	}
}

// ---------------------------------------------------------------------
// Codex R3 (dev-health-acr #319) regression tests -- fixed per R4 ruling
// ---------------------------------------------------------------------

// investmentMixDriverForLabels builds a valid investment_mix driver whose
// Value is the exact sum of the given threshold labels' sub-weights (the
// invariant codex R3 finding 3 requires), sole driver so
// availableWeight==Weight (30) and WeightContributed==100*Value.
func investmentMixDriverForLabels(labels ...string) ContextFabricCohortMemberDriver {
	var value float64
	for _, label := range labels {
		value += contextFabricInvestmentMixSubWeights[label]
	}
	return ContextFabricCohortMemberDriver{
		Signal: "investment_mix", Value: value, Weight: 30, WeightContributed: 100 * value,
		Window: ContextFabricCohortMemberDriverWindowCurrent, ThresholdLabels: labels,
		Concentration: floatPtr(0.5), ConcentrationMethod: "max_share",
		// SourceClaimedFactIDs (CHAOS-4398 PR3b): placeholder citation, same
		// reasoning as baseDriverForBasis' own comment.
		SourceClaimedFactIDs: []string{"claim_test_investment_mix_default"},
	}
}

// TestContextFabricCohortMemberDriver_TwoMixShiftLabelsRejected is codex R3
// finding 1 (part A): investmentMixSignal fires at most ONE of the three
// mutually-exclusive mix_shift_* labels.
func TestContextFabricCohortMemberDriver_TwoMixShiftLabelsRejected(t *testing.T) {
	t.Parallel()
	score := 100.0
	driver := investmentMixDriverForLabels("investment_mix.mix_shift_toward_operational", "investment_mix.mix_shift_toward_feature")
	driver.Window = ContextFabricCohortMemberDriverWindowCurrentVsPrior
	member := ContextFabricCohortMember{
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:    1, InclusionReasons: []string{"matched"}, RankingComputed: true,
		Score: &score, AttentionRank: 1,
		RankingBasis:     []string{"investment_mix", "investment_mix.mix_shift_toward_operational", "investment_mix.mix_shift_toward_feature"},
		DataCompleteness: ContextFabricCohortDataDegraded,
		Drivers:          []ContextFabricCohortMemberDriver{driver},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver claiming two mutually-exclusive mix_shift labels, want an error")
	}
}

// TestContextFabricCohortMemberDriver_MixShiftLabelRequiresCurrentVsPrior
// is codex R3 finding 1 (part B): a driver claiming any mix_shift_* label
// must have Window==current_vs_prior.
func TestContextFabricCohortMemberDriver_MixShiftLabelRequiresCurrentVsPrior(t *testing.T) {
	t.Parallel()
	score := 100.0
	driver := investmentMixDriverForLabels("investment_mix.mix_shift_other")
	driver.Window = ContextFabricCohortMemberDriverWindowCurrent // wrong: should be current_vs_prior
	member := ContextFabricCohortMember{
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:    1, InclusionReasons: []string{"matched"}, RankingComputed: true,
		Score: &score, AttentionRank: 1,
		RankingBasis:     []string{"investment_mix", "investment_mix.mix_shift_other"},
		DataCompleteness: ContextFabricCohortDataDegraded,
		Drivers:          []ContextFabricCohortMemberDriver{driver},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver claiming mix_shift_other with window=current, want an error")
	}
}

// TestContextFabricCohortMemberDriver_ValueMustMatchClaimedLabelSum is
// codex R3 finding 3: an investment_mix driver's Value must equal the
// EXACT sum of the sub-weights of its own ThresholdLabels -- a driver
// claiming "reactive_share_high" fired (0.35) while reporting Value: 0
// must be rejected.
func TestContextFabricCohortMemberDriver_ValueMustMatchClaimedLabelSum(t *testing.T) {
	t.Parallel()
	score := 0.0
	driver := ContextFabricCohortMemberDriver{
		Signal: "investment_mix", Value: 0, Weight: 30, WeightContributed: 0,
		Window:          ContextFabricCohortMemberDriverWindowCurrent,
		ThresholdLabels: []string{"investment_mix.reactive_share_high"}, // claims 0.35, but Value says 0
	}
	member := ContextFabricCohortMember{
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Rank:    1, InclusionReasons: []string{"matched"}, RankingComputed: true,
		Score: &score, AttentionRank: 1,
		RankingBasis:     []string{"investment_mix", "investment_mix.reactive_share_high"},
		DataCompleteness: ContextFabricCohortDataDegraded,
		Drivers:          []ContextFabricCohortMemberDriver{driver},
	}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver claiming reactive_share_high with Value=0, want an error")
	}
}

// TestContextFabricCohortMember_DataCompletenessMustMatchDriverCount is
// codex R3 finding 4: DataCompleteness must agree with len(Drivers) --
// complete requires all 5 families, degraded requires <=2, partial
// otherwise. A "complete" member with only 1 driver must be rejected.
func TestContextFabricCohortMember_DataCompletenessMustMatchDriverCount(t *testing.T) {
	t.Parallel()
	member := baseRankedCohortMember() // 1 driver's worth of RankingBasis
	member.DataCompleteness = ContextFabricCohortDataComplete
	member.Drivers = []ContextFabricCohortMemberDriver{baseDriverForBasis()}
	if err := member.Validate(); err == nil {
		t.Fatal("Validate() = nil for data_completeness=complete with only 1 driver, want an error")
	}
}

// ---------------------------------------------------------------------
// PR3: Concentration/ConcentrationMethod
// ---------------------------------------------------------------------

// TestContextFabricCohortMemberDriver_InvestmentMixRequiresConcentration
// proves an investment_mix driver missing Concentration is rejected --
// investmentMixSignal always sets it whenever the family is available.
func TestContextFabricCohortMemberDriver_InvestmentMixRequiresConcentration(t *testing.T) {
	t.Parallel()
	d := investmentMixDriverForLabels()
	d.Concentration = nil
	d.ConcentrationMethod = ""
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for an investment_mix driver with no concentration, want an error")
	}
}

// TestContextFabricCohortMemberDriver_ConcentrationRejectsUnrecognizedMethod
// proves ConcentrationMethod is checked against a closed vocabulary.
func TestContextFabricCohortMemberDriver_ConcentrationRejectsUnrecognizedMethod(t *testing.T) {
	t.Parallel()
	d := investmentMixDriverForLabels()
	d.ConcentrationMethod = "hhi" // not yet a recognized value (CHAOS-4414, not shipped)
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for an unrecognized concentration_method, want an error")
	}
}

// TestContextFabricCohortMemberDriver_ConcentrationOnlyValidForInvestmentMix
// proves a non-investment_mix driver cannot carry Concentration.
func TestContextFabricCohortMemberDriver_ConcentrationOnlyValidForInvestmentMix(t *testing.T) {
	t.Parallel()
	d := baseDriverForBasis() // health.compounding_risk
	d.Concentration = floatPtr(0.5)
	d.ConcentrationMethod = "max_share"
	if err := d.Validate(); err == nil {
		t.Fatal("Validate() = nil for a non-investment_mix driver carrying concentration, want an error")
	}
}

// ---------------------------------------------------------------------
// CHAOS-4398 PR3b (R4-style ruling): SourceClaimedFactIDs -- shape-only on
// the driver itself (bounds/uniqueness, above); cross-reference against
// result.ClaimedFacts needs the FULL ContextFabricInvestigationResult
// (validateCohortDriverClaimedFacts, only reachable there -- a bare
// ContextFabricCohortMember.Validate() has no ClaimedFacts to check
// against).
// ---------------------------------------------------------------------

// baseCohortResultWithClaims builds a minimal, otherwise-valid
// ContextFabricInvestigationResult carrying ONE cohort member with driver
// and claims -- the fixture every test in this section starts from,
// varying only driver/claims to prove the cross-reference check.
func baseCohortResultWithClaims(driver ContextFabricCohortMemberDriver, claims []ContextFabricClaimedFact) ContextFabricInvestigationResult {
	score := 42.0
	rows := 0
	for _, claim := range claims {
		rows += len(claim.Rows)
	}
	return ContextFabricInvestigationResult{
		SchemaVersion: ContextFabricInvestigationResultSchema,
		ResultID:      "result_pr3b_xref01", RequestID: "request_pr3b_xref01",
		GeneratedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), Status: ContextFabricInvestigationComplete,
		Question: "Which team is struggling most?",
		Interpretation: ContextFabricInterpretedQuestion{
			Shape: ContextFabricShapeDiscoveredCohort, RequestedJudgment: "attention_ranking",
			TimeContext:      ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
			FactRequirements: []ContextFabricFactRequirement{{Kind: ContextFabricFactHealth}},
		},
		SubjectResolution:   ContextFabricSubjectResolution{Candidates: []ContextFabricSubjectCandidate{}, Committed: []ContextFabricSubjectRef{}},
		DirectJudgment:      "Team CHAOS is the sole ranked member.",
		DeterministicAnswer: "Team CHAOS is the sole ranked member.",
		StrongestPressures:  []string{}, Drivers: []ContextFabricDriverJudgment{},
		RemainingWork: []ContextFabricFinding{}, ReadinessGaps: []ContextFabricFinding{},
		Paths: []ContextFabricRelationshipPath{}, Conflicts: []ContextFabricFinding{},
		Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts: claims,
		Coverage:     ContextFabricCoverage{Sources: []ContextFabricSourceObservation{}},
		Versions: ContextFabricVersionSet{
			ServiceVersion: "test", ContractVersion: ContextFabricInvestigationResultSchema, Backend: "test",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
		},
		Warnings: []string{},
		Cohort: &ContextFabricCohort{
			Kind: ContextFabricSubjectTeam, Rationale: "matched by kind census",
			Members: []ContextFabricCohortMember{{
				Subject:          ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
				Rank:             1,
				InclusionReasons: []string{"matched"},
				RankingComputed:  true,
				Score:            &score,
				AttentionRank:    1,
				RankingBasis:     []string{"health.compounding_risk"},
				DataCompleteness: ContextFabricCohortDataDegraded,
				Outcome:          ContextFabricCohortOutcomeProvisional,
				MissingSignals:   []string{"investment_mix", "operational_deficiencies.severity", "readiness.coverage_gap", "workload.forecast_pressure"},
				Drivers:          []ContextFabricCohortMemberDriver{driver},
			}},
		},
		Completeness: ContextFabricAnswerCompleteness{
			TerminalStatus: ContextFabricInvestigationComplete, ClaimedFactsCount: len(claims), RowsCount: rows,
			State: ContextFabricAnswerCompletenessNotDerived,
		},
	}
}

// TestContextFabricCohortMemberDriver_SourceClaimedFactIDsMustResolve
// proves a driver citing an ID with NO matching entry in result.ClaimedFacts
// is rejected -- a dangling reference is exactly the "citation without a
// backing claim" gap this ruling closes.
func TestContextFabricCohortMemberDriver_SourceClaimedFactIDsMustResolve(t *testing.T) {
	t.Parallel()
	driver := baseDriverForBasis() // cites "claim_test_health_default", never in ClaimedFacts here
	result := baseCohortResultWithClaims(driver, nil)
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver citing an ID with no matching claim, want an error")
	}
}

// TestContextFabricCohortMemberDriver_SourceClaimedFactIDsMustMatchFactKind
// proves a driver citing a REAL claim of the WRONG FactKind is still
// rejected -- resolving is necessary but not sufficient; the resolved
// claim must be the FactKind
// contextFabricCohortMemberDriverRequiredFactKind names for that signal
// (health.compounding_risk requires kind="health", not "readiness").
func TestContextFabricCohortMemberDriver_SourceClaimedFactIDsMustMatchFactKind(t *testing.T) {
	t.Parallel()
	driver := baseDriverForBasis() // signal: health.compounding_risk, cites "claim_test_health_default"
	wrongKindClaim := ContextFabricClaimedFact{
		ClaimID: "claim_test_health_default", Kind: ContextFabricFactReadiness, // wrong kind: should be "health"
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Field:   "severity", Value: ContextFabricScalarValue{String: strPtr("high")},
	}
	result := baseCohortResultWithClaims(driver, []ContextFabricClaimedFact{wrongKindClaim})
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver citing a claim of the wrong FactKind, want an error")
	}
}

// TestContextFabricCohortMemberDriver_SourceClaimedFactIDsResolvesCleanly
// proves the positive case: a driver citing a REAL claim of the MATCHING
// FactKind passes -- the red-first pair to the two rejection tests above.
func TestContextFabricCohortMemberDriver_SourceClaimedFactIDsResolvesCleanly(t *testing.T) {
	t.Parallel()
	driver := baseDriverForBasis() // signal: health.compounding_risk, cites "claim_test_health_default"
	correctClaim := ContextFabricClaimedFact{
		ClaimID: "claim_test_health_default", Kind: ContextFabricFactHealth,
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:CHAOS", Label: "Fullchaos"},
		Field:   "severity", Value: ContextFabricScalarValue{String: strPtr("high")},
	}
	result := baseCohortResultWithClaims(driver, []ContextFabricClaimedFact{correctClaim})
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a driver citing a claim of the matching FactKind", err)
	}
}

// TestContextFabricCohortMemberDriver_SourceClaimedFactIDsMustBelongToTheSameMember
// is codex R1's finding (CHAOS-4398 PR3b): resolving and matching FactKind
// are NOT sufficient -- a driver citing a REAL, right-Kind claim minted
// for a DIFFERENT cohort member's subject must still be rejected. Without
// this, a driver citing a mix of {foreign-member health claim, anything
// else} would pass as long as ONE referenced ID happened to carry the
// required Kind, defeating "this member's own evidence."
func TestContextFabricCohortMemberDriver_SourceClaimedFactIDsMustBelongToTheSameMember(t *testing.T) {
	t.Parallel()
	driver := baseDriverForBasis() // signal: health.compounding_risk, cites "claim_test_health_default"
	foreignMemberClaim := ContextFabricClaimedFact{
		ClaimID: "claim_test_health_default", Kind: ContextFabricFactHealth, // right Kind
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}, // wrong subject -- not team:CHAOS
		Field:   "severity", Value: ContextFabricScalarValue{String: strPtr("high")},
	}
	result := baseCohortResultWithClaims(driver, []ContextFabricClaimedFact{foreignMemberClaim})
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() = nil for a driver citing another member's claimed fact, want an error")
	}
}

func strPtr(s string) *string { return &s }
