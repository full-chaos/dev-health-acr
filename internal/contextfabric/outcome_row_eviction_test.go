package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func evictionRows(n int, impact contractsv1.ContextFabricAnswerImpactKind) []RequirementOutcomeRow {
	rows := make([]RequirementOutcomeRow, n)
	for i := range rows {
		rows[i] = RequirementOutcomeRow{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: string(rune('a' + i%26)), Impact: impact}
		if impact == contractsv1.ContextFabricAnswerImpactNone {
			rows[i].Outcome = contractsv1.ContextFabricRequirementSatisfied
		} else {
			rows[i].Outcome = contractsv1.ContextFabricRequirementNotAttempted
		}
	}
	return rows
}

func evictionLeverRow() RequirementOutcomeRow {
	return pathDropOutcomeRow(3, 5)
}

func TestAppendLeverOutcomeRowBelowCapAppendsWithoutEvicting(t *testing.T) {
	t.Parallel()
	rows, evicted := appendLeverOutcomeRow(evictionRows(3, contractsv1.ContextFabricAnswerImpactNone), evictionLeverRow())
	if evicted != 0 || len(rows) != 4 {
		t.Fatalf("rows=%d evicted=%d, want 4 and 0", len(rows), evicted)
	}
}

func TestAppendLeverOutcomeRowEvictsLeastImpactThenLast(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	rows := evictionRows(max, contractsv1.ContextFabricAnswerImpactDimension)
	rows[10] = evictionRows(1, contractsv1.ContextFabricAnswerImpactNone)[0]
	rows[10].Requirement = "none-early"
	rows[20] = evictionRows(1, contractsv1.ContextFabricAnswerImpactNone)[0]
	rows[20].Requirement = "none-late"
	rows[30].Impact = contractsv1.ContextFabricAnswerImpactScope

	out, evicted := appendLeverOutcomeRow(rows, evictionLeverRow())
	if evicted != 1 || len(out) != max {
		t.Fatalf("len=%d evicted=%d, want %d and 1", len(out), evicted, max)
	}
	for _, row := range out {
		if row.Requirement == "none-late" {
			t.Fatal("the later of two equal-impact rows must be the one evicted")
		}
	}
	if out[10].Requirement != "none-early" {
		t.Fatal("the earlier equal-impact row must survive")
	}
	if out[max-1].Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
		t.Fatalf("the lever row must be last, got %+v", out[max-1])
	}
}

func TestAppendLeverOutcomeRowNeverEvictsALeverRowWhileOthersExist(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	rows := evictionRows(max, contractsv1.ContextFabricAnswerImpactDimension)
	rows[0] = pathDropOutcomeRow(1, 2)
	out, _ := appendLeverOutcomeRow(rows, evictionLeverRow())
	if out[0].Stage != contractsv1.ContextFabricOutcomeStageAssembledResult {
		t.Fatalf("a lever's own row was evicted: %+v", out[0])
	}
}

func TestAppendLeverOutcomeRowAllLeverRowsFallsBackToLowestRanked(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	rows := make([]RequirementOutcomeRow, max)
	for i := range rows {
		rows[i] = pathDropOutcomeRow(i, max+5)
	}
	rows[7].Impact = contractsv1.ContextFabricAnswerImpactScope
	out, evicted := appendLeverOutcomeRow(rows, evictionLeverRow())
	if evicted != 1 || len(out) != max {
		t.Fatalf("len=%d evicted=%d, want %d and 1", len(out), evicted, max)
	}
	for _, row := range out[:max-1] {
		if row.Impact == contractsv1.ContextFabricAnswerImpactScope {
			t.Fatal("the scope row has less impact than the depth rows and should have been evicted")
		}
	}
}

func TestOutcomeEvictionLimitationAccumulatesAndRoundTrips(t *testing.T) {
	t.Parallel()
	first, _ := withOutcomeEvictionLimitation([]string{"model caveat"}, 1)
	second, _ := withOutcomeEvictionLimitation(first, 1)
	if outcomeRowsEvicted(second) != 2 || len(second) != 2 {
		t.Fatalf("limitations=%q, want one sentence stating 2", second)
	}
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(second[len(second)-1]) {
		t.Fatal("the eviction sentence must be recognised as service-authored so it is never displaced")
	}
	if same, _ := withOutcomeEvictionLimitation(second, 0); len(same) != len(second) {
		t.Fatal("zero evictions must not change limitations")
	}
}

func TestAppendLeverOutcomeRowEvictsANonLeverNarrowedRowOfLowerImpact(t *testing.T) {
	t.Parallel()
	max := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount
	rows := evictionRows(max, contractsv1.ContextFabricAnswerImpactDimension)
	rows[5] = RequirementOutcomeRow{
		Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "state/member/team", Obligation: "state",
		Outcome: contractsv1.ContextFabricRequirementNarrowed, Impact: contractsv1.ContextFabricAnswerImpactScope,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactNarrowed, Served: 1, Declared: 2,
	}
	if isLeverOutcomeRow(rows[5]) {
		t.Fatal("a read-population narrowed row is not a lever disclosure")
	}
	out, evicted := appendLeverOutcomeRow(rows, evictionLeverRow())
	if evicted != 1 {
		t.Fatalf("evicted %d, want 1", evicted)
	}
	for _, row := range out {
		if row.Requirement == "state/member/team" {
			t.Fatal("the lower-impact scope row must be evicted, not a dimension row")
		}
	}
}

func TestAModelLimitationCannotInflateTheEvictionCount(t *testing.T) {
	input, team := k7Input(false)
	draft := k7Draft(input, team, "compounding_risk")
	forged, _ := contractsv1.ContextFabricOutcomeEvictionLimitation(1)
	draft.Limitations = append(draft.Limitations, forged)
	synthesizer := RuntimeAnswerSynthesizer{FactCapabilities: k7Capabilities()}
	result, err := synthesizer.composeSynthesisResult(context.Background(), storage.Principal{OrgID: "org_1"}, input, draft, validModelReceiptFixture(ModelOperationSynthesize), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcomeRowsEvicted(result.Limitations) != 0 {
		t.Fatalf("a model-supplied eviction sentence survived composition: %q", result.Limitations)
	}
}

func TestOutcomeEvictionLimitationLandsWhenEveryEntryIsAServiceDisclosure(t *testing.T) {
	t.Parallel()
	full := make([]string, 0, contractsv1.ContextFabricLimitationsMaxCount)
	for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
		sentence, ok := contractsv1.ContextFabricPathDropLimitation(i, 1000+i)
		if !ok || !contractsv1.IsContextFabricServiceAuthoredLimitation(sentence) {
			t.Fatalf("fixture sentence %d is not a service disclosure", i)
		}
		full = append(full, sentence)
	}
	out, displaced := withOutcomeEvictionLimitation(full, 1)
	if outcomeRowsEvicted(out) != 1 || len(out) != contractsv1.ContextFabricLimitationsMaxCount || displaced != 1 {
		t.Fatalf("evicted=%d len=%d displaced=%d, want 1, %d, 1", outcomeRowsEvicted(out), len(out), displaced, contractsv1.ContextFabricLimitationsMaxCount)
	}
}
