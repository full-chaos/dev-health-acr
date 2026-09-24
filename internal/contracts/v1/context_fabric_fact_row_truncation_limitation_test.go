package v1

import (
	"strings"
	"testing"
)

func TestFactRowTruncationLimitationRoundTripsAndRejectsLookalikes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ served, declared, perTable int }{{26, 780, 1}, {390, 780, 15}, {1, 2, 1}} {
		sentence, ok := ContextFabricFactRowTruncationLimitation(testCase.served, testCase.declared, testCase.perTable)
		if !ok {
			t.Fatalf("%+v: not composed", testCase)
		}
		if !IsContextFabricFactRowTruncationLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("%q is not recognised as the service-authored sentence it is", sentence)
		}
	}
	for _, testCase := range []struct{ served, declared, perTable int }{
		{780, 780, 30}, // nothing cut
		{800, 780, 30}, // served above declared
		{26, 780, 0},   // a zero cap would strip whole tables
		{0, 780, 1},    // fewer rows than the cap can leave
	} {
		if _, ok := ContextFabricFactRowTruncationLimitation(testCase.served, testCase.declared, testCase.perTable); ok {
			t.Fatalf("%+v composed a sentence for a cut that did not happen", testCase)
		}
	}
	sentence, _ := ContextFabricFactRowTruncationLimitation(26, 780, 1)
	for _, lookalike := range []string{
		sentence + " ",
		strings.Replace(sentence, "at most 1 row ", "at most 1 rows ", 1),
		strings.Replace(sentence, "shows 26 ", "shows 026 ", 1),
		strings.Replace(sentence, "shows 26 ", "shows 900 ", 1),
		strings.Replace(sentence, "most recent days", "oldest days", 1),
	} {
		if lookalike == sentence {
			t.Fatalf("look-alike %q did not change the sentence; this row tests nothing", lookalike)
		}
		if IsContextFabricFactRowTruncationLimitation(lookalike) {
			t.Fatalf("look-alike recognised: %q", lookalike)
		}
	}
}
