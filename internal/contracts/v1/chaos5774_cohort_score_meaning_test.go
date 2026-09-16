package v1

import "testing"

// CHAOS-5774: ScoreMeaning is required exactly when a cohort was ranked
// (mirrors Outcome's own required-on-write shape) -- this file pins the
// rejection direction, which the sibling "a valid ScoreMeaning passes"
// coverage elsewhere in this package never exercised on its own.

func rankedCohortFixture(scoreMeaning ContextFabricCohortScoreMeaning) ContextFabricCohort {
	return ContextFabricCohort{
		Kind: ContextFabricSubjectTeam,
		Members: []ContextFabricCohortMember{{
			Subject:          ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:X", Label: "X"},
			Rank:             1,
			InclusionReasons: []string{"matched"},
			RankingComputed:  true,
			AttentionRank:    1,
			DataCompleteness: ContextFabricCohortDataDegraded,
			Outcome:          ContextFabricCohortOutcomeInsufficientEvidence,
			MissingSignals:   []string{"investment_mix"},
		}},
		Rationale:    "fixture",
		Complete:     true,
		Truncated:    false,
		ScoreMeaning: scoreMeaning,
	}
}

func TestCohortValidateAcceptsARankedCohortWithScoreMeaning(t *testing.T) {
	t.Parallel()
	cohort := rankedCohortFixture(ContextFabricCohortScoreMeaningAttention)
	if err := cohort.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a ranked cohort carrying a valid score_meaning", err)
	}
}

// TestCohortValidateRejectsARankedCohortMissingScoreMeaning is the direct
// pin for the required-on-write half: a cohort with a ranked member (RankCohort
// always mints ScoreMeaning) but an empty ScoreMeaning must be refused, or a
// producer that forgot to mint it would ship silently.
func TestCohortValidateRejectsARankedCohortMissingScoreMeaning(t *testing.T) {
	t.Parallel()
	cohort := rankedCohortFixture("")
	err := cohort.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for a ranked cohort with no score_meaning")
	}
}

// TestCohortValidateRejectsAnUnrecognizedScoreMeaning proves the value is
// checked against the closed vocabulary, not merely for non-emptiness.
func TestCohortValidateRejectsAnUnrecognizedScoreMeaning(t *testing.T) {
	t.Parallel()
	cohort := rankedCohortFixture(ContextFabricCohortScoreMeaning("performance"))
	err := cohort.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for an out-of-vocabulary score_meaning")
	}
}

// TestCohortValidateRejectsScoreMeaningOnAnUnrankedCohort is the mirror:
// a cohort that never ranked anything must not carry a meaning to state.
func TestCohortValidateRejectsScoreMeaningOnAnUnrankedCohort(t *testing.T) {
	t.Parallel()
	cohort := ContextFabricCohort{
		Kind:         ContextFabricSubjectTeam,
		Members:      []ContextFabricCohortMember{},
		Rationale:    "fixture",
		Complete:     true,
		Truncated:    false,
		ScoreMeaning: ContextFabricCohortScoreMeaningAttention,
	}
	err := cohort.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for score_meaning set with no ranked member")
	}
}

// TestCohortValidateRejectsJudgmentMismatchWithoutScoreMeaning pins
// JudgmentMismatch's own "never set without a meaning to mismatch against"
// invariant.
func TestCohortValidateRejectsJudgmentMismatchWithoutScoreMeaning(t *testing.T) {
	t.Parallel()
	cohort := ContextFabricCohort{
		Kind: ContextFabricSubjectTeam, Members: []ContextFabricCohortMember{},
		Rationale: "fixture", Complete: true, Truncated: false,
		JudgmentMismatch: true,
	}
	err := cohort.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for judgment_mismatch=true with no score_meaning")
	}
}
