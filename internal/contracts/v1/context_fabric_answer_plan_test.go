package v1

import "testing"

// TestPlanNarrowingStageBasisTotality is the VALIDATION TOTALITY sweep chris
// ordered: every stage x basis combination (3 stages x 4 bases = 12 cells),
// each with an EXPLICIT accept/reject verdict, matching what live code can
// actually produce -- not argued in the abstract.
//
// Live-code source of truth for each cell, traced through the engine rather
// than assumed:
//   - Stage 1 (cardinality) hardcodes canonical_id_lexical unconditionally
//     (chaos4636_answer_plan.go's planBudget); nothing else runs there.
//   - Stage 2 (synthesis_input) runs AFTER the fact read but BEFORE
//     RankCohort (engine.go calls RankCohort strictly after the stage-2
//     narrowing block), so attention_rank cannot be live there either --
//     found by writing this sweep, not by review (codex round 4's gap
//     covered only the cardinality stage). Stage 2 reports
//     canonical_id_lexical (flat cohort), overlap_aware_set_cover (grouped,
//     within the guard) or largest_group_round_robin (grouped, beyond it).
//   - Stage 3 (assembled_result) is where RankCohort's output could in
//     principle be reported as attention_rank -- this enum member's own
//     doc comment says "available ONLY at stage 3" -- though no CURRENT
//     code path actually emits it there; it is accepted as the designed,
//     forward-compatible home for a basis nothing yet produces, not
//     rejected as unreachable. Stage 3 also reports canonical_id_lexical
//     (flat) and either grouped basis (see chaos4636_budget_stage3.go).
func TestPlanNarrowingStageBasisTotality(t *testing.T) {
	t.Parallel()
	stages := []ContextFabricPlanNarrowingStage{
		ContextFabricPlanNarrowingCardinality,
		ContextFabricPlanNarrowingSynthesisInput,
		ContextFabricPlanNarrowingAssembledResult,
	}
	bases := []ContextFabricNarrowingBasis{
		ContextFabricNarrowingBasisCanonicalIDLexical,
		ContextFabricNarrowingBasisLargestGroupRoundRobin,
		ContextFabricNarrowingBasisAttentionRank,
		ContextFabricNarrowingBasisOverlapAwareSetCover,
	}
	// acceptedAt[basis] = the set of stages that basis is honest at.
	acceptedAt := map[ContextFabricNarrowingBasis]map[ContextFabricPlanNarrowingStage]bool{
		ContextFabricNarrowingBasisCanonicalIDLexical: {
			ContextFabricPlanNarrowingCardinality:     true,
			ContextFabricPlanNarrowingSynthesisInput:  true,
			ContextFabricPlanNarrowingAssembledResult: true,
		},
		ContextFabricNarrowingBasisLargestGroupRoundRobin: {
			ContextFabricPlanNarrowingCardinality:     false,
			ContextFabricPlanNarrowingSynthesisInput:  true,
			ContextFabricPlanNarrowingAssembledResult: true,
		},
		ContextFabricNarrowingBasisAttentionRank: {
			ContextFabricPlanNarrowingCardinality:     false,
			ContextFabricPlanNarrowingSynthesisInput:  false,
			ContextFabricPlanNarrowingAssembledResult: true,
		},
		ContextFabricNarrowingBasisOverlapAwareSetCover: {
			ContextFabricPlanNarrowingCardinality:     false,
			ContextFabricPlanNarrowingSynthesisInput:  true,
			ContextFabricPlanNarrowingAssembledResult: true,
		},
	}
	for _, stage := range stages {
		for _, basis := range bases {
			wantAccept := acceptedAt[basis][stage]
			t.Run(string(stage)+"_x_"+string(basis), func(t *testing.T) {
				n := ContextFabricPlanNarrowing{Stage: stage, Basis: basis, Before: 5, After: 3}
				err := n.Validate()
				if wantAccept && err != nil {
					t.Fatalf("Validate() = %v, want ACCEPT for stage=%q basis=%q", err, stage, basis)
				}
				if !wantAccept && err == nil {
					t.Fatalf("Validate() = nil, want REJECT for stage=%q basis=%q -- this combination is not something live code can produce", stage, basis)
				}
			})
		}
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
