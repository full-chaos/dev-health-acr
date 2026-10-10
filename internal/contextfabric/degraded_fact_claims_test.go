package contextfabric

import (
	"context"
	"fmt"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func degradedClaimFact(index, refs int, fields map[string]FactValue) CanonicalFact {
	id := fmt.Sprintf("PLAT-%04d", index)
	evidence := make([]string, 0, refs)
	for ref := 0; ref < refs; ref++ {
		evidence = append(evidence, fmt.Sprintf("acr:v1:work_item:%s:%d", id, ref))
	}
	return CanonicalFact{
		Kind:           FactWork,
		Subject:        SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:" + id, Label: id},
		Fields:         fields,
		EvidenceRefIDs: evidence,
		Source:         "devhealthfacts.work",
		SourceVersion:  "v1",
	}
}

func degradedScalarFact(index, refs int) CanonicalFact {
	return degradedClaimFact(index, refs, map[string]FactValue{"title": StringFactValue("title " + fmt.Sprint(index))})
}

func TestADegradedAnswerMintsOneClaimPerScalarFieldRead(t *testing.T) {
	number, count := 2.5, int64(7)
	facts := []CanonicalFact{degradedClaimFact(1, 2, map[string]FactValue{
		"b_count": {Integer: &count},
		"a_title": StringFactValue("alpha"),
		"c_ratio": {Number: &number},
		"d_done":  BooleanFactValue(true),
		"e_null":  {Null: true},
		"f_rows":  {Rows: []FactValueRow{{Fields: map[string]FactValue{"x": StringFactValue("y")}}}},
	})}

	claims, evidence, truncated := mintDegradedFactClaims(facts)

	if truncated || len(claims) != 5 {
		t.Fatalf("claims = %d truncated = %v, want the five scalar fields and no truncation: %+v", len(claims), truncated, claims)
	}
	fields := make([]string, 0, len(claims))
	for _, claim := range claims {
		fields = append(fields, claim.Field)
		if _, outcome := groundClaim(facts, claim); outcome != claimGrounded {
			t.Fatalf("claim %+v is not grounded in the facts it was minted from", claim)
		}
	}
	if !slices.Equal(fields, []string{"a_title", "b_count", "c_ratio", "d_done", "e_null"}) {
		t.Fatalf("fields = %v, want the scalar fields in field order", fields)
	}
	if !slices.Equal(evidence, facts[0].EvidenceRefIDs) {
		t.Fatalf("evidence = %v, want the cited rows once each", evidence)
	}
}

func TestADegradedAnswerCitesEachEvidenceRowOnce(t *testing.T) {
	first := degradedClaimFact(1, 1, map[string]FactValue{"a": StringFactValue("x"), "b": StringFactValue("y")})
	_, evidence, _ := mintDegradedFactClaims([]CanonicalFact{first})
	if len(evidence) != 1 {
		t.Fatalf("evidence = %v, want one row for two claims of one fact", evidence)
	}
}

func TestADegradedAnswerStopsAtTheClaimBound(t *testing.T) {
	for _, tc := range []struct {
		facts         int
		wantClaims    int
		wantTruncated bool
	}{
		{contractsv1.ContextFabricClaimedFactsMaxCount - 1, contractsv1.ContextFabricClaimedFactsMaxCount - 1, false},
		{contractsv1.ContextFabricClaimedFactsMaxCount, contractsv1.ContextFabricClaimedFactsMaxCount, false},
		{contractsv1.ContextFabricClaimedFactsMaxCount + 1, contractsv1.ContextFabricClaimedFactsMaxCount, true},
	} {
		facts := make([]CanonicalFact, 0, tc.facts)
		for index := 0; index < tc.facts; index++ {
			facts = append(facts, degradedScalarFact(index, 1))
		}
		claims, _, truncated := mintDegradedFactClaims(facts)
		if len(claims) != tc.wantClaims || truncated != tc.wantTruncated {
			t.Fatalf("%d facts: claims = %d truncated = %v, want %d and %v", tc.facts, len(claims), truncated, tc.wantClaims, tc.wantTruncated)
		}
	}
}

func TestADegradedAnswerStopsAtTheEvidenceBound(t *testing.T) {
	facts := make([]CanonicalFact, 0, 200)
	for index := 0; index < 200; index++ {
		facts = append(facts, degradedScalarFact(index, 3))
	}

	claims, evidence, truncated := mintDegradedFactClaims(facts)

	if !truncated || len(evidence) > contractsv1.ContextFabricEvidenceRefIDsMaxCount || len(evidence) != 3*len(claims) || len(claims) != contractsv1.ContextFabricEvidenceRefIDsMaxCount/3 {
		t.Fatalf("claims = %d evidence = %d truncated = %v, want a claim only with its evidence inside the bound", len(claims), len(evidence), truncated)
	}
}

func TestADegradedAnswerServesFactsAndNamesTheMechanism(t *testing.T) {
	over := contractsv1.ContextFabricClaimedFactsMaxCount + 5
	facts := make([]CanonicalFact, 0, over)
	for index := 0; index < over; index++ {
		facts = append(facts, degradedScalarFact(index, 1))
	}
	input := SynthesisInput{Facts: CanonicalFactBundle{Facts: facts}}
	synthesizer := RuntimeAnswerSynthesizer{}
	failure := &SynthesisFailure{Class: SynthesisFailureModelOutputInvalid, DeferEvent: true, cause: fmt.Errorf("model output")}

	result, err := synthesizer.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, input, failure)
	if err != nil {
		t.Fatal(err)
	}

	if result.Status != InvestigationDegraded || len(result.ClaimedFacts) != contractsv1.ContextFabricClaimedFactsMaxCount || len(result.Drivers) != 0 {
		t.Fatalf("status = %q claims = %d drivers = %d, want degraded, exactly the bound, no model judgment", result.Status, len(result.ClaimedFacts), len(result.Drivers))
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) || !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisFailedFactsTruncatedLimitation) {
		t.Fatalf("limitations = %q, want the facts-only and the truncated disclosures", result.Limitations)
	}
	if !IsSynthesisModelFailureAnswer(result) {
		t.Fatal("a degraded answer that carries facts is not recognised as a model-failure answer, so it could be reused")
	}
	for _, limitation := range []string{contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation, contractsv1.ContextFabricSynthesisFailedFactsTruncatedLimitation} {
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) {
			t.Fatalf("%q is not in the service-authored list: a later disclosure could displace it", limitation)
		}
	}
}

func TestADegradedAnswerWithNoScalarFactsKeepsTheFailureStatement(t *testing.T) {
	facts := []CanonicalFact{degradedClaimFact(1, 1, map[string]FactValue{"rows": {Rows: []FactValueRow{{Fields: map[string]FactValue{"x": StringFactValue("y")}}}}})}
	failure := &SynthesisFailure{Class: SynthesisFailureModelOutputInvalid, DeferEvent: true, cause: fmt.Errorf("model output")}

	result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, SynthesisInput{Facts: CanonicalFactBundle{Facts: facts}}, failure)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.ClaimedFacts) != 0 || result.CurrentState != synthesisFailureCurrentState || slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) {
		t.Fatalf("claims = %d current_state = %q limitations = %q, want the failure statement and no facts-only disclosure", len(result.ClaimedFacts), result.CurrentState, result.Limitations)
	}
}
