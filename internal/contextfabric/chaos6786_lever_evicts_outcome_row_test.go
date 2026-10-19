package contextfabric

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// assertLeverEvictsToServe: at the outcome row cap a stage-3 lever evicts one
// row to make room for its own disclosure row, so the answer is served rather
// than refused as over budget (D6080).
func assertLeverEvictsToServe(t *testing.T, result InvestigationResult, err error) {
	t.Helper()
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused at the outcome cap: overrun=%s measured=%d; a servable answer must beat the 413", refusal.Overrun, refusal.MeasuredBytes)
		}
		t.Fatalf("Investigate() error = %v, want a served answer", err)
	}
	rows := result.Completeness.Outcomes
	if len(rows) != contractsv1.ContextFabricPlanRequirementOutcomeMaxCount {
		t.Fatalf("served %d outcome rows, want exactly the cap %d", len(rows), contractsv1.ContextFabricPlanRequirementOutcomeMaxCount)
	}
	last := rows[len(rows)-1]
	if last.Outcome == contractsv1.ContextFabricRequirementSatisfied {
		t.Fatalf("last row is a padded satisfied row %+v; the lever disclosure row is missing", last)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
	for _, limitation := range result.Limitations {
		if strings.Contains(limitation, "evicted") {
			return
		}
	}
	t.Fatalf("no limitation says outcome rows were evicted; limitations=%q", result.Limitations)
}

func TestCHAOS6786ClaimDepthLeverEvictsAnOutcomeRowAtTheCap(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(chaos6743Engine(t, &calls, telemetry, chaos6743ProdShape))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6743Request())
	assertLeverEvictsToServe(t, result, err)
}

func TestCHAOS6786FactRowLeverEvictsAnOutcomeRowAtTheCap(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(chaos6558Engine(t, &calls, telemetry, chaos6558ProdShape))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	assertLeverEvictsToServe(t, result, err)
}

func TestCHAOS6786PathDropLeverEvictsAnOutcomeRowAtTheCap(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(chaos6558PathEngine(t, &calls, telemetry, chaos6558PathShape{maxBytes: chaos6558PathMaxBytes}))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	assertLeverEvictsToServe(t, result, err)
}

func TestCHAOS6786CandidateLeverEvictsAnOutcomeRowAtTheCap(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(outcomeCohortEngineWithCandidates(t, budgetStageCohort(6), 2, 6, budgetStageOptions(12, time.Second), &calls, telemetry))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	assertLeverEvictsToServe(t, result, err)
}
