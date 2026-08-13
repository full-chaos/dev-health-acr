package contextfabric

import (
	"strings"
	"testing"
)

// TestSynthesisDraftValidateAgainstBindsCHAOS3780GatedCategories is
// AC-3780-6's named test. The closed category->kind table
// (contractsv1.ContextFabricDriverCategoryRequiresClaimedFact,
// internal/contracts/v1/context_fabric_types.go) already gated health,
// workload, investment, readiness, operational_deficiency, and
// source_health before CHAOS-3780 -- that table is single-sourced and this
// test does not duplicate it. What CHAOS-3780 closes is the coverage gap:
// model_runtime_test.go's closureFixture only ever exercised "readiness".
// This proves, for every one of the six required categories, that a driver
// citing a matching ClaimedFact validates, and a driver in that category
// with no claim at all is rejected outright -- the same value-level and
// presence-level closure "readiness" already had, now proved for all six.
func TestSynthesisDraftValidateAgainstBindsCHAOS3780GatedCategories(t *testing.T) {
	t.Parallel()
	cases := []struct {
		category string
		kind     FactKind
	}{
		{"health", FactHealth},
		{"workload", FactWorkload},
		{"investment", FactInvestment},
		{"readiness", FactReadiness},
		{"operational_deficiency", FactOperationalDeficiencies},
		{"source_health", FactSourceHealth},
	}
	for _, tc := range cases {
		t.Run(tc.category, func(t *testing.T) {
			t.Parallel()
			input := validSynthesisInputFixture()
			input.Facts.Facts = []CanonicalFact{{
				Kind: tc.kind, Subject: input.Graph.Resolution.Committed[0],
				Fields:         map[string]FactValue{"field_under_test": BooleanFactValue(true)},
				EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
				Source: "ops", SourceVersion: "v1",
			}}

			draft := validSynthesisDraftFixture(input)
			draft.Drivers[0].Category = tc.category
			draft.Drivers[0].ClaimedFactIDs = []string{"claim_test_fixture_1"}
			draft.ClaimedFacts = []ClaimedFact{{
				ClaimID: "claim_test_fixture_1", Kind: tc.kind, Subject: input.Graph.Resolution.Committed[0],
				Field: "field_under_test", Value: boolScalar(true),
			}}
			if err := draft.ValidateAgainst(input); err != nil {
				t.Fatalf("ValidateAgainst() error = %v, want category %q bound to kind %q to validate", err, tc.category, tc.kind)
			}

			// A driver in this category with no claim at all must be
			// rejected -- the H4-shaped guard: silent admission of an
			// unbound fact-shaped judgment is exactly the defect class
			// §19.5.5 names.
			draftNoClaim := validSynthesisDraftFixture(input)
			draftNoClaim.Drivers[0].Category = tc.category
			draftNoClaim.Drivers[0].ClaimedFactIDs = nil
			draftNoClaim.ClaimedFacts = nil
			err := draftNoClaim.ValidateAgainst(input)
			if err == nil || !strings.Contains(err.Error(), "requires a claimed fact") {
				t.Fatalf("ValidateAgainst() error = %v, want a category-requires-claimed-fact error for %q", err, tc.category)
			}
		})
	}
}
