package v1

import "testing"

// The frozen predicate and the stored-path arm that admits what it derives.
//
// Amending the completeness derivation would otherwise make every ALREADY
// STORED document whose state the new rule no longer produces unreadable:
// results are immutable, the payload IS the document, and Store.Get validates
// on every read. The frozen function keeps those rows readable; these tests are
// what bound what it excuses, so the exemption cannot quietly widen into a
// second opinion about completeness.

// legacyArmRowShapes is the generator's row pool: every outcome token, at a
// planning and a non-planning stage, for a READ and a COMPUTED obligation.
//
// Identity always begins with the obligation, because the row validator
// requires it and because an identity that disagreed with its own obligation
// would exercise a shape no producer can emit.
func legacyArmRowShapes() []ContextFabricPlanRequirementOutcomeRow {
	var shapes []ContextFabricPlanRequirementOutcomeRow
	for _, obligation := range []string{"state", "ranking"} {
		for _, stage := range []ContextFabricOutcomeStage{
			ContextFabricOutcomeStagePlanning,
			ContextFabricOutcomeStageAssembledResult,
		} {
			for _, outcome := range ContextFabricPlanRequirementOutcomeVocabulary() {
				shapes = append(shapes, ContextFabricPlanRequirementOutcomeRow{
					Stage:       stage,
					Requirement: obligation + "/subject/team",
					Obligation:  obligation,
					Outcome:     outcome,
				})
			}
		}
	}
	return shapes
}

// TestTheFrozenPredicateDisagreesOnExactlyOneShape is the behavioural pin.
//
// "The frozen function is a verbatim copy of the old derivation" is a claim,
// and a source-text comparison would be a LEXICAL pin -- it proves a string
// exists, not that a function behaves. So the two are driven over every outcome
// multiset the vocabulary admits up to length three, crossed with both stages
// and both obligation kinds, and required to agree everywhere except the one
// shape the frozen function's own comment names.
//
// The second half matters as much as the first: the test also requires that
// they DISAGREE at least once. A mutant that collapses the two -- pointing the
// legacy arm at the amended derivation -- would otherwise pass on an empty
// disagreement set, which is the "a check that can never fire is no assertion"
// shape.
func TestTheFrozenPredicateDisagreesOnExactlyOneShape(t *testing.T) {
	t.Parallel()
	shapes := legacyArmRowShapes()
	if len(shapes) == 0 {
		t.Fatal("the row pool is empty; every assertion below would pass vacuously")
	}

	sets := 0
	disagreements := 0
	var walk func(depth int, prefix []ContextFabricPlanRequirementOutcomeRow)
	walk = func(depth int, prefix []ContextFabricPlanRequirementOutcomeRow) {
		sets++
		frozen := DeriveContextFabricAnswerCompletenessStateBeforeReadEvaluation(prefix)
		amended := DeriveContextFabricAnswerCompletenessState(prefix)
		if frozen != amended {
			disagreements++
			// THE ONE SHAPE, asserted positively in every part rather than
			// as "they differ somehow".
			if frozen != ContextFabricAnswerCompletenessComplete {
				t.Fatalf("the two rules disagree on a set the frozen rule called %q; the exemption is "+
					"only characterised for `complete`. rows=%+v", frozen, prefix)
			}
			if amended != ContextFabricAnswerCompletenessPartial {
				t.Fatalf("the amended rule moved a `complete` set to %q rather than `partial`. rows=%+v", amended, prefix)
			}
			if !hasPlanningOnlyReadRequirement(prefix) {
				t.Fatalf("the two rules disagree on a set carrying NO planning-only read requirement, "+
					"which is the only shape the frozen function's comment excuses. rows=%+v", prefix)
			}
		}
		if depth == 0 {
			return
		}
		for _, shape := range shapes {
			walk(depth-1, append(prefix, shape))
		}
	}
	walk(3, nil)

	if sets < len(shapes) {
		t.Fatalf("the generator produced %d sets from a %d-shape pool; it stopped producing inputs", sets, len(shapes))
	}
	if disagreements == 0 {
		t.Fatal("the two rules NEVER disagree over the whole generated space -- the amended derivation " +
			"is doing nothing, or the legacy arm has been collapsed onto it")
	}
	t.Logf("compared %d outcome multisets; %d disagreements, all of the one excused shape", sets, disagreements)
}

// TestTheLegacyArmAdmitsExactlyOneShape drives the VALIDATOR, both paths.
//
// The frozen predicate existing is not the same as it being reachable from the
// stored path and unreachable from the fresh one, and that split is the whole
// safety property: a fresh result has no older-deployment excuse.
func TestTheLegacyArmAdmitsExactlyOneShape(t *testing.T) {
	t.Parallel()
	// A set the OLD rule calls complete and the NEW rule calls partial: one
	// READ requirement carrying only its planning seed.
	legacy := ContextFabricAnswerCompleteness{
		TerminalStatus: ContextFabricInvestigationComplete,
		State:          ContextFabricAnswerCompletenessComplete,
		Outcomes:       []ContextFabricPlanRequirementOutcomeRow{planningRow("state/subject/team", "state")},
	}
	if want := DeriveContextFabricAnswerCompletenessState(legacy.Outcomes); want == legacy.State {
		t.Fatalf("the fixture's premise moved: the amended rule now derives %q for it, so it no "+
			"longer exercises the legacy arm", want)
	}

	if err := validateAnswerOutcomes(legacy, contextFabricLegacyBounds); err != nil {
		t.Fatalf("a stored document written under the old rule is no longer readable: %v", err)
	}

	err := validateAnswerOutcomes(legacy, contextFabricWriteBounds)
	if err == nil {
		t.Fatal("a FRESH result was allowed to stamp a state the amended rule does not produce; the " +
			"legacy arm must be reachable from the stored path only")
	}
	// THE RIGHT REASON. An oracle that accepts any error proves the document
	// was rejected, not that it was rejected by the bound under test.
	if got := err.Error(); !contains(got, "does not match the state derived from its own outcome rows") {
		t.Fatalf("the fresh path rejected the document for a different reason: %v", err)
	}

	// ATTRIBUTION CONTROL. Correcting ONLY the state makes the same document
	// valid on the fresh path, so the rejection above is attributable to the
	// state field and to nothing else in the block.
	corrected := legacy
	corrected.State = DeriveContextFabricAnswerCompletenessState(legacy.Outcomes)
	if err := validateAnswerOutcomes(corrected, contextFabricWriteBounds); err != nil {
		t.Fatalf("correcting only the state left the document invalid, so the rejection above was "+
			"not attributable to the state: %v", err)
	}
}

// TestTheLegacyArmRefusesAStateNeitherRuleDerives keeps the arm bounded.
//
// It admits a state the FROZEN rule derives -- not any state at all. Without
// this, "the stored path is lenient" would be indistinguishable from "the
// stored path does not check".
func TestTheLegacyArmRefusesAStateNeitherRuleDerives(t *testing.T) {
	t.Parallel()
	rows := []ContextFabricPlanRequirementOutcomeRow{planningRow("state/subject/team", "state")}
	for _, state := range []ContextFabricAnswerCompletenessState{
		ContextFabricAnswerCompletenessDegraded,
		ContextFabricAnswerCompletenessNotDerived,
	} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			block := ContextFabricAnswerCompleteness{
				TerminalStatus: ContextFabricInvestigationComplete,
				State:          state,
				Outcomes:       rows,
			}
			if err := validateAnswerOutcomes(block, contextFabricLegacyBounds); err == nil {
				t.Fatalf("the stored path accepted state %q, which NEITHER rule derives from these "+
					"rows -- the exemption is admitting anything, not one named shape", state)
			}
		})
	}
}

// contains is a local substring helper, kept here rather than reaching for a
// new import in a package this size.
func contains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
