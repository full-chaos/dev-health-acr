package v1

import "testing"

func TestBudgetTrimLimitationIsRecognisedWholeAndServiceAuthored(t *testing.T) {
	sentence := ContextFabricBudgetTrimClaimedFactsLimitation
	if !IsContextFabricBudgetTrimLimitation(sentence) {
		t.Fatal("the composed sentence is not recognised")
	}
	for _, other := range []string{"", sentence + " ", "This answer was shortened to fit the item budget.", "x" + sentence} {
		if IsContextFabricBudgetTrimLimitation(other) {
			t.Fatalf("%q was recognised", other)
		}
	}
	if !IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatal("the trim sentence is not service-authored, so the limitation cap could displace it")
	}
}
