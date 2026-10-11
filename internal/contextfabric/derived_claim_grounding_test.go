package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func derivationSubject() SubjectRef {
	return SubjectRef{Kind: SubjectProject, CanonicalID: "project_derivation", Label: "Derivation"}
}

// derivationFacts serves two facts of one subject: open=7, closed=3, a
// ratio-able pair (open_rate 0.5, total 2.0) and a non-numeric field.
func derivationFacts() []CanonicalFact {
	subject := derivationSubject()
	facts := []CanonicalFact{
		{Kind: FactReadiness, Subject: subject, Fields: map[string]FactValue{"open_items": IntegerFactValue(7), "label": StringFactValue("x"), "rate": NumberFactValue(0.5)}},
		{Kind: FactWork, Subject: subject, Fields: map[string]FactValue{"closed_items": IntegerFactValue(3), "zero": IntegerFactValue(0), "total": NumberFactValue(2)}},
	}
	AssignFactIDs(facts)
	return facts
}

func derivedClaim(facts []CanonicalFact, op contractsv1.ContextFabricDerivationOperation, value ScalarValue, inputs ...contractsv1.ContextFabricDerivationInput) ClaimedFact {
	return ClaimedFact{
		ClaimID: "claim_derived_1", Kind: FactReadiness, Subject: derivationSubject(), Field: "derived_value",
		Value: value, DerivedFrom: inputs, Operation: op,
	}
}

func intValue(v int64) ScalarValue      { return ScalarValue{Integer: &v} }
func numberValue(v float64) ScalarValue { return ScalarValue{Number: &v} }

func input(factID, field string) contractsv1.ContextFabricDerivationInput {
	return contractsv1.ContextFabricDerivationInput{FactID: factID, Field: field}
}

func TestDerivedClaimGroundsOnlyWhenEveryInputIsServedAndTheArithmeticReproduces(t *testing.T) {
	facts := derivationFacts()
	open, closed := facts[0].ID, facts[1].ID
	cases := []struct {
		name  string
		claim ClaimedFact
		want  claimGroundingOutcome
	}{
		{"integer sum", derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(10), input(open, "open_items"), input(closed, "closed_items")), claimGrounded},
		{"integer difference", derivedClaim(facts, contractsv1.ContextFabricDerivationDifference, intValue(4), input(open, "open_items"), input(closed, "closed_items")), claimGrounded},
		{"negative difference", derivedClaim(facts, contractsv1.ContextFabricDerivationDifference, intValue(-4), input(closed, "closed_items"), input(open, "open_items")), claimGrounded},
		{"ratio", derivedClaim(facts, contractsv1.ContextFabricDerivationRatio, numberValue(7.0/3.0), input(open, "open_items"), input(closed, "closed_items")), claimGrounded},
		{"ratio inside tolerance", derivedClaim(facts, contractsv1.ContextFabricDerivationRatio, numberValue(7.0/3.0+1e-10), input(open, "open_items"), input(closed, "closed_items")), claimGrounded},
		{"ratio outside tolerance", derivedClaim(facts, contractsv1.ContextFabricDerivationRatio, numberValue(7.0/3.0+1e-8), input(open, "open_items"), input(closed, "closed_items")), claimDerivationValueMismatch},
		{"ratio rounded", derivedClaim(facts, contractsv1.ContextFabricDerivationRatio, numberValue(2.33), input(open, "open_items"), input(closed, "closed_items")), claimDerivationValueMismatch},
		{"float sum", derivedClaim(facts, contractsv1.ContextFabricDerivationSum, numberValue(7.5), input(open, "open_items"), input(open, "rate")), claimGrounded},
		{"wrong sum", derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(11), input(open, "open_items"), input(closed, "closed_items")), claimDerivationValueMismatch},
		{"wrong operation for the value", derivedClaim(facts, contractsv1.ContextFabricDerivationDifference, intValue(10), input(open, "open_items"), input(closed, "closed_items")), claimDerivationValueMismatch},
		{"ratio over zero", derivedClaim(facts, contractsv1.ContextFabricDerivationRatio, numberValue(0), input(open, "open_items"), input(closed, "zero")), claimDerivationValueMismatch},
		{"unknown fact id", derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(10), input(open, "open_items"), input("fact_missing", "closed_items")), claimDerivationInputUnknown},
		{"unobserved field", derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(10), input(open, "open_items"), input(closed, "absent")), claimDerivationInputUnknown},
		{"non numeric input", derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(10), input(open, "open_items"), input(open, "label")), claimDerivationInputUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, outcome := groundClaim(facts, tc.claim); outcome != tc.want {
				t.Fatalf("outcome %v, want %v", outcome, tc.want)
			}
		})
	}
}

func TestDerivedClaimInputOfAnotherSubjectIsUnknown(t *testing.T) {
	facts := derivationFacts()
	foreign := CanonicalFact{Kind: FactReadiness, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_other", Label: "Other"}, Fields: map[string]FactValue{"open_items": IntegerFactValue(5)}}
	foreign.ID = MintFactID(foreign.Kind, foreign.Subject, 0)
	served := append(append([]CanonicalFact{}, facts...), foreign)
	claim := derivedClaim(served, contractsv1.ContextFabricDerivationSum, intValue(12), input(facts[0].ID, "open_items"), input(foreign.ID, "open_items"))
	if _, outcome := groundClaim(served, claim); outcome != claimDerivationInputUnknown {
		t.Fatalf("another subject's fact grounded a derivation: %v", outcome)
	}
}

func TestDerivedClaimIsRejectedWithItsOwnReasonAndAttachesNoRows(t *testing.T) {
	facts := derivationFacts()
	open, closed := facts[0].ID, facts[1].ID
	bad := derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(11), input(open, "open_items"), input(closed, "closed_items"))
	_, outcome := groundClaim(facts, bad)
	if outcome != claimDerivationValueMismatch {
		t.Fatalf("premise moved: %v", outcome)
	}
	if got := SynthesisRejectionReasonOf(rejectSynthesisClaim(RejectionReasonClaimDerivationValueMismatch, 0, "x")); got != RejectionReasonClaimDerivationValueMismatch {
		t.Fatalf("reason %q is not in the closed vocabulary", got)
	}
	if got := SynthesisRejectionReasonOf(rejectSynthesisClaim(RejectionReasonClaimDerivationInputUnknown, 0, "x")); got != RejectionReasonClaimDerivationInputUnknown {
		t.Fatalf("reason %q is not in the closed vocabulary", got)
	}
	good := derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(10), input(open, "open_items"), input(closed, "closed_items"))
	if _, ok := claimSourceFact(facts, good); ok {
		t.Fatal("a derived claim has no single source fact; rows must not attach from an unrelated fact")
	}
	if got := ClaimsGroundedBeyondFirstGroupMember(facts, []ClaimedFact{good}); got != 0 {
		t.Fatalf("a derived claim is not a group-closure decision, got %d", got)
	}
}

func TestDerivedClaimShapeIsValidatedBeforeGrounding(t *testing.T) {
	facts := derivationFacts()
	open, closed := facts[0].ID, facts[1].ID
	valid := derivedClaim(facts, contractsv1.ContextFabricDerivationSum, intValue(10), input(open, "open_items"), input(closed, "closed_items"))
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid derivation rejected: %v", err)
	}
	mutations := map[string]func(c *ClaimedFact){
		"one input":         func(c *ClaimedFact) { c.DerivedFrom = c.DerivedFrom[:1] },
		"no operation":      func(c *ClaimedFact) { c.Operation = "" },
		"unknown operation": func(c *ClaimedFact) { c.Operation = "product" },
		"ratio of three": func(c *ClaimedFact) {
			c.Operation = contractsv1.ContextFabricDerivationRatio
			c.DerivedFrom = append(c.DerivedFrom, input(open, "rate"))
		},
		"fact id beside inputs": func(c *ClaimedFact) { c.FactID = open },
		"duplicate input":       func(c *ClaimedFact) { c.DerivedFrom[1] = c.DerivedFrom[0] },
		"string value":          func(c *ClaimedFact) { s := "10"; c.Value = ScalarValue{String: &s} },
		"inputs without an op":  func(c *ClaimedFact) { c.Operation = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			claim := valid
			claim.DerivedFrom = append([]contractsv1.ContextFabricDerivationInput{}, valid.DerivedFrom...)
			mutate(&claim)
			if err := claim.Validate(); err == nil {
				t.Fatal("malformed derivation passed Validate")
			}
		})
	}
}
