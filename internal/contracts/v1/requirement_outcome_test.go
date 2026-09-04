package v1

import (
	"strings"
	"testing"
)

// legalRow is a row every predicate below starts from, so each case changes
// exactly one thing and a failure names that thing.
func legalRow() ContextFabricPlanRequirementOutcomeRow {
	return ContextFabricPlanRequirementOutcomeRow{
		Stage:         ContextFabricOutcomeStageAssembledResult,
		Outcome:       ContextFabricRequirementNarrowed,
		Impact:        ContextFabricAnswerImpactScope,
		CauseOverrun:  ContextFabricBudgetOverrunItems,
		CauseObserved: true,
		Served:        3,
		Declared:      9,
	}
}

// THE PAIRING RULE, BOTH DIRECTIONS.
//
// Only the permissive half tends to get written -- "a narrowed row must
// declare an impact" -- and on its own it is a loosening: it accepts a
// satisfied row that also claims one, which states that the reader lost
// something on a requirement that was served in full. Both halves, or the
// rule is half-enforced.
func TestOutcomeAndImpactMustPairInBothDirections(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(*ContextFabricPlanRequirementOutcomeRow)
		reject bool
	}{
		{"narrowed with a real impact", func(r *ContextFabricPlanRequirementOutcomeRow) {}, false},
		{"narrowed claiming no impact", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Impact = ContextFabricAnswerImpactNone
		}, true},
		{"unavailable claiming no impact", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementUnavailable
			r.Impact = ContextFabricAnswerImpactNone
		}, true},
		{"not_attempted claiming no impact", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotAttempted
			r.Impact = ContextFabricAnswerImpactNone
			r.Served, r.Declared = 0, 9
		}, true},
		{"satisfied with no impact and no cause", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementSatisfied
			r.Impact = ContextFabricAnswerImpactNone
			r.CauseOverrun, r.CauseObserved = "", false
			r.Served, r.Declared = 9, 9
		}, false},
		{"satisfied claiming an impact", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementSatisfied
			r.CauseOverrun, r.CauseObserved = "", false
			r.Served, r.Declared = 9, 9
		}, true},
		{"not_applicable claiming an impact", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementNotApplicable
			r.CauseOverrun, r.CauseObserved = "", false
			r.Served, r.Declared = 0, 0
		}, true},
		{"satisfied naming a cause", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementSatisfied
			r.Impact = ContextFabricAnswerImpactNone
			r.Served, r.Declared = 9, 9
		}, true},
		{"narrowed naming no cause at all", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.CauseOverrun, r.CauseObserved = "", false
		}, true},
		{"satisfied claiming an observed cause it does not name", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Outcome = ContextFabricRequirementSatisfied
			r.Impact = ContextFabricAnswerImpactNone
			r.CauseOverrun = ""
			r.Served, r.Declared = 9, 9
		}, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			row := legalRow()
			testCase.mutate(&row)
			err := ValidateContextFabricPlanRequirementOutcomeRow(row)
			if testCase.reject && err == nil {
				t.Fatalf("row %+v was accepted; it must be rejected", row)
			}
			if !testCase.reject && err != nil {
				t.Fatalf("row %+v was rejected: %v", row, err)
			}
		})
	}
}

// A closed vocabulary is closed over its MEMBERS. What may CARRY it is a
// separate question, and left unstated it is open by default -- which is how
// a permitted key holding a token-shaped value from no vocabulary at all
// passes a gate that only checks key names.
func TestEveryTokenFieldIsCheckedAgainstItsOwnVocabulary(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(*ContextFabricPlanRequirementOutcomeRow)
	}{
		{"stage", func(r *ContextFabricPlanRequirementOutcomeRow) { r.Stage = "assembly" }},
		{"outcome", func(r *ContextFabricPlanRequirementOutcomeRow) { r.Outcome = "reduced" }},
		{"impact", func(r *ContextFabricPlanRequirementOutcomeRow) { r.Impact = "coverage" }},
		{"cause_overrun", func(r *ContextFabricPlanRequirementOutcomeRow) { r.CauseOverrun = "too_big" }},
		{"cause_coverage", func(r *ContextFabricPlanRequirementOutcomeRow) { r.CauseCoverage = "fact_missing" }},
		{"cause_narrowing", func(r *ContextFabricPlanRequirementOutcomeRow) { r.CauseNarrowing = "first_n" }},
		{"obligation", func(r *ContextFabricPlanRequirementOutcomeRow) {
			r.Requirement, r.Obligation = "throughput/member/team", "throughput"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			row := legalRow()
			testCase.mutate(&row)
			if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err == nil {
				t.Fatalf("a %s outside every vocabulary was accepted: %+v", testCase.name, row)
			}
		})
	}
}

// The identity is the COORDINATE. A row whose identity disagrees with its own
// obligation gives a reader two answers to "which requirement is this".
func TestRequirementIdentityMustBeItsOwnCoordinate(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		requirement string
		obligation  string
		reject      bool
	}{
		{"a well-formed coordinate", "state/member/team", "state", false},
		{"identity disagreeing with its obligation", "health/member/team", "state", true},
		{"too few segments", "state/member", "state", true},
		{"too many segments", "state/member/team/extra", "state", true},
		{"an empty middle segment", "state//team", "state", true},
		{"an obligation with no identity", "", "state", true},
		{"an identity with no obligation", "state/member/team", "", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			row := legalRow()
			row.Requirement, row.Obligation = testCase.requirement, testCase.obligation
			err := ValidateContextFabricPlanRequirementOutcomeRow(row)
			if testCase.reject != (err != nil) {
				t.Fatalf("requirement %q / obligation %q: err = %v, want rejected = %v",
					testCase.requirement, testCase.obligation, err, testCase.reject)
			}
		})
	}
}

// TOTALITY. Every legal multiset returns a vocabulary member, and no arm
// falls through to a default.
//
// The trial count is asserted, because a generator that stopped producing
// inputs would leave this test green while proving nothing -- the same shape
// as a property test whose every trial takes an early exit.
func TestTheCompletenessDerivationIsTotalOverEveryOutcomeMultiset(t *testing.T) {
	t.Parallel()
	vocabulary := ContextFabricPlanRequirementOutcomeVocabulary()
	trials := 0
	var rowsFor func(depth int, prefix []ContextFabricPlanRequirementOutcomeRow)
	rowsFor = func(depth int, prefix []ContextFabricPlanRequirementOutcomeRow) {
		state := DeriveContextFabricAnswerCompletenessState(prefix)
		if !ValidContextFabricAnswerCompletenessState(state) {
			t.Fatalf("a multiset of %d rows derived %q, which is not a vocabulary member", len(prefix), state)
		}
		trials++
		if depth == 0 {
			return
		}
		for _, outcome := range vocabulary {
			row := legalRow()
			row.Outcome = outcome
			rowsFor(depth-1, append(prefix, row))
		}
	}
	rowsFor(3, nil)
	// 1 + 5 + 25 + 125 multisets by construction.
	if trials != 156 {
		t.Fatalf("the generator produced %d multisets, want 156 -- a generator that stopped producing inputs leaves this test green while proving nothing", trials)
	}
}

// The three states an outcome set can add up to, each pinned to the rule that
// produces it -- and degraded pinned as ABSORBING, so no later row can walk
// an unavailable requirement back to partial.
func TestCompletenessDerivesFromTheOutcomeSet(t *testing.T) {
	t.Parallel()
	row := func(outcome ContextFabricPlanRequirementOutcome) ContextFabricPlanRequirementOutcomeRow {
		r := legalRow()
		r.Outcome = outcome
		return r
	}
	for _, testCase := range []struct {
		name string
		rows []ContextFabricPlanRequirementOutcomeRow
		want ContextFabricAnswerCompletenessState
	}{
		{"no rows at all", nil, ContextFabricAnswerCompletenessNotDerived},
		{"every row satisfied", []ContextFabricPlanRequirementOutcomeRow{
			row(ContextFabricRequirementSatisfied), row(ContextFabricRequirementNotApplicable),
		}, ContextFabricAnswerCompletenessComplete},
		{"one narrowed", []ContextFabricPlanRequirementOutcomeRow{
			row(ContextFabricRequirementSatisfied), row(ContextFabricRequirementNarrowed),
		}, ContextFabricAnswerCompletenessPartial},
		{"one not_attempted", []ContextFabricPlanRequirementOutcomeRow{
			row(ContextFabricRequirementNotAttempted),
		}, ContextFabricAnswerCompletenessPartial},
		{"one unavailable", []ContextFabricPlanRequirementOutcomeRow{
			row(ContextFabricRequirementNarrowed), row(ContextFabricRequirementUnavailable),
		}, ContextFabricAnswerCompletenessDegraded},
		{"unavailable followed by satisfied rows", []ContextFabricPlanRequirementOutcomeRow{
			row(ContextFabricRequirementUnavailable), row(ContextFabricRequirementSatisfied),
			row(ContextFabricRequirementSatisfied),
		}, ContextFabricAnswerCompletenessDegraded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := DeriveContextFabricAnswerCompletenessState(testCase.rows); got != testCase.want {
				t.Fatalf("state = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A document may not carry a completeness state its own rows contradict.
// This is the single-authority check at the wire boundary: the validator runs
// the SAME derivation and requires equality, so a producer cannot stamp a
// state and then change the document, nor copy one across a surface that
// narrowed again.
func TestAStateThatDisagreesWithItsOwnRowsIsRejected(t *testing.T) {
	t.Parallel()
	narrowed := legalRow()
	block := ContextFabricAnswerCompleteness{
		TerminalStatus: ContextFabricInvestigationComplete,
		Outcomes:       []ContextFabricPlanRequirementOutcomeRow{narrowed},
		State:          ContextFabricAnswerCompletenessComplete,
	}
	err := validateAnswerOutcomes(block, contextFabricWriteBounds)
	if err == nil {
		t.Fatal("a block claiming `complete` over a narrowed row was accepted")
	}
	if !strings.Contains(err.Error(), "does not match the state derived from its own outcome rows") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}
	block.State = ContextFabricAnswerCompletenessPartial
	if err := validateAnswerOutcomes(block, contextFabricWriteBounds); err != nil {
		t.Fatalf("the agreeing block was rejected: %v", err)
	}
}

// A `narrowed` row that served everything it declared narrowed nothing.
func TestANarrowingMustBeARealReduction(t *testing.T) {
	t.Parallel()
	row := legalRow()
	row.Served, row.Declared = 9, 9
	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err == nil {
		t.Fatal("a narrowing that served all 9 of 9 declared was accepted")
	}
	row.Served, row.Declared = 10, 9
	if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err == nil {
		t.Fatal("a row serving more than it declared was accepted")
	}
}
