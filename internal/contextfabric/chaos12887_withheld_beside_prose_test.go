package contextfabric

import (
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12887 (lead): the v31 prompt gives the model a detail table's row count
// and never says the table is shown, because the answer-size cut runs after
// synthesis and can still withhold it. When it does, the server's own
// sentence (CHAOS-12582) says so beside the model's prose, which stays as
// written: the page never shows a count with no table and no word about it.
func TestAServeTimeWithholdAddsItsSentenceBesideTheModelProse(t *testing.T) {
	t.Parallel()
	result := repositoryFlowResult(repositoryFlowLinks())
	prose := "The breakdown by repository holds 6 rows; its values are not restated here."
	result.Limitations = []string{prose}
	declared := len(result.ClaimedFacts[0].Rows)
	cut, _, rules, applied := applyFactRowTruncation(result, 5, declared)
	if !applied || rules.RepositoryFlowWithheld != 1 {
		t.Fatalf("applied = %t, rules = %+v; want the repository flow table withheld whole", applied, rules)
	}
	// The table itself is gone, not only counted as withheld (Codex r1 P3).
	if claim := cut.ClaimedFacts[0]; claim.Rows != nil || claim.Table != nil {
		t.Fatalf("rows = %d, table = %v after the cut; want the table withheld whole", len(claim.Rows), claim.Table)
	}
	if !slices.Contains(cut.Limitations, prose) {
		t.Fatalf("limitations %q dropped the model's prose", cut.Limitations)
	}
	if !slices.Contains(cut.Limitations, contractsv1.ContextFabricRepositoryFlowWithheldLimitation) {
		t.Fatalf("limitations %q do not say the table was left out", cut.Limitations)
	}
}
