package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A row cut fits the answer under the byte ceiling and the route then measures
// what it sends. Prod fitted an answer at 65,498 of 65,536 bytes, the document
// sent measured 65,550, and a true smaller answer was refused with a 413. The
// fit must measure what is sent, so an answer that a few more cut rows would
// fit is never refused.
//
// The late writer here is the one a repository-restricted caller reaches: the
// census scope limitation added when the document is finalized.
func investigateAtCap(t *testing.T, maxBytes int64, scopeRecorded bool) (InvestigationResult, error) {
	t.Helper()
	return investigateAtCapWithHook(t, maxBytes, scopeRecorded, nil)
}

func investigateAtCapWithHook(t *testing.T, maxBytes int64, scopeRecorded bool, hook func(InvestigationResult) InvestigationResult) (InvestigationResult, error) {
	t.Helper()
	calls := 0
	shape := chaos6558Shape{maxBytes: maxBytes}
	engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, shape)
	engine.servedLateHook = hook
	ctx := context.Background()
	if scopeRecorded {
		ctx = WithWorkItemCensusRepositoryScopeRecorder(ctx)
		RecordWorkItemCensusRepositoryScope(ctx)
	}
	return engine.Investigate(ctx, storage.Principal{OrgID: "org_1"}, chaos6558Request())
}

func servedBytes(t *testing.T, result InvestigationResult) int64 {
	t.Helper()
	measurement, err := contractsv1.MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatal(err)
	}
	return measurement.Bytes
}

// seededNearCapCeiling finds, by running the real engine without the late
// writer, a ceiling that the row cut fits with fewer than 100 bytes to spare.
func seededNearCapCeiling(t *testing.T) int64 {
	t.Helper()
	for ceiling := int64(40000); ceiling <= 66000; ceiling += 3 {
		result, err := investigateAtCap(t, ceiling, false)
		if err != nil {
			continue
		}
		if headroom := ceiling - servedBytes(t, result); headroom >= 1 && headroom < 100 {
			return ceiling
		}
	}
	t.Fatal("no ceiling in range lands the row cut within 100 bytes of it; the fixture cannot seed the case")
	return 0
}

func TestARowCutThatLandsNearTheCeilingIsNotRefusedByTheWritersAfterIt(t *testing.T) {
	ceiling := seededNearCapCeiling(t)
	result, err := investigateAtCap(t, ceiling, true)
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused (%s) at %d bytes against a %d-byte ceiling after a row cut that fit before the final writers: a smaller true answer exists", refusal.Overrun, refusal.MeasuredBytes, refusal.MaxSerializedBytes)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	if got := servedBytes(t, result); got > ceiling {
		t.Fatalf("served %d bytes against a %d-byte ceiling", got, ceiling)
	}
	if len(result.ClaimedFacts) != chaos6558Facts {
		t.Fatalf("served %d claims, want all %d: only rows are cut", len(result.ClaimedFacts), chaos6558Facts)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricWorkItemCensusRepositoryScopeLimitation {
			found = true
		}
	}
	if !found {
		t.Fatalf("the late writer's sentence is not in the served answer, so the case did not exercise it: %q", result.Limitations)
	}
}

// A writer the fit has never heard of adds bytes after the fit, inside the
// finalization the route's document comes out of. The answer sits within 100
// bytes of the ceiling, so any late bytes overrun it. The decisive measure is
// the document as sent: the engine cuts one more row and measures again until
// it fits, so no named writer is needed for the answer to be served.
func lateBytes(n int) func(InvestigationResult) InvestigationResult {
	return func(result InvestigationResult) InvestigationResult {
		result.Warnings = append(append([]string{}, result.Warnings...), strings.Repeat("w", n))
		return result
	}
}

func TestAnUnknownWriterAfterTheFitDoesNotMakeASmallerTrueAnswerRefused(t *testing.T) {
	ceiling := seededNearCapCeiling(t)
	const added = 400
	result, err := investigateAtCapWithHook(t, ceiling, false, lateBytes(added))
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused (%s) at %d bytes against %d after a writer added %d bytes past the fit: a smaller true answer exists", refusal.Overrun, refusal.MeasuredBytes, refusal.MaxSerializedBytes, added)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	if got := servedBytes(t, result); got > ceiling {
		t.Fatalf("served %d bytes against a %d-byte ceiling", got, ceiling)
	}
	if len(result.ClaimedFacts) != chaos6558Facts {
		t.Fatalf("served %d claims, want all %d: only rows are cut", len(result.ClaimedFacts), chaos6558Facts)
	}
	cuts := 0
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricFactRowTruncationLimitation(limitation) {
			cuts++
		}
	}
	if cuts != 1 {
		t.Fatalf("the served answer carries %d row-cut disclosures, want exactly 1 stating the final cut: %q", cuts, result.Limitations)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the re-cut answer is not a valid result: %v", err)
	}
}

// When even one row per table does not fit the document as sent, the refusal
// stands and says nothing was left to cut.
func TestARefusalSaysWhenNothingWasLeftToCut(t *testing.T) {
	_, err := investigateAtCapWithHook(t, chaos6558MaxBytes, false, lateBytes(60000))
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a budget refusal", err)
	}
	if !refusal.NothingLeftToCut || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
		t.Fatalf("refusal = %+v, want a bytes refusal that says nothing was left to cut", refusal)
	}
}
