package genkitruntime

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The RENDERED prompt must not offer `cardinality`.
//
// The vocabulary test next door proves the constant is absent from the array.
// This proves the consequence that actually matters: the closed set the model
// is shown does not contain it, so the model is never told it may ask for a
// kind nothing produces.
//
// Asserted on the rendered STRING rather than on the array, because the string
// is what reaches the model -- and because a future change that renders the
// list from a different source would pass an array-only assertion while
// putting the kind back in front of the model.
func TestTheInterpretationPromptDoesNotOfferCardinality(t *testing.T) {
	t.Parallel()
	if contextFabricFactKindList == "" {
		t.Fatal("the rendered fact-kind list is empty -- this test would pass vacuously")
	}
	if strings.Contains(contextFabricFactKindList, string(contractsv1.ContextFabricFactCardinality)) {
		t.Fatalf("the prompt's closed set offers %q: %s", contractsv1.ContextFabricFactCardinality, contextFabricFactKindList)
	}
	// Non-vacuous control: a kind that IS requestable must be present, so the
	// assertion above is reading a real list and not an empty or malformed one.
	if !strings.Contains(contextFabricFactKindList, string(contractsv1.ContextFabricFactHealth)) {
		t.Fatalf("the prompt's closed set is missing a requestable kind (health): %s", contextFabricFactKindList)
	}
}
