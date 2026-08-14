package contextfabric

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
	limitations, displaced := withRetrievalDegradation(modelLimitations(contractsv1.ContextFabricLimitationsMaxCount))

	if got, want := len(limitations), contractsv1.ContextFabricLimitationsMaxCount; got != want {
		t.Errorf("composed %d limitations, want at most the contract's %d: the append cost the entire answer", got, want)
	}
	if !hasRetrievalDegradedLimitation(limitations) {
		t.Error("the degradation disclosure was dropped: a bounded consumer reads a degraded answer as a clean one")
	}
	// The count comes back from the swap itself. A before/after length
	// comparison cannot see this: both lists are the same length.
	if displaced != 1 {
		t.Errorf("displaced = %d, want 1: a caveat was dropped and the caller was told nothing", displaced)
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
	composed, displaced := withRetrievalDegradation(original)

	if len(composed) != contractsv1.ContextFabricLimitationsMaxCount {
		t.Fatalf("composed %d limitations, want %d", len(composed), contractsv1.ContextFabricLimitationsMaxCount)
	}
	if displaced != 1 {
		t.Errorf("displaced = %d, want 1", displaced)
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
			composed, displaced := withRetrievalDegradation(original)

			if len(composed) != len(original) {
				t.Errorf("composed %d limitations from %d: a disclosure already present was duplicated or something was displaced for nothing", len(composed), len(original))
			}
			if displaced != 0 {
				t.Errorf("displaced = %d, want 0: nothing may be dropped for a disclosure that is already there", displaced)
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
	composed, displaced := withRetrievalDegradation(original)

	if len(composed) != len(original)+1 {
		t.Fatalf("composed %d limitations from %d, want one more", len(composed), len(original))
	}
	if displaced != 0 {
		t.Errorf("displaced = %d, want 0: there was room, so nothing was dropped", displaced)
	}
	if composed[len(composed)-1] != retrievalDegradedLimitation {
		t.Errorf("the disclosure is not last: %q", composed[len(composed)-1])
	}
}

// TestEngineRecordsItsOwnDisplacement drives the real Investigate path at
// the cap, which is the only place the RESULT's own count is written.
//
// Round 16 found the counter dead: the displacement happened, nothing
// recorded it, and the projection reported no omission. The unit tests
// above prove the swap reports a count; this proves the engine keeps it.
// Without this, discarding the value at the call site fails nothing.
func TestEngineRecordsItsOwnDisplacement(t *testing.T) {
	engine, request := engineForDegradationWithLimitations(t, true, modelLimitations(contractsv1.ContextFabricLimitationsMaxCount))

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_displacement"}, request)
	if err != nil {
		t.Fatalf("Investigate() = %v, want an answer: the displacement must cost a caveat, never the answer", err)
	}

	if got, want := len(result.Limitations), contractsv1.ContextFabricLimitationsMaxCount; got != want {
		t.Errorf("result carries %d limitations, want the cap %d", got, want)
	}
	if !hasRetrievalDegradedLimitation(result.Limitations) {
		t.Error("the degradation disclosure is absent from a degraded answer")
	}
	if result.LimitationsDisplaced != 1 {
		t.Errorf("result.LimitationsDisplaced = %d, want 1: the engine dropped a caveat and recorded nothing", result.LimitationsDisplaced)
	}
}

// TestEngineRecordsNoDisplacementWhenThereIsRoom is the other side at the
// engine level: one below the cap, the disclosure is appended into real
// room, so the result must claim no loss. The contract's coherence rule
// would reject a false positive here anyway, which is the point -- the two
// guards agree.
func TestEngineRecordsNoDisplacementWhenThereIsRoom(t *testing.T) {
	engine, request := engineForDegradationWithLimitations(t, true, modelLimitations(contractsv1.ContextFabricLimitationsMaxCount-1))

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_displacement"}, request)
	if err != nil {
		t.Fatalf("Investigate() = %v, want an answer", err)
	}

	if result.LimitationsDisplaced != 0 {
		t.Errorf("result.LimitationsDisplaced = %d, want 0: there was room", result.LimitationsDisplaced)
	}
	if got, want := len(result.Limitations), contractsv1.ContextFabricLimitationsMaxCount; got != want {
		t.Errorf("result carries %d limitations, want %d (cap-1 model caveats plus the disclosure)", got, want)
	}
}

// TestDegradedHistoricalAnswerAtCapStillReturns is round-17 finding 1: the
// same defect class as the main-at-250 repro, re-entering through the
// append site the displacement fix did not cover.
//
// CHAOS-3781 adds standing historical disclosures to every non-current
// answer. Appended after the cap handling, they pushed a full list over
// ContextFabricLimitationsMaxCount and the whole investigation died at
// validation -- an answer lost to a disclosure, exactly what displacement
// exists to prevent.
//
// Drives the REAL Investigate path, at the cap, on a historical axis, with
// retrieval degraded, so every appender runs in the order production uses.
func TestDegradedHistoricalAnswerAtCapStillReturns(t *testing.T) {
	// Before the harness clock (time.Unix(100)), so the axis is genuinely
	// historical rather than a future bound the engine refuses.
	asOf := time.Unix(50, 0).UTC()
	engine, request := engineForDegradationOnAxis(t, true, modelLimitations(contractsv1.ContextFabricLimitationsMaxCount),
		TimeContext{Axis: TemporalValidTime, AsOf: &asOf})

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_hist_cap"}, request)
	if err != nil {
		t.Fatalf("Investigate() = %v, want an answer: a disclosure must cost a caveat, never the answer", err)
	}

	if got, want := len(result.Limitations), contractsv1.ContextFabricLimitationsMaxCount; got > want {
		t.Errorf("result carries %d limitations, over the cap %d", got, want)
	}
	if !hasRetrievalDegradedLimitation(result.Limitations) {
		t.Error("the degradation disclosure was lost")
	}
	// Every standing historical disclosure must survive too: they are the
	// statements a reader cannot reconstruct from anything else.
	for _, disclosure := range temporalLimitationsFor(TemporalValidTime) {
		if !alreadyStates(result.Limitations, disclosure) {
			t.Errorf("historical disclosure was lost: %q", disclosure)
		}
	}
	// One displacement per disclosure that had to be forced in.
	wantDisplaced := 1 + len(temporalLimitationsFor(TemporalValidTime))
	if result.LimitationsDisplaced != wantDisplaced {
		t.Errorf("result.LimitationsDisplaced = %d, want %d", result.LimitationsDisplaced, wantDisplaced)
	}
}

// TestServiceDisclosuresCannotFillTheCap pins the premise
// appendBoundedLimitations' unreachable branch rests on: there are always
// fewer service-authored disclosures than the cap, so a full list always
// holds a model caveat to displace. If a future disclosure set ever grew
// toward the cap, that branch would start silently dropping disclosures.
func TestServiceDisclosuresCannotFillTheCap(t *testing.T) {
	total := len(serviceAuthoredLimitations()) + 1 // plus the degradation disclosure
	if total >= contractsv1.ContextFabricLimitationsMaxCount {
		t.Fatalf("%d service-authored disclosures against a cap of %d: appendBoundedLimitations can no longer guarantee a displaceable model caveat",
			total, contractsv1.ContextFabricLimitationsMaxCount)
	}
}

// TestServiceDisclosuresAreNeverDisplaced proves the preference, not just
// that something gave way. A disclosure displaced to make room for another
// disclosure is a net loss of exactly the statements nothing else in the
// document carries.
func TestServiceDisclosuresAreNeverDisplaced(t *testing.T) {
	limitations := modelLimitations(contractsv1.ContextFabricLimitationsMaxCount - 1)
	limitations = append(limitations, retrievalDegradedLimitation)

	composed, displaced := appendBoundedLimitations(limitations, temporalLimitationsFor(TemporalObservedTime))

	if !hasRetrievalDegradedLimitation(composed) {
		t.Error("the degradation disclosure was displaced to make room for a historical one")
	}
	if displaced != len(temporalLimitationsFor(TemporalObservedTime)) {
		t.Errorf("displaced = %d, want %d", displaced, len(temporalLimitationsFor(TemporalObservedTime)))
	}
	if len(composed) != contractsv1.ContextFabricLimitationsMaxCount {
		t.Errorf("composed %d limitations, want the cap %d", len(composed), contractsv1.ContextFabricLimitationsMaxCount)
	}
}
