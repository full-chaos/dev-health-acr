package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12951: a claim whose value is null and that carries a served table
// is a table claim. Its rows are the observation; "null" is not.
func TestComposeCurrentStateSkipsNullValueOfATableClaim(t *testing.T) {
	t.Parallel()
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:linear:CHAOS", Label: "Fullchaos"}
	table := ClaimedFact{
		ClaimID: "c1", Kind: FactInvestment, Field: "subcategory_breakdown", Subject: subject,
		Value: ScalarValue{Null: true},
		Rows:  []contractsv1.ContextFabricClaimedFactRow{{Fields: map[string]ScalarValue{"subcategory": {String: ptrString("feature_delivery.customer")}}}},
	}
	ten := int64(10)
	scalar := ClaimedFact{
		ClaimID: "c2", Kind: FactInvestment, Field: "owned_repository_count", Subject: subject,
		Value: ScalarValue{Integer: &ten},
	}
	got := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{table, scalar}})
	if strings.Contains(got, "null") {
		t.Fatalf("CurrentState lists a table claim as null: %q", got)
	}
	if !strings.Contains(got, "10 (Fullchaos)") {
		t.Fatalf("CurrentState lost the scalar claim: %q", got)
	}
	only := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{table}})
	if strings.Contains(only, "null") {
		t.Fatalf("CurrentState lists a lone table claim as null: %q", only)
	}
	bare := table
	bare.Rows = nil
	if got := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{bare}}); !strings.Contains(got, "null") {
		t.Fatalf("a null claim with no table must keep stating null: %q", got)
	}
}

func TestComposeSynthesisResultCurrentStateStatesRowsOfADetailTableClaim(t *testing.T) {
	t.Parallel()
	fact := detailFact(true)
	input := SynthesisInput{Facts: CanonicalFactBundle{Facts: []CanonicalFact{fact}}}
	draft := SynthesisDraft{
		Status: InvestigationPartial,
		ClaimedFacts: []ClaimedFact{{ClaimID: "claim_detail", Kind: FactInvestment, Subject: fact.Subject,
			Field: "subcategory_breakdown", Value: ScalarValue{Null: true}, FactID: fact.ID}},
	}
	result, err := RuntimeAnswerSynthesizer{}.composeSynthesisResult(context.Background(), storage.Principal{OrgID: "org_1"}, input, draft, validModelReceiptFixture(ModelOperationSynthesize), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ClaimedFacts) != 1 || len(result.ClaimedFacts[0].Rows) == 0 {
		t.Fatalf("claim carries no table: %+v", result.ClaimedFacts)
	}
	if strings.Contains(result.CurrentState, "null") || !strings.Contains(result.CurrentState, "3 rows") {
		t.Fatalf("CurrentState = %q, want the table's rows stated and no null", result.CurrentState)
	}
}
