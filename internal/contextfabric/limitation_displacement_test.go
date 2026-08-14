package contextfabric

import (
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// modelLimitations returns count distinct model-authored limitation
// strings, none of which is the retrieval-degradation disclosure.
func modelLimitations(count int) []string {
	limitations := make([]string, 0, count)
	for i := 0; i < count; i++ {
		limitations = append(limitations, "Model-authored caveat number "+strconv.Itoa(i)+".")
	}
	return limitations
}

// TestRetrievalDegradationDisclosureSurvivesAFullLimitationList documents
// the defect first, at the number that produces it.
//
// The engine appends the retrieval-degradation limitation to whatever the
// model returned. When the model already returned a FULL list, that append
// pushes the result one past ContextFabricLimitationsMaxCount, and
// ContextFabricInvestigationResult.Validate then rejects the whole thing:
// ErrInvalidResult, no answer at all, because a degradation disclosure
// could not fit.
//
// The number is not the point and never was. The cap was 250 on main and
// is 100 here, and the same append produces the same failure at either --
// so this test derives the count from the contract constant rather than
// naming one, and would have failed identically before the narrowing.
//
// What makes it a real path rather than a curiosity: a degraded retrieval
// is exactly the run most likely to produce a long limitation list, since
// the same missing mechanism drives the model to note more gaps.
func TestRetrievalDegradationDisclosureSurvivesAFullLimitationList(t *testing.T) {
	limitations := withRetrievalDegradation(modelLimitations(contractsv1.ContextFabricLimitationsMaxCount))

	if got, want := len(limitations), contractsv1.ContextFabricLimitationsMaxCount; got != want {
		t.Errorf("composed %d limitations, want at most the contract's %d: the append cost the entire answer", got, want)
	}
	if !hasRetrievalDegradedLimitation(limitations) {
		t.Error("the degradation disclosure was dropped: a bounded consumer reads a degraded answer as a clean one")
	}

	// The consequence, through the REAL validator. Without this the count
	// above reads as a tidiness complaint rather than what it is: the
	// engine returns ErrInvalidResult and the caller gets no answer.
	result := validInvestigationResult()
	result.Limitations = limitations
	if err := result.Validate(); err != nil {
		t.Errorf("the composed result does not validate, so the whole investigation is rejected: %v", err)
	}
}

// TestDisplacementDropsTheLastModelLimitationOnly pins WHICH entry gives
// way, because "it fits now" is not enough on its own.
//
// The disclosure is service-authored and is a statement about how much the
// answer is worth; a model caveat is one of many. So the last model-
// authored entry is displaced, the earlier ones keep their order, and the
// disclosure lands at the end where a reader meets it after the caveats it
// qualifies.
func TestDisplacementDropsTheLastModelLimitationOnly(t *testing.T) {
	original := modelLimitations(contractsv1.ContextFabricLimitationsMaxCount)
	composed := withRetrievalDegradation(original)

	if len(composed) != contractsv1.ContextFabricLimitationsMaxCount {
		t.Fatalf("composed %d limitations, want %d", len(composed), contractsv1.ContextFabricLimitationsMaxCount)
	}
	if composed[len(composed)-1] != retrievalDegradedLimitation {
		t.Errorf("the disclosure is not last: %q", composed[len(composed)-1])
	}
	// Every model entry but the final one survives, in order.
	for i := 0; i < len(composed)-1; i++ {
		if composed[i] != original[i] {
			t.Fatalf("model limitation %d changed from %q to %q: displacement reordered the survivors", i, original[i], composed[i])
		}
	}
	if strings.Contains(strings.Join(composed, "\n"), original[len(original)-1]) {
		t.Errorf("the displaced entry %q is still present", original[len(original)-1])
	}
}

// TestDisplacementIsSkippedWhenTheDisclosureIsAlreadyPresent covers the
// reuse-shaped case: a draft that already carries either spelling must not
// gain a second copy, and must not displace anything to make room for one.
func TestDisplacementIsSkippedWhenTheDisclosureIsAlreadyPresent(t *testing.T) {
	for name, present := range map[string]string{
		"current": retrievalDegradedLimitation,
		"legacy":  retrievalDegradedLimitationLegacy,
	} {
		t.Run(name, func(t *testing.T) {
			original := append(modelLimitations(contractsv1.ContextFabricLimitationsMaxCount-1), present)
			composed := withRetrievalDegradation(original)

			if len(composed) != len(original) {
				t.Errorf("composed %d limitations from %d: a disclosure already present was duplicated or something was displaced for nothing", len(composed), len(original))
			}
			if composed[len(composed)-1] != present {
				t.Errorf("the stored spelling was rewritten to %q; an immutable answer's own wording must survive verbatim", composed[len(composed)-1])
			}
		})
	}
}

// TestDisplacementLeavesRoomAlone is the ordinary case: below the cap
// nothing is displaced and the disclosure is simply appended.
func TestDisplacementLeavesRoomAlone(t *testing.T) {
	original := modelLimitations(3)
	composed := withRetrievalDegradation(original)

	if len(composed) != len(original)+1 {
		t.Fatalf("composed %d limitations from %d, want one more", len(composed), len(original))
	}
	if composed[len(composed)-1] != retrievalDegradedLimitation {
		t.Errorf("the disclosure is not last: %q", composed[len(composed)-1])
	}
}
