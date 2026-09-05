package contextfabric

// THE WIRE COST of seeding the unrunnable ranking cell `unavailable`.
//
// The answer-budget rule this repository holds itself to is that any change
// touching the served document states its byte delta against the 8192-byte
// budget and the 200-row bound, MEASURED rather than argued. This change adds
// no row -- it rewrites fields on a row the document already carried -- so the
// row bound is untouched, and the bytes go DOWN: the cell stops publishing a
// step, a step execution, an input class and five input fact kinds, and starts
// publishing one reason token.
//
// The "before" shape is not hand-typed. It is rebuilt from the derivation's
// OWN declaration table (`InputsForComputedStep`), which is exactly what
// `deriveRequirement` wrote onto this row before the fix, so the comparison is
// against the shipped shape rather than against a guess at it.

import (
	"encoding/json"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestSeedingTheUnrunnableRankingCellShrinksTheServedDocument measures the
// delta and asserts its SIGN, not a frozen constant.
//
// A frozen byte count would fail on any unrelated field rename and teach the
// next reader to re-baseline it, which is how a budget assertion stops being
// read. The property that matters is that this change cannot push a document
// over the budget, and that is a statement about the sign.
func TestSeedingTheUnrunnableRankingCellShrinksTheServedDocument(t *testing.T) {
	t.Parallel()

	frame := rankingFrameOverANamedSubject()
	result, _, _ := rankingInvestigation(t, nil, frame, QuestionFamilySubjectInvestigation, ShapeSingleSubject)

	after, published := planRequirementForObligation(result, ObligationRanking)
	if !published {
		t.Fatal("the served plan publishes no `ranking` requirement, so there is nothing to measure")
	}
	if after.Unavailable == "" {
		t.Fatal("the served `ranking` row is not the unavailable shape, so this measures the wrong thing")
	}

	// The pre-fix shape of the SAME row, rebuilt from the declaration table.
	inputs, declared := InputsForComputedStep(ComputedStepRankCohort)
	if !declared || len(inputs.FactKinds) == 0 {
		t.Fatal("rank_cohort declares no inputs, so the before shape cannot be rebuilt")
	}
	before := after
	before.Unavailable = ""
	before.Step = string(ComputedStepRankCohort)
	before.StepExecution = string(inputs.Execution)
	before.InputClass = string(inputs.Class)
	before.InputFactKinds = append([]contractsv1.ContextFabricFactKind(nil), inputs.FactKinds...)
	before.Quantifier = string(CompletionQuantifierAll)

	beforeBytes, err := json.Marshal(before)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	afterBytes, err := json.Marshal(after)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	delta := len(afterBytes) - len(beforeBytes)
	t.Logf("plan requirement row: before %d B, after %d B, delta %+d B (budget %d)",
		len(beforeBytes), len(afterBytes), delta, contractsv1.ContextFabricMaxSerializedBytesDefault)
	if delta >= 0 {
		t.Errorf("the unavailable row is %+d B against the served shape it replaces; this change was supposed to REMOVE a step, an execution, an input class and %d input kinds",
			delta, len(inputs.FactKinds))
	}

	// NO NEW ROWS. The row bound is a count, and a change that added a row
	// would consume the 200-row budget however few bytes it spent.
	planRows := 0
	if result.AnswerPlan != nil {
		planRows = len(result.AnswerPlan.Requirements)
	}
	coordinates := DeriveRequirementCoordinates(*frame)
	if planRows != len(coordinates) {
		t.Errorf("the served plan carries %d requirement rows for %d derived coordinates -- this change must rewrite a row, never add one", planRows, len(coordinates))
	}
	if planRows == 0 {
		t.Fatal("the served plan carries no requirement rows, so the row-count assertion above quantified over nothing")
	}
	t.Logf("row count: %d plan requirements for %d derived coordinates (200-row bound untouched)", planRows, len(coordinates))
}
