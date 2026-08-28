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
