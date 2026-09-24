package v1

import "testing"

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
		"This answer shows 26 of the 780 table rows its facts carried, because the assembled answer exceeded the size budget: each table keeps its first 1 rows in the order its source listed them. Ask about a shorter evidence window or allow a larger response budget to see the rest.",
		"This answer shows 026 of the 780 table rows its facts carried, because the assembled answer exceeded the size budget: each table keeps its first 1 row in the order its source listed them. Ask about a shorter evidence window or allow a larger response budget to see the rest.",
		"This answer shows 900 of the 780 table rows its facts carried, because the assembled answer exceeded the size budget: each table keeps its first 1 row in the order its source listed them. Ask about a shorter evidence window or allow a larger response budget to see the rest.",
	} {
		if IsContextFabricFactRowTruncationLimitation(lookalike) {
			t.Fatalf("look-alike recognised: %q", lookalike)
		}
	}
}
