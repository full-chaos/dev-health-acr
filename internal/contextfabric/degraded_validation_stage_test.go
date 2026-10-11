package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The validation-stage degrade composes with the real composer: the canonical
// values about the members that stay are carried as claims, with the facts-only
// disclosure, and the prose names only those values.
func TestTheValidationStageDegradeCarriesTheMintedClaimsAndDisclosure(t *testing.T) {
	_, _, result, basis := validationStageFixture(t)
	member := result.Cohort.Members[0].Subject
	memberRef, ok := canonicalWorkItemEvidenceRef(member)
	if !ok {
		t.Fatal("fixture member has no canonical evidence ref")
	}
	outside := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work-item-outside-list", Label: "Outside"}
	basis.Input = SynthesisInput{Facts: CanonicalFactBundle{Facts: []CanonicalFact{
		{Kind: FactWork, Subject: member, Fields: map[string]FactValue{"title": StringFactValue("Member title")}, EvidenceRefIDs: []string{memberRef}, Source: "devhealthfacts.work", SourceVersion: "v1"},
		{Kind: FactWork, Subject: outside, Fields: map[string]FactValue{"title": StringFactValue("Outside title")}, EvidenceRefIDs: []string{"acr:v1:work_item:outside"}, Source: "devhealthfacts.work", SourceVersion: "v1"},
	}}}
	telemetry := &recordingTelemetry{}
	engine := &Engine{synthesizer: RuntimeAnswerSynthesizer{}, telemetry: telemetry}

	out, degraded := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)

	if !degraded {
		t.Fatal("not degraded")
	}
	minted := 0
	for _, claim := range out.ClaimedFacts {
		if strings.HasPrefix(claim.ClaimID, degradedFactClaimIDPrefix) {
			minted++
			if subjectKeyForModel(claim.Subject) != subjectKeyForModel(member) {
				t.Fatalf("minted claim %+v is about a subject the answer no longer serves", claim)
			}
		}
	}
	if minted != 1 {
		t.Fatalf("minted claims = %d in %+v, want the one value about the member that stays", minted, out.ClaimedFacts)
	}
	if !slices.Contains(out.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) {
		t.Fatalf("limitations %v: want the facts-only disclosure beside the claims", out.Limitations)
	}
	if !strings.Contains(out.CurrentState, "Member title") || strings.Contains(out.CurrentState, "Outside title") || !strings.HasPrefix(out.CurrentState, "Canonical values read:") {
		t.Fatalf("current_state = %q, want only the canonical values the claims carry", out.CurrentState)
	}
	if !slices.Contains(out.EvidenceRefIDs, memberRef) {
		t.Fatalf("evidence %v: want the member's row cited", out.EvidenceRefIDs)
	}
}

// With no value to carry for a retained member, the answer states the failure
// and claims no facts-only disclosure.
func TestTheValidationStageDegradeWithNoRetainedValueKeepsTheFailureStatement(t *testing.T) {
	_, _, result, basis := validationStageFixture(t)
	outside := SubjectRef{Kind: SubjectWorkItem, CanonicalID: "work-item-outside-list", Label: "Outside"}
	basis.Input = SynthesisInput{Facts: CanonicalFactBundle{Facts: []CanonicalFact{
		{Kind: FactWork, Subject: outside, Fields: map[string]FactValue{"title": StringFactValue("Outside title")}, EvidenceRefIDs: []string{"acr:v1:work_item:outside"}, Source: "devhealthfacts.work", SourceVersion: "v1"},
	}}}
	engine := &Engine{synthesizer: RuntimeAnswerSynthesizer{}, telemetry: &recordingTelemetry{}}

	out, degraded := engine.degradeWorkItemModelBreach(context.Background(), storage.Principal{OrgID: "org-1"}, result, basis)

	if !degraded || out.CurrentState != synthesisFailureCurrentState || slices.Contains(out.Limitations, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) {
		t.Fatalf("degraded=%v current_state=%q limitations=%v, want the failure statement and no facts-only disclosure", degraded, out.CurrentState, out.Limitations)
	}
}

func TestADegradedAnswerClaimLimitFollowsTheItemCeiling(t *testing.T) {
	max := contractsv1.ContextFabricClaimedFactsMaxCount
	candidates := make([]contractsv1.ContextFabricSubjectCandidate, 3)
	for _, tc := range []struct {
		name  string
		input SynthesisInput
		want  int
	}{
		{"no ceiling in force", SynthesisInput{}, max},
		{"ceiling 50, no candidates, no members", SynthesisInput{Allocation: ItemAllocation{MaxItems: 50}}, 49},
		{"ceiling 50 minus members", SynthesisInput{Allocation: ItemAllocation{MaxItems: 50, MemberRowCommitment: 10}}, 39},
		{"ceiling 50 minus candidates", SynthesisInput{Allocation: ItemAllocation{MaxItems: 50}, Graph: GraphContext{Resolution: SubjectResolution{Candidates: candidates}}}, 46},
		{"ceiling above the claim bound", SynthesisInput{Allocation: ItemAllocation{MaxItems: 1000}}, max},
		{"commitments meet the ceiling", SynthesisInput{Allocation: ItemAllocation{MaxItems: 10, MemberRowCommitment: 10}}, 0},
	} {
		if got := degradedClaimLimit(tc.input); got != tc.want {
			t.Fatalf("%s: limit = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestADegradedAnswerStopsAtTheItemCeilingAndDisclosesIt(t *testing.T) {
	facts := make([]CanonicalFact, 0, 120)
	for index := 0; index < 120; index++ {
		facts = append(facts, degradedScalarFact(index, 1))
	}
	input := SynthesisInput{Allocation: ItemAllocation{MaxItems: 50}, Facts: CanonicalFactBundle{Facts: facts}}
	failure := &SynthesisFailure{Class: SynthesisFailureModelOutputInvalid, DeferEvent: true, cause: context.Canceled}

	result, err := RuntimeAnswerSynthesizer{}.ComposeDegraded(context.Background(), storage.Principal{OrgID: "org_1"}, input, failure)
	if err != nil {
		t.Fatal(err)
	}

	if got := contractsv1.CountContextFabricResultItems(result).Budgeted(); len(result.ClaimedFacts) != 49 || got > 50 {
		t.Fatalf("claims = %d budgeted items = %d, want 49 claims inside the 50-item ceiling", len(result.ClaimedFacts), got)
	}
	if !slices.Contains(result.Limitations, contractsv1.ContextFabricSynthesisFailedFactsTruncatedLimitation) {
		t.Fatalf("limitations = %q, want the truncated disclosure", result.Limitations)
	}
}
