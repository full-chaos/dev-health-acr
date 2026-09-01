package v1

import "testing"

// TestPlanNarrowingCardinalityStageRejectsBasesItCannotHonestlyClaim pins
// codex round 4, finding 1 (EXECUTED): a pre-read cardinality-stage record
// claiming overlap_aware_set_cover passed validation, even though groups do
// not exist until after the fact read (this package's own header). The
// pre-existing attention_rank check is exercised alongside it -- it had no
// direct unit test of its own before this one.
func TestPlanNarrowingCardinalityStageRejectsBasesItCannotHonestlyClaim(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		basis   ContextFabricNarrowingBasis
		wantErr bool
	}{
		{"canonical_id_lexical is the only honest stage-1 order", ContextFabricNarrowingBasisCanonicalIDLexical, false},
		{"attention_rank does not exist before the fact read", ContextFabricNarrowingBasisAttentionRank, true},
		{"overlap_aware_set_cover needs groups that do not exist before the fact read", ContextFabricNarrowingBasisOverlapAwareSetCover, true},
		{"largest_group_round_robin also needs groups that do not exist yet", ContextFabricNarrowingBasisLargestGroupRoundRobin, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := ContextFabricPlanNarrowing{
				Stage: ContextFabricPlanNarrowingCardinality, Basis: tc.basis, Before: 5, After: 5,
			}
			err := n.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error for basis %q at the cardinality stage", tc.basis)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for basis %q at the cardinality stage", err, tc.basis)
			}
		})
	}
}

// TestPlanNarrowingSynthesisInputStageAcceptsTheSetCover: the SAME basis
// that the cardinality stage must reject is exactly what a later,
// post-fact-read stage is expected to report -- proving the fix is scoped
// to the stage, not a blanket rejection of the new enum member.
func TestPlanNarrowingSynthesisInputStageAcceptsTheSetCover(t *testing.T) {
	t.Parallel()
	n := ContextFabricPlanNarrowing{
		Stage: ContextFabricPlanNarrowingSynthesisInput, Basis: ContextFabricNarrowingBasisOverlapAwareSetCover, Before: 5, After: 3,
	}
	if err := n.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil -- the set cover is exactly what a post-fact-read stage should report", err)
	}
}

// TestAnswerPlanBudgetNarrowingBasisIsStage1Only pins codex round 4,
// finding 1's second half (EXECUTED): ContextFabricAnswerPlanBudget's own
// NarrowingBasis field records ONLY what stage 1 declared (its own doc
// comment), and stage 1 is structurally flat, so canonical_id_lexical is
// the only value it can ever honestly hold -- yet the validator accepted
// any closed-vocabulary member.
func TestAnswerPlanBudgetNarrowingBasisIsStage1Only(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		basis   ContextFabricNarrowingBasis
		wantErr bool
	}{
		{"empty is the unset default", "", false},
		{"canonical_id_lexical is stage 1's own order", ContextFabricNarrowingBasisCanonicalIDLexical, false},
		{"overlap_aware_set_cover is a later stage's order, not stage 1's", ContextFabricNarrowingBasisOverlapAwareSetCover, true},
		{"largest_group_round_robin is also a later stage's order", ContextFabricNarrowingBasisLargestGroupRoundRobin, true},
		{"attention_rank is also a later stage's order", ContextFabricNarrowingBasisAttentionRank, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := ContextFabricAnswerPlanBudget{MaxItems: 100, NarrowingBasis: tc.basis}
			err := b.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error for budget NarrowingBasis %q", tc.basis)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for budget NarrowingBasis %q", err, tc.basis)
			}
		})
	}
}
