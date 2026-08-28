package v1

import "testing"

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

func nan() float64    { var zero float64; return zero / zero }
func posInf() float64 { var zero float64; return 1 / zero }
func negInf() float64 { var zero float64; return -1 / zero }

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
	labels := []string{
		"investment_mix", "health.compounding_risk", "operational_deficiencies.severity",
		"readiness.coverage_gap", "workload.forecast_pressure",
		"investment_mix.reactive_share_high", "investment_mix.deliberate_share_low",
		"investment_mix.mix_concentrated", "investment_mix.mix_shift_toward_operational",
		"investment_mix.mix_shift_toward_feature", "investment_mix.mix_shift_other",
	}
	member := baseRankedCohortMember()
	member.DataCompleteness = ContextFabricCohortDataComplete
	member.RankingBasis = labels
	// One driver per family, uniform value=0.42 so
	// Sum(WeightContributed) == Score (42.0, baseRankedCohortMember's own
	// value) exactly: weight*0.42 summed across 30+25+20+15+10==100 total
	// weight is 100*0.42==42.
	member.Drivers = []ContextFabricCohortMemberDriver{
		// At most 4 threshold labels can co-occur in practice
		// (RankCohort's own investmentMixSignal fires at most one of the
		// three mutually exclusive mix_shift_* labels) -- the bound is 4,
		// not 6, so this uses only the realistic subset.
		{Signal: "investment_mix", Value: 0.42, Weight: 30, WeightContributed: 12.6, Window: ContextFabricCohortMemberDriverWindowCurrentVsPrior, ThresholdLabels: []string{
			"investment_mix.reactive_share_high", "investment_mix.deliberate_share_low",
			"investment_mix.mix_concentrated", "investment_mix.mix_shift_other",
		}},
		{Signal: "health.compounding_risk", Value: 0.42, Weight: 25, WeightContributed: 10.5, Window: ContextFabricCohortMemberDriverWindowCurrent},
		{Signal: "operational_deficiencies.severity", Value: 0.42, Weight: 20, WeightContributed: 8.4, Window: ContextFabricCohortMemberDriverWindowCurrent},
		{Signal: "readiness.coverage_gap", Value: 0.42, Weight: 15, WeightContributed: 6.3, Window: ContextFabricCohortMemberDriverWindowCurrent},
		{Signal: "workload.forecast_pressure", Value: 0.42, Weight: 10, WeightContributed: 4.2, Window: ContextFabricCohortMemberDriverWindowCurrent},
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
// whose WeightContributed exactly equals its Score (42.0): weight 25,
// value 1.68 -> 25*1.68 == 42.
func baseDriverForBasis() ContextFabricCohortMemberDriver {
	return ContextFabricCohortMemberDriver{
		Signal: "health.compounding_risk", Value: 1.0, Weight: 25, WeightContributed: 42.0,
		Window: ContextFabricCohortMemberDriverWindowCurrent,
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
