package contextfabric

import (
	"context"
	"errors"
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
	calls := 0
	shape := chaos6558Shape{maxBytes: maxBytes}
	engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, shape)
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
