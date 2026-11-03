package contextfabric

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Lead ruling (CHAOS-12554): subcategory_breakdown is declared whatever the
// switch, as completion_distribution is. With the switch off a fact carries no
// such table, so an answer whose claim names it must get a closed reason, never
// an empty table and never silence. Today that is the grounding rule: the claim
// is unobserved, so it is left out with the closed rejection reason
// claim_field_unobserved, the answer carries the dropped-items limitation and
// is partial, and the served-answer drop line counts the reason.
func TestCHAOS12554WithTheSwitchOffANamedTableGetsAClosedReason(t *testing.T) {
	input, team := k7Input(false)
	quality := 0.25
	// The team's investment fact as the provider serves it with the switch off:
	// the theme table and the scalars, no subcategory_breakdown.
	input.Facts.Facts = append(input.Facts.Facts, CanonicalFact{
		Kind: FactInvestment, Subject: team,
		Fields: map[string]FactValue{
			"theme_quality": {Number: &quality},
			"theme_breakdown": TableFactValue(FactTable{Shape: FactTableBreakdown, Key: []string{"theme"}, Measures: []string{"share"},
				Rows: []FactValueRow{{Fields: map[string]FactValue{"theme": StringFactValue("quality"), "share": NumberFactValue(quality)}}}}),
		},
		EvidenceRefIDs: []string{"evidence_release_1234"}, SourceState: SourceAvailable, Source: "devhealthfacts.investment", SourceVersion: "v1",
	})
	draft := k7Draft(input, team, "severity")
	named := ClaimedFact{ClaimID: "claim_subcategory_1", Kind: FactInvestment, Subject: team, Field: "subcategory_breakdown", Value: ScalarValue{Null: true}}
	draft.ClaimedFacts = append(draft.ClaimedFacts, named)

	if _, outcome := groundClaim(input.Facts.Facts, named); outcome != claimFieldUnobserved {
		t.Fatalf("grounding outcome %v, want claimFieldUnobserved", outcome)
	}
	if reason, ungrounded := ungroundedClaimReason(input, named, synthesisSubjects(input), canonicalSubjectLabels(input)); !ungrounded || reason != RejectionReasonClaimFieldUnobserved {
		t.Fatalf("drop reason %q (%v), want the closed %q", reason, ungrounded, RejectionReasonClaimFieldUnobserved)
	}

	runtime := &recordingModelRuntime{fakeModelRuntime: fakeModelRuntime{draft: draft, receipt: validModelReceiptFixture(ModelOperationSynthesize)}}
	var logs bytes.Buffer
	synthesizer := RuntimeAnswerSynthesizer{Runtime: runtime, Telemetry: NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&logs, nil)))}
	result, err := synthesizer.Synthesize(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	if err != nil {
		t.Fatalf("Synthesize() error = %v, want the grounded remainder served", err)
	}
	for _, claim := range result.ClaimedFacts {
		if claim.Field == "subcategory_breakdown" || (claim.Table != nil && claim.Table.Field == "subcategory_breakdown") {
			t.Fatalf("a claim on the missing table was served: %+v", claim)
		}
		if claim.Table != nil && len(claim.Rows) == 0 {
			t.Fatalf("claim %s carries an empty table", claim.ClaimID)
		}
	}
	if got := servedClaimIDs(result); !slices.Equal(got, []string{"claim_risk_1"}) {
		t.Fatalf("claims on the wire %v, want only the grounded one", got)
	}
	if result.Status != InvestigationPartial || !slices.Contains(result.Limitations, contractsv1.ContextFabricUngroundedItemsDroppedLimitation) {
		t.Fatalf("status %q limitations %v, want partial with the dropped-items limitation", result.Status, result.Limitations)
	}
	if !strings.Contains(logs.String(), "dropped_claim_field_unobserved=1") {
		t.Fatalf("the drop line does not count the closed reason: %s", logs.String())
	}
}
