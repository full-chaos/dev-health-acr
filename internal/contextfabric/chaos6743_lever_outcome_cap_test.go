package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// withFullOutcomeRows makes every synthesis carry the v1 maximum of outcome
// rows, so a lever that appends its disclosure row without room produces a
// 201-row document the wire contract rejects.
func withFullOutcomeRows(engine *Engine) *Engine {
	base := engine.synthesizer
	engine.synthesizer = synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		result, err := base.Synthesize(ctx, principal, input)
		if err != nil {
			return result, err
		}
		rows := make([]RequirementOutcomeRow, contractsv1.ContextFabricPlanRequirementOutcomeMaxCount)
		for i := range rows {
			rows[i] = RequirementOutcomeRow{
				Stage:   contractsv1.ContextFabricOutcomeStagePlanning,
				Outcome: contractsv1.ContextFabricRequirementSatisfied,
				Impact:  contractsv1.ContextFabricAnswerImpactNone,
			}
		}
		result.Completeness.Outcomes = rows
		return result, nil
	})
	return engine
}

// assertLeverNeverServesAnInvalidAnswer: a lever is served only when the
// document it serves validates. A result with no room for the disclosure row
// is a bounded refusal, never a 500, and no lever event claims served.
func assertLeverNeverServesAnInvalidAnswer(t *testing.T, result InvestigationResult, err error, telemetry *recordingTelemetry) {
	t.Helper()
	if errors.Is(err, ErrInvalidResult) {
		t.Fatalf("a lever served a document the contract rejects: %v; claim_depth=%+v fact_row=%+v", err, telemetry.claimDepthNarrowings, telemetry.factRowTruncations)
	}
	if err != nil {
		var refusal AnswerBudgetRefusal
		if !errors.As(err, &refusal) {
			t.Fatalf("Investigate() error = %v, want a served answer or a budget refusal", err)
		}
		for _, event := range telemetry.claimDepthNarrowings {
			if event.Served {
				t.Fatalf("claim-depth event says served on a refused answer: %+v", event)
			}
		}
		for _, event := range telemetry.factRowTruncations {
			if event.Served {
				t.Fatalf("fact-row event says served on a refused answer: %+v", event)
			}
		}
		return
	}
	if len(result.Completeness.Outcomes) > contractsv1.ContextFabricPlanRequirementOutcomeMaxCount {
		t.Fatalf("served %d outcome rows, cap %d", len(result.Completeness.Outcomes), contractsv1.ContextFabricPlanRequirementOutcomeMaxCount)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result invalid: %v", err)
	}
}

func TestCHAOS6743ClaimDepthLeverWithNoOutcomeRoomNeverServesAnInvalidAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(chaos6743Engine(t, &calls, telemetry, chaos6743ProdShape))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6743Request())
	if len(telemetry.claimDepthNarrowings) == 0 {
		t.Fatalf("claim-depth lever never ran; the shape no longer reaches the seam under test")
	}
	assertLeverNeverServesAnInvalidAnswer(t, result, err, telemetry)
	for _, event := range telemetry.claimDepthNarrowings {
		if event.Declined == ClaimDepthInvalidResult {
			return
		}
	}
	t.Fatalf("no claim-depth event names %q; events=%+v", ClaimDepthInvalidResult, telemetry.claimDepthNarrowings)
}

func TestCHAOS6743FactRowLeverWithNoOutcomeRoomNeverServesAnInvalidAnswer(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(chaos6558Engine(t, &calls, telemetry, chaos6558ProdShape))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
	if len(telemetry.factRowTruncations) == 0 {
		t.Fatalf("fact-row lever never ran; the shape no longer reaches the seam under test")
	}
	assertLeverNeverServesAnInvalidAnswer(t, result, err, telemetry)
	for _, event := range telemetry.factRowTruncations {
		if event.Declined == FactRowTruncationInvalidResult {
			return
		}
	}
	t.Fatalf("no fact-row event names %q; events=%+v", FactRowTruncationInvalidResult, telemetry.factRowTruncations)
}

func TestCHAOS6743CandidateLeverWithNoOutcomeRoomDeclinesAsInvalidResult(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := withFullOutcomeRows(outcomeCohortEngineWithCandidates(t, budgetStageCohort(6), 2, 6, budgetStageOptions(12, time.Second), &calls, telemetry))
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	assertLeverNeverServesAnInvalidAnswer(t, result, err, telemetry)
	for _, event := range telemetry.planNarrowings {
		if event.OutcomeReductionDeclined == OutcomeReductionInvalidResult {
			return
		}
	}
	t.Fatalf("no plan-narrowing event names %q; events=%+v", OutcomeReductionInvalidResult, telemetry.planNarrowings)
}
