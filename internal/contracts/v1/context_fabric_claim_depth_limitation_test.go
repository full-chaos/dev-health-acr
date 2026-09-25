package v1

import (
	"strings"
	"testing"
)

func TestClaimDepthLimitationRoundTripsAndRejectsLookalikes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ served, declared, subjects, perMember int }{{11, 22, 11, 1}, {20, 33, 11, 2}, {0, 1, 1, 1}} {
		sentence, ok := ContextFabricClaimDepthLimitation(testCase.served, testCase.declared, testCase.subjects, testCase.perMember)
		if !ok {
			t.Fatalf("%+v: not composed", testCase)
		}
		if !IsContextFabricClaimDepthLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("%q is not recognised as the service-authored sentence it is", sentence)
		}
		if IsContextFabricFactRowTruncationLimitation(sentence) {
			t.Fatalf("%q is recognised as the row-truncation sentence too", sentence)
		}
	}
	for _, testCase := range []struct{ served, declared, subjects, perMember int }{
		{22, 22, 11, 2}, // nothing cut
		{23, 22, 11, 2}, // served above declared
		{11, 22, 11, 0}, // a zero cap
		{11, 22, 0, 1},  // no subjects
	} {
		if _, ok := ContextFabricClaimDepthLimitation(testCase.served, testCase.declared, testCase.subjects, testCase.perMember); ok {
			t.Fatalf("%+v composed a sentence for a cut that did not happen", testCase)
		}
	}
	sentence, _ := ContextFabricClaimDepthLimitation(11, 22, 11, 1)
	for _, lookalike := range []string{
		sentence + " ",
		strings.Replace(sentence, "at most 1 fact ", "at most 1 facts ", 1),
		strings.Replace(sentence, "shows 11 ", "shows 011 ", 1),
		strings.Replace(sentence, "shows 11 ", "shows 90 ", 1),
		strings.Replace(sentence, "item budget", "size budget", 1),
	} {
		if lookalike == sentence {
			t.Fatalf("look-alike %q did not change the sentence; this row tests nothing", lookalike)
		}
		if IsContextFabricClaimDepthLimitation(lookalike) {
			t.Fatalf("look-alike recognised: %q", lookalike)
		}
	}
}
