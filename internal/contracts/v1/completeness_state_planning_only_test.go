package v1

import "testing"

// The read pass of the completeness derivation: a READ requirement that reached
// the served document carrying nothing but its planning-stage seed cannot leave
// the set reading `complete`.
//
// A seed row says the registry COULD serve the cell. It is minted from
// serveability, before any read runs, so a set of nothing but seeds derives
// `complete` vacuously -- an answer whose every read failed reporting the
// strongest completeness the vocabulary has.

// planningRow builds a seed-shaped row: planning stage, satisfied, attributed.
func planningRow(identity, obligation string) ContextFabricPlanRequirementOutcomeRow {
	return ContextFabricPlanRequirementOutcomeRow{
		Stage:       ContextFabricOutcomeStagePlanning,
		Requirement: identity,
		Obligation:  obligation,
		Outcome:     ContextFabricRequirementSatisfied,
		Impact:      ContextFabricAnswerImpactNone,
	}
}

// evaluatedRow builds the assembled-result row that answers a seed.
func evaluatedRow(identity, obligation string) ContextFabricPlanRequirementOutcomeRow {
	row := planningRow(identity, obligation)
	row.Stage = ContextFabricOutcomeStageAssembledResult
	return row
}

// TestAPlanningOnlyReadRequirementCannotReadComplete carries the harm.
//
// Every case is a set whose outcome TOKENS are all lossless -- so the outcome
// pass says `complete` in each of them, and the only thing that can move the
// result is the read pass.
func TestAPlanningOnlyReadRequirementCannotReadComplete(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		rows []ContextFabricPlanRequirementOutcomeRow
		want ContextFabricAnswerCompletenessState
	}{
		{
			// THE DEFECT, in one row.
			name: "a READ requirement with only its seed",
			rows: []ContextFabricPlanRequirementOutcomeRow{planningRow("state/subject/team", "state")},
			want: ContextFabricAnswerCompletenessPartial,
		},
		{
			name: "the same requirement, evaluated",
			rows: []ContextFabricPlanRequirementOutcomeRow{
				planningRow("state/subject/team", "state"),
				evaluatedRow("state/subject/team", "state"),
			},
			want: ContextFabricAnswerCompletenessComplete,
		},
		{
			// SCOPED TO READS. Nothing appends an assembled-result row for
			// `ranking` anywhere in this service, so an unscoped rule would
			// make every ranking answer partial for a hole this change does
			// not close.
			name: "a COMPUTED requirement with only its seed",
			rows: []ContextFabricPlanRequirementOutcomeRow{planningRow("ranking/member/team", "ranking")},
			want: ContextFabricAnswerCompletenessComplete,
		},
		{
			name: "one read seeded and evaluated, another read seeded only",
			rows: []ContextFabricPlanRequirementOutcomeRow{
				planningRow("state/subject/team", "state"),
				evaluatedRow("state/subject/team", "state"),
				planningRow("health/subject/team", "health"),
			},
			want: ContextFabricAnswerCompletenessPartial,
		},
		{
			// An unattributed row cannot answer FOR a requirement, and it
			// cannot make a set partial on its own either: the projection's
			// rows carry no identity by design.
			name: "an unattributed row beside a seeded read",
			rows: []ContextFabricPlanRequirementOutcomeRow{
				planningRow("state/subject/team", "state"),
				{Stage: ContextFabricOutcomeStageProjection, Outcome: ContextFabricRequirementSatisfied,
					Impact: ContextFabricAnswerImpactNone},
			},
			want: ContextFabricAnswerCompletenessPartial,
		},
		{
			name: "no rows at all is still not_derived",
			rows: nil,
			want: ContextFabricAnswerCompletenessNotDerived,
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := DeriveContextFabricAnswerCompletenessState(testCase.rows); got != testCase.want {
				t.Fatalf("state = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestTheReadPassOnlyLOWERS is the property the frozen predicate's contract
// rests on, asserted rather than argued.
//
// If the read pass could raise a state, the frozen function's "we disagree on
// exactly one shape" claim would be false and the stored-path exemption would
// admit shapes nobody enumerated.
func TestTheReadPassOnlyLOWERS(t *testing.T) {
	t.Parallel()
	seeded := planningRow("state/subject/team", "state")
	for _, name := range []struct {
		label string
		rows  []ContextFabricPlanRequirementOutcomeRow
	}{
		{"degraded stays degraded", []ContextFabricPlanRequirementOutcomeRow{seeded, legalUnavailableRow()}},
		{"partial stays partial", []ContextFabricPlanRequirementOutcomeRow{seeded, legalRow()}},
	} {
		name := name
		t.Run(name.label, func(t *testing.T) {
			t.Parallel()
			before := DeriveContextFabricAnswerCompletenessStateBeforeReadEvaluation(name.rows)
			after := DeriveContextFabricAnswerCompletenessState(name.rows)
			if before != after {
				t.Fatalf("the read pass moved a non-complete state %q to %q; it may only lower "+
					"`complete`, and the frozen predicate's exemption depends on that", before, after)
			}
		})
	}
}

// legalUnavailableRow is a valid attributed unavailable row, for the
// absorbing-state cases.
func legalUnavailableRow() ContextFabricPlanRequirementOutcomeRow {
	return ContextFabricPlanRequirementOutcomeRow{
		Stage:         ContextFabricOutcomeStageAssembledResult,
		Outcome:       ContextFabricRequirementUnavailable,
		Impact:        ContextFabricAnswerImpactDimension,
		CauseCoverage: ContextFabricCoverageDetailFactUnconfigured,
		CauseObserved: true,
	}
}

// TestAnUnknownStageContributesToNeitherSet is the stage ALLOW-LIST, and it
// asserts BOTH directions.
//
// The classification is an allow-list rather than "anything that is not
// planning", because the stage vocabulary is CLOSED: a deny-list admits its
// next member by default AND admits the zero value. A row carrying stage "" is
// malformed -- the row validator refuses it -- and reading a malformed row as
// PROOF THAT A REQUIREMENT WAS EVALUATED is the strongest possible conclusion
// from the weakest possible evidence. Under a deny-list one such row silently
// restores `complete`.
//
// The positive controls are what stop this passing because the fixture never
// reached the classifier at all.
func TestAnUnknownStageContributesToNeitherSet(t *testing.T) {
	t.Parallel()
	const identity, obligation = "state/subject/team", "state"

	for _, stage := range []ContextFabricOutcomeStage{"", "not_a_stage", "PLANNING"} {
		stage := stage
		t.Run("unknown stage "+string(stage)+" cannot evaluate a seed", func(t *testing.T) {
			t.Parallel()
			odd := planningRow(identity, obligation)
			odd.Stage = stage
			rows := []ContextFabricPlanRequirementOutcomeRow{planningRow(identity, obligation), odd}
			if got := DeriveContextFabricAnswerCompletenessState(rows); got != ContextFabricAnswerCompletenessPartial {
				t.Fatalf("a row with stage %q answered a planning-stage seed: state = %q, want partial", stage, got)
			}
		})
		t.Run("unknown stage "+string(stage)+" cannot seed on its own", func(t *testing.T) {
			t.Parallel()
			odd := planningRow(identity, obligation)
			odd.Stage = stage
			rows := []ContextFabricPlanRequirementOutcomeRow{odd}
			if got := DeriveContextFabricAnswerCompletenessState(rows); got != ContextFabricAnswerCompletenessComplete {
				t.Fatalf("a row with stage %q lowered the state on its own: state = %q, want complete", stage, got)
			}
		})
	}

	// POSITIVE CONTROLS: the same fixture DOES register when the stage is a
	// vocabulary member. Without these the assertions above would pass on a
	// classifier that ignored every row.
	for _, stage := range []ContextFabricOutcomeStage{
		ContextFabricOutcomeStageAssembledResult,
		ContextFabricOutcomeStageProjection,
		ContextFabricOutcomeStageReuse,
	} {
		stage := stage
		t.Run("known stage "+string(stage)+" answers a seed", func(t *testing.T) {
			t.Parallel()
			answer := planningRow(identity, obligation)
			answer.Stage = stage
			rows := []ContextFabricPlanRequirementOutcomeRow{planningRow(identity, obligation), answer}
			if got := DeriveContextFabricAnswerCompletenessState(rows); got != ContextFabricAnswerCompletenessComplete {
				t.Fatalf("a row at stage %q did NOT answer the seed: state = %q, want complete -- the "+
					"allow-list has dropped a real stage", stage, got)
			}
		})
	}
}

// TestEveryReadObligationIsClassifiedByTheMirror keeps the scoping key total.
//
// An obligation missing from the mirror classifies as not-a-read, so its
// planning-only rows would stop lowering the state -- a silent restoration of
// the exact `complete` this pass exists to refuse.
func TestEveryReadObligationIsClassifiedByTheMirror(t *testing.T) {
	t.Parallel()
	mirror := ContextFabricAnswerObligationKindByObligation()
	if len(mirror) == 0 {
		t.Fatal("the obligation-kind mirror is empty; every assertion below would pass vacuously")
	}
	vocabulary := ContextFabricAnswerObligationVocabulary()
	reads := 0
	for _, obligation := range vocabulary {
		kind, classified := mirror[obligation]
		if !classified {
			t.Fatalf("obligation %q has no kind in the mirror", obligation)
		}
		if !ValidContextFabricAnswerObligationKind(kind) {
			t.Fatalf("obligation %q maps to %q, which is not an obligation-kind vocabulary member", obligation, kind)
		}
		if kind == contextFabricObligationKindRead {
			reads++
		}
	}
	if len(mirror) != len(vocabulary) {
		t.Fatalf("the mirror holds %d entries for a %d-member obligation vocabulary", len(mirror), len(vocabulary))
	}
	if reads == 0 {
		t.Fatal("the mirror classifies no obligation as a read; the read pass could never fire")
	}
}
