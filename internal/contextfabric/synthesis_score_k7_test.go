package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Decision K7: never a bare score. A model-written answer that claims a declared score (health compounding_risk, whose
// drivers table risk_rules is not among the facts served) must not carry that claim. On main the claim is admitted.
func TestAModelWrittenAnswerNeverClaimsABareScore(t *testing.T) {
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_a", Label: "Team A"}
	input := validSynthesisInputFixture()
	input.Graph.Resolution.Committed = []SubjectRef{team}
	risk := 0.993773746751932
	input.Facts.Facts = []CanonicalFact{{
		Kind: FactHealth, Subject: team,
		Fields:         map[string]FactValue{"compounding_risk": {Number: &risk}},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable,
		Source: "devhealthfacts.health", SourceVersion: "v1",
	}}
	draft := validSynthesisDraftFixture(input)
	draft.Drivers[0].AffectedSubjects = []SubjectRef{team}
	draft.Drivers[0].Category = "relationship"
	draft.ClaimedFacts = []ClaimedFact{{
		ClaimID: "claim_risk_1", Kind: FactHealth, Subject: team,
		Field: "compounding_risk", Value: ScalarValue{Number: &risk},
	}}
	synthesizer := RuntimeAnswerSynthesizer{Runtime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}

	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Field == "compounding_risk" {
			t.Fatalf("the answer claims the score %q with no drivers table served: %+v", claim.Field, claim)
		}
	}
}
