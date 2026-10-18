package v1

import "testing"

func TestUngroundedItemsLimitationIsRecognisedWholeAndServiceAuthored(t *testing.T) {
	sentence := ContextFabricUngroundedItemsDroppedLimitation
	if !IsContextFabricUngroundedItemsDroppedLimitation(sentence) {
		t.Fatal("the composed sentence is not recognised")
	}
	for _, other := range []string{"", sentence + " ", "x" + sentence} {
		if IsContextFabricUngroundedItemsDroppedLimitation(other) {
			t.Fatalf("%q was recognised", other)
		}
	}
	if !IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatal("the sentence is not service-authored, so the limitation cap could displace it")
	}
}
