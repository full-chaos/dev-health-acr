package v1

import (
	"strings"
	"testing"
)

func TestPathDropLimitationRoundTripsAndRejectsLookalikes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ served, declared int }{{0, 25}, {12, 25}, {1, 2}} {
		sentence, ok := ContextFabricPathDropLimitation(testCase.served, testCase.declared)
		if !ok {
			t.Fatalf("%+v: not composed", testCase)
		}
		if !IsContextFabricPathDropLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("%q is not recognised as the service-authored sentence it is", sentence)
		}
		if IsContextFabricFactRowTruncationLimitation(sentence) || IsContextFabricClaimDepthLimitation(sentence) {
			t.Fatalf("%q is recognised as another lever's sentence", sentence)
		}
	}
	for _, testCase := range []struct{ served, declared int }{{25, 25}, {26, 25}, {-1, 25}} {
		if _, ok := ContextFabricPathDropLimitation(testCase.served, testCase.declared); ok {
			t.Fatalf("%+v composed a sentence for a drop that did not happen", testCase)
		}
	}
	sentence, _ := ContextFabricPathDropLimitation(12, 25)
	for _, lookalike := range []string{
		sentence + " ",
		strings.Replace(sentence, "shows 12 ", "shows 012 ", 1),
		strings.Replace(sentence, "shows 12 ", "shows 30 ", 1),
		strings.Replace(sentence, "no driver cites", "every driver cites", 1),
	} {
		if lookalike == sentence {
			t.Fatalf("look-alike %q did not change the sentence; this row tests nothing", lookalike)
		}
		if IsContextFabricPathDropLimitation(lookalike) {
			t.Fatalf("look-alike recognised: %q", lookalike)
		}
	}
}
