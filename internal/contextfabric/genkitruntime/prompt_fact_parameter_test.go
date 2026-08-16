package genkitruntime

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

// TestInterpretationPromptFactParameterClaimIsCurrentlyTrue is the
// mechanical oracle for the CHAOS-3854 prompt-side sentence added in
// interpretation v7: "fact_requirements[].parameters accepts NO keys for
// any fact family in this deployment".
//
// That sentence is a factual claim about internal/contextfabric/
// devhealthfacts' production wiring (every provider's Capability() call
// goes through devhealthfacts' shared newCapability helper, which never
// sets FactCapability.AllowedParameters), not merely a stated bound like
// the length/count limits above it. A future change that gives even ONE
// capability a real parameter would make the prompt's blanket claim false
// -- the model would then be told to omit a key a provider actually wants
// -- and nothing else here would notice, because
// TestPromptsStateEveryModelFacingBound only checks numeric bounds, not
// this vocabulary-shaped sentence.
//
// NewProviders(nil) is safe here: Capability() is a pure declaration that
// never touches the ClickHouseQueryClient argument (see each provider's
// Capability() method in devhealthfacts), so this needs no real database
// connection to be a faithful check of what the prompt actually promises.
func TestInterpretationPromptFactParameterClaimIsCurrentlyTrue(t *testing.T) {
	const claim = "fact_requirements[].parameters accepts NO keys for any fact family in this deployment"
	if !strings.Contains(interpretationSystemPrompt, claim) {
		t.Fatalf("the interpretation prompt no longer makes the empty-parameter-vocabulary claim; this assertion is stale and must be updated deliberately")
	}

	for _, provider := range devhealthfacts.NewProviders(nil) {
		capability := provider.Capability()
		if len(capability.AllowedParameters) != 0 {
			t.Errorf("fact capability %q (%s) now declares AllowedParameters %v, which makes the interpretation prompt's blanket claim (%q) false for this capability -- either narrow the prompt to say which capabilities accept which keys, or drop this sentence and enumerate the real per-capability vocabulary instead",
				capability.Kind, capability.Name, capability.AllowedParameters, claim)
		}
	}
}
