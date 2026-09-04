package answerprojection

import (
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE PROJECTED-SURFACE ORACLE.
//
// The defect it pins is the one that made an earlier version of this design
// unsound: a canonical answer whose every row reads `satisfied`, put through
// a projection that then drops content. Copying the canonical completeness
// through leaves the SERVED document claiming `complete` while it has lost
// members and whole groups -- measure, then shrink somewhere the measurement
// cannot see, relocated one boundary down from where it was first found.
func TestAProjectionThatDropsContentCannotServeACompleteAnswer(t *testing.T) {
	t.Parallel()
	satisfied := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: "state/member/team",
		Obligation:  "state",
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}
	projection := contractsv1.ContextFabricAnswerProjection{
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			TerminalStatus: contractsv1.ContextFabricInvestigationComplete,
			State:          contractsv1.ContextFabricAnswerCompletenessComplete,
			Outcomes:       []contractsv1.ContextFabricPlanRequirementOutcomeRow{satisfied},
		},
		ProjectionBudget: contractsv1.ContextFabricProjectionBudget{
			// A whole group vanished, and two members with it.
			CohortGroupsOmitted:  1,
			CohortMembersOmitted: 2,
		},
	}

	served := appendProjectionOutcomes(projection)

	if served.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("the served answer claims %q while its own document lost a whole group and two members", served.Completeness.State)
	}
	// APPEND, never rewrite: the canonical row is carried through byte for
	// byte. A projection that edited what the investigation established
	// would let the stored and served answers disagree about it.
	if len(served.Completeness.Outcomes) < 1 || !reflect.DeepEqual(served.Completeness.Outcomes[0], satisfied) {
		t.Fatalf("the canonical row was rewritten: %+v", served.Completeness.Outcomes)
	}
	// A STRICT superset: the stage added rows rather than replacing them.
	if len(served.Completeness.Outcomes) <= len(projection.Completeness.Outcomes) {
		t.Fatalf("the projection narrowed and appended nothing: %d rows before, %d after",
			len(projection.Completeness.Outcomes), len(served.Completeness.Outcomes))
	}
	// The vanished GROUP is named in its own right, not folded into a
	// members count. A generic counter standing in for a class is what a
	// closed vocabulary exists to replace.
	appended := served.Completeness.Outcomes[1:]
	if len(appended) != 2 {
		t.Fatalf("appended %d rows for two distinct omissions, want 2: %+v", len(appended), appended)
	}
	for _, row := range appended {
		if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
			t.Fatalf("an appended row is not legal: %v", err)
		}
		if row.Stage != contractsv1.ContextFabricOutcomeStageProjection {
			t.Fatalf("appended row carries stage %q, want projection", row.Stage)
		}
		if row.Requirement != "" {
			t.Fatalf("the projection attributed a cut to requirement %q; it cuts by its own budget over a finished document and does not know which requirement a dropped item served", row.Requirement)
		}
		// THE REDUCTION STEP MUST BE ON THE ROW THE PROJECTION SERVES.
		//
		// This assertion is here because it was NOT, and a review round
		// found the gap by mutation: replacing the projection's derivation
		// call with a bare `return row` stripped refinements from every
		// projection cut and no test failed. Validating the row is not the
		// same as pinning what it carries -- the refinement list is
		// optional, so an empty one is legal and a validator can never
		// notice its absence. Only an assertion at the consumer can.
		if len(row.Refinements) != 1 {
			t.Fatalf("a projection cut served %d of %d and recorded %d refinements, want exactly 1: "+
				"the two counts are a before and an after with the step between them erased",
				row.Served, row.Declared, len(row.Refinements))
		}
		step := row.Refinements[0]
		if step.Stage != contractsv1.ContextFabricOutcomeStageProjection {
			t.Errorf("refinement stage = %q, want projection", step.Stage)
		}
		// The cause is the caller's BYTE ceiling -- the projection cuts to a
		// budget, it runs no selection and reads no coverage event.
		if step.Overrun != contractsv1.ContextFabricBudgetOverrunBytes {
			t.Errorf("refinement overrun = %q, want bytes", step.Overrun)
		}
		if step.Basis != "" || step.Coverage != "" {
			t.Errorf("the refinement invented causes the projection does not have: basis=%q coverage=%q", step.Basis, step.Coverage)
		}
		// And it must reconcile with the row's own numbers, which is what
		// makes the chain an audit rather than a decoration.
		if step.Before != row.Declared || step.After != row.Served {
			t.Errorf("refinement runs %d->%d but the row declared %d and served %d",
				step.Before, step.After, row.Declared, row.Served)
		}
	}
}

// A projection that drops NOTHING must not invent a narrowing.
//
// The inverse of the test above, and the one that stops the fix from becoming
// "always report partial": publishing a narrowing that did not happen is the
// same defect as suppressing one that did.
func TestAProjectionThatDropsNothingAppendsNothing(t *testing.T) {
	t.Parallel()
	satisfied := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: "state/member/team",
		Obligation:  "state",
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
		Impact:      contractsv1.ContextFabricAnswerImpactNone,
	}
	projection := contractsv1.ContextFabricAnswerProjection{
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			TerminalStatus: contractsv1.ContextFabricInvestigationComplete,
			State:          contractsv1.ContextFabricAnswerCompletenessComplete,
			Outcomes:       []contractsv1.ContextFabricPlanRequirementOutcomeRow{satisfied},
		},
	}
	served := appendProjectionOutcomes(projection)
	if len(served.Completeness.Outcomes) != 1 {
		t.Fatalf("appended %d rows to a projection that dropped nothing", len(served.Completeness.Outcomes)-1)
	}
	if served.Completeness.State != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Fatalf("state = %q, want complete", served.Completeness.State)
	}
}

// EVERY omission counter this budget can declare must be nameable.
//
// Reflection rather than a list, and the reason is the shape that has
// defeated this repository's guards repeatedly: a hand-written list of the
// counters someone thought of is only as complete as their memory, and the
// next counter added to the budget joins it silently. A projection that
// declares itself truncated on a counter with no entry would announce a
// truncation it cannot name, which is precisely the generic bit the outcome
// vocabulary replaces.
func TestEveryProjectionOmissionCounterCanBeNamed(t *testing.T) {
	t.Parallel()
	named := map[string]bool{}
	for _, omission := range projectionOmissions(contractsv1.ContextFabricProjectionBudget{}) {
		if named[omission.Field] {
			t.Errorf("%s is named twice; each counter is stated once", omission.Field)
		}
		named[omission.Field] = true
	}
	// FullResultOmitted is a bool, not a count, and is appended by
	// MarkFullResultOmitted rather than here -- it is set after Project has
	// already run. Declared by name so it is an exemption someone chose
	// rather than one the walk silently allowed.
	named["FullResultOmitted"] = true
	// Truncated is the DERIVED summary of every counter beside it, not an
	// omission of its own; naming it would append a row for the fact that
	// other rows exist.
	named["Truncated"] = true

	budgetType := reflect.TypeOf(contractsv1.ContextFabricProjectionBudget{})
	var unnamed []string
	for index := 0; index < budgetType.NumField(); index++ {
		field := budgetType.Field(index)
		// The counters are the int fields plus the two bools above. Any
		// other kind (the selection basis, a closed token) describes HOW
		// something was cut, not that it was.
		if field.Type.Kind() != reflect.Int && field.Type.Kind() != reflect.Bool {
			continue
		}
		if !named[field.Name] {
			unnamed = append(unnamed, field.Name)
		}
	}
	if len(unnamed) > 0 {
		t.Fatalf("%d projection budget counters have no outcome row to name them:\n  %s\n\n"+
			"A projection can declare itself truncated on each of these. One with no entry announces a truncation it cannot name.",
			len(unnamed), strings.Join(unnamed, "\n  "))
	}
	// A guard that quantifies over an empty population proves nothing.
	if len(named) < 5 {
		t.Fatalf("the walk found only %d named counters; it is not reaching the budget", len(named))
	}
}

// Dropping the caller's requested copy of the canonical result is a cut like
// any other, and it happens OUTSIDE the function that owns the invariant --
// which is where an append is easiest to forget.
func TestMarkingTheFullResultOmittedAppendsAndReDerives(t *testing.T) {
	t.Parallel()
	projection := contractsv1.ContextFabricAnswerProjection{
		Completeness: contractsv1.ContextFabricAnswerCompleteness{
			TerminalStatus: contractsv1.ContextFabricInvestigationComplete,
			State:          contractsv1.ContextFabricAnswerCompletenessNotDerived,
		},
	}
	MarkFullResultOmitted(&projection)
	if len(projection.Completeness.Outcomes) != 1 {
		t.Fatalf("appended %d rows, want 1", len(projection.Completeness.Outcomes))
	}
	if projection.Completeness.State != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Fatalf("state = %q, want partial", projection.Completeness.State)
	}
	if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(projection.Completeness.Outcomes[0]); err != nil {
		t.Fatalf("the appended row is not legal: %v", err)
	}
}
