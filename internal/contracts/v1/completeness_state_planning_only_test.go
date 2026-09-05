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
			// A HALF-ATTRIBUTED row: an obligation with no requirement. The
			// row validator refuses it (requirement and obligation must be
			// present or absent together), so it never reaches the wire --
			// but this is a PURE FUNCTION over whatever rows it is handed,
			// and it must not read a nameless row as a requirement.
			//
			// This is the case the skip exists for, and the ONLY one. The
			// fully unattributed row above is already excluded a step
			// earlier, because an empty obligation is not classified as a
			// read; without a case whose obligation IS a read, deleting the
			// skip changes no observable behaviour and the guard is
			// untested. Found by a deletion mutant surviving the battery.
			name: "a half-attributed row cannot seed a requirement nobody named",
			rows: []ContextFabricPlanRequirementOutcomeRow{
				planningRow("state/subject/team", "state"),
				evaluatedRow("state/subject/team", "state"),
				{Stage: ContextFabricOutcomeStagePlanning, Obligation: "state",
					Outcome: ContextFabricRequirementSatisfied, Impact: ContextFabricAnswerImpactNone},
			},
			want: ContextFabricAnswerCompletenessComplete,
		},
		{
			// THE COMPLEMENT, asserted in the same run: give that row an
			// identity and it DOES lower the state. Without this the case
			// above could pass because the fixture never reached the
			// predicate at all, which is the way a guard test quietly stops
			// testing anything.
			name: "the same row WITH an identity does lower the state",
			rows: []ContextFabricPlanRequirementOutcomeRow{
				planningRow("state/subject/team", "state"),
				evaluatedRow("state/subject/team", "state"),
				planningRow("health/subject/team", "state"),
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

	// POSITIVE CONTROL: the same fixture DOES register when the stage is the
	// one that means a read happened. Without it the assertions above would
	// pass on a classifier that ignored every row.
	t.Run("known stage assembled_result answers a seed", func(t *testing.T) {
		t.Parallel()
		answer := planningRow(identity, obligation)
		answer.Stage = ContextFabricOutcomeStageAssembledResult
		rows := []ContextFabricPlanRequirementOutcomeRow{planningRow(identity, obligation), answer}
		if got := DeriveContextFabricAnswerCompletenessState(rows); got != ContextFabricAnswerCompletenessComplete {
			t.Fatalf("an assembled-result row did NOT answer the seed: state = %q, want complete -- the "+
				"allow-list has dropped the one stage that means a read happened", got)
		}
	})

	// AND THE OTHER DIRECTION, in the same run: `projection` and `reuse` are
	// VOCABULARY MEMBERS that must NOT answer a read seed.
	//
	// A projection row is a byte-budget cut over a finished document; a reuse
	// row is a degrade of a stored one. Neither performed a read. A lossless
	// row from either, beside a lossless planning seed, would otherwise derive
	// `complete` for a requirement nothing ever read -- and it would do so
	// through a row that is entirely valid, so no validator could catch it.
	//
	// These are LOSSLESS rows on purpose. A narrowed projection row would
	// lower the state through the outcome pass and prove nothing about the
	// stage classification, which is what is under test here. Today's emitters
	// happen to produce narrowed rows at both stages; a guard that holds only
	// because no emitter has yet produced the admitting row is a coincidence,
	// not a guard.
	for _, stage := range []ContextFabricOutcomeStage{
		ContextFabricOutcomeStageProjection,
		ContextFabricOutcomeStageReuse,
	} {
		stage := stage
		t.Run("known stage "+string(stage)+" cannot answer a read seed", func(t *testing.T) {
			t.Parallel()
			counterfeit := planningRow(identity, obligation)
			counterfeit.Stage = stage
			// The row is contract-valid, which is the point: validity is not
			// what stops this, the stage classification is.
			if err := ValidateContextFabricPlanRequirementOutcomeRow(counterfeit); err != nil {
				t.Fatalf("the fixture row is not contract-valid, so this test would prove nothing about "+
					"the classification: %v", err)
			}
			rows := []ContextFabricPlanRequirementOutcomeRow{planningRow(identity, obligation), counterfeit}
			if got := DeriveContextFabricAnswerCompletenessState(rows); got != ContextFabricAnswerCompletenessPartial {
				t.Fatalf("a lossless %q row answered a READ seed: state = %q, want partial -- neither stage "+
					"performs a read, so neither is evidence that the requirement was evaluated", stage, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BASE-COMPILABLE BOUNDARY. Everything ABOVE this line compiles and runs at the
// base commit, so it can serve as a red-at-base proof: a real failure, not a
// build error. Everything BELOW names a symbol this change introduces and can
// only be proven by mutation. The red-on-base runner truncates each file here;
// the boundary is a declared property of the file rather than a list kept
// somewhere else that would drift from it.
// ---------------------------------------------------------------------------
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
