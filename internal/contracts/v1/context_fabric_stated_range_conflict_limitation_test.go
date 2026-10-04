package v1

import "testing"

func TestStatedRangeConflictLimitationIsAServiceDisclosureRecognisedWhole(t *testing.T) {
	sentence := ContextFabricStatedRangeConflictLimitation("2026-09-27", "2026-10-04", "2026-09-04", "2026-10-04")
	if !IsContextFabricStatedRangeConflictLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatalf("the composed sentence is not recognised as a service disclosure: %q", sentence)
	}
	for _, other := range []string{
		sentence + " More.",
		"Note: " + sentence,
		ContextFabricStatedRangeConflictLimitation("last week", "now", "2026-09-04", "2026-10-04"),
		ContextFabricStatedRangeConflictLimitation("a while ago", "2026-10-04", "2026-09-04", "2026-10-04"),
		"The interpretation sent with this question read its period differently.",
	} {
		if IsContextFabricStatedRangeConflictLimitation(other) {
			t.Errorf("%q is recognised as the range-conflict disclosure", other)
		}
	}
}
