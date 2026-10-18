package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A draw with one grounded claim and one the stored facts contradict is
// accepted on the first draw with only the grounded claim, instead of being
// rejected and redrawn.
func TestAPartlyUngroundedDrawIsAcceptedWithoutItsUngroundedClaim(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	output := validSynthesisOutput()
	bad := groundedReadinessClaim(input)
	bad.ClaimID, bad.Field = "claim_readiness_unobserved", "field_no_fact_carries"
	output.ClaimedFacts = []contextfabric.ClaimedFact{groundedReadinessClaim(input), bad}
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{output, validSynthesisOutput()}}
	runtime := mustRuntime(t, gen, Config{MaxSynthesisResynthesisAttempts: 3})

	draft, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input)

	if err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the grounded remainder", err)
	}
	if gen.calls != 1 {
		t.Fatalf("model calls = %d, want 1: the partly ungrounded draw was rejected and redrawn", gen.calls)
	}
	if len(draft.ClaimedFacts) != 1 || draft.ClaimedFacts[0].ClaimID != "claim_readiness_grounded" {
		t.Fatalf("claims = %+v, want only the grounded one", draft.ClaimedFacts)
	}
}
