package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE WIRING between the requirement derivation and the plan's fact kinds.
//
// The unit pin beside this one (computed_step_input_reads_test.go) hands the
// rows to PlanAnswer directly, which proves the plan stage reads them and
// proves nothing about whether the ENGINE hands them over. This file drives
// the public entry point and reads the served document.
//
// It lives in its own file rather than beside the plan-requirement consumer
// tests it was first written into, because it is about a different seam and
// because two branches appending to one file's tail is a merge conflict with
// no disagreement in it.

// rankingRequirementRows is twoRequirementRows with its computed row replaced
// by a SERVED, SERVER-EXECUTED, fact-kind-consuming one.
//
// The shared fixture's computed row is `membership_cardinality`, which
// consumes the resolved member set and declares no fact kind, so it can say
// nothing about whether declared inputs reach the plan. This one declares the
// ranking formula's own kinds, which is the only shape the input consumer acts
// on.
func rankingRequirementRows() []DerivedRequirement {
	rows := twoRequirementRows()
	rows[1] = DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationRanking, Role: SubjectRoleMember, Subject: SubjectTeam,
		},
		Kind:           ObligationKindComputed,
		Step:           ComputedStepRankCohort,
		InputClass:     ComputedInputFactKinds,
		InputFactKinds: append([]FactKind(nil), cohortRankingFormulaKinds...),
		StepExecution:  ComputedStepServerExecuted,
		Scope:          CompletionScopeEachMember,
		Quantifier:     CompletionQuantifierAll,
	}
	return rows
}

// TestInvestigatePlansTheComputedStepInputsTheDerivationDeclared is the WIRING
// pin, and it exists because the unit pin below the engine cannot be it.
//
// TestPlanFactKindsPlansEveryDeclaredComputedStepInput drives PlanAnswer with
// requirement rows handed to it directly. That proves the plan stage reads
// them; it proves nothing about whether the ENGINE hands them over, and
// dropping one field from the engine's PlanAnswerInput literal would leave it
// green -- constructing a struct literal in a test proves nothing about the
// production call site. So this drives Investigate and reads the fact kinds
// off the SERVED document.
//
// THE FAMILY MUST NOT BE A COHORT ONE, asserted rather than assumed. On a
// cohort family the unconditional ranking injection names the same five kinds,
// so the assertion would pass with the whole input consumer deleted. If this
// fixture's question ever resolves to a cohort family the test fails LOUDLY
// rather than continuing to look green while proving nothing.
func TestInvestigatePlansTheComputedStepInputsTheDerivationDeclared(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	deriver := &fixedRequirementDeriver{rows: rankingRequirementRows()}

	engine := planRequirementEngine(t, deriver, &calls, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil && !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v", err)
	}
	if deriver.calls == 0 {
		t.Fatal("the engine never called the requirement deriver, so the plan's kinds cannot have come from the rows")
	}
	if result.AnswerPlan == nil {
		t.Fatal("the served answer carries no answer plan")
	}

	definition, found := LookupQuestionFamily(result.AnswerPlan.Family)
	if !found {
		t.Fatalf("the served plan names family %q, which the registry does not carry", result.AnswerPlan.Family)
	}
	if isCohortSubjectAxis(definition.SubjectAxis) {
		t.Fatalf("this fixture resolved to COHORT family %q (axis %q); the unconditional ranking injection would supply these kinds and this test could not attribute them to the requirement rows",
			result.AnswerPlan.Family, definition.SubjectAxis)
	}

	want := ComputedStepInputReads(deriver.rows)
	if len(want) == 0 {
		t.Fatal("the fixture's rows declare no computed-step input, so this test asserted nothing")
	}
	for _, kind := range want {
		found := false
		for _, planned := range result.AnswerPlan.FactKinds {
			if planned == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the served plan's fact kinds %v do not include %q, which the derivation declared as an input of a server-executed computed step", result.AnswerPlan.FactKinds, kind)
		}
	}
	t.Logf("family %s (axis %s): served plan kinds %v, declared inputs %v", result.AnswerPlan.Family, definition.SubjectAxis, result.AnswerPlan.FactKinds, want)
}
