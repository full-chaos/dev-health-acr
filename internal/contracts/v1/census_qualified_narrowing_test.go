package v1

// The ONE exception to "a narrowed row must be a real reduction".
//
// `narrowed` means SERVED, over a reduced set, and the row validator enforces
// it: a row claiming a narrowing that served everything it declared narrowed
// nothing. That rule is right for every reduction this layer describes,
// because Served and Declared measure the axis the reduction happened on.
//
// A count qualified by its own POPULATION is the case it cannot express. The
// number is exact over the resolved member set -- Served == Declared is the
// truthful pair, and inventing a larger Declared would be a fabricated
// population size -- while the answer is still not the census the question
// asked for. Before this exception there was no legal row shape for "exact
// count, population unknown", so the honest outcome could not be written at
// all and the false `satisfied` stood by default.
//
// The exception is narrow on purpose, and each conjunct is tested here on the
// exact defect it exists to catch:
//
//	Served == Declared   -- not a licence for Served > Declared
//	CauseObserved        -- a DEFAULTED census cause would let any producer
//	                        opt out of the reduction rule by naming a code
//	a census code        -- an allow-list over the closed vocabulary, so the
//	                        fifteenth code added is not admitted by default
//
// THE WIRE TOKEN, NOT THE GO SYMBOL. These tests name the coverage code by
// its literal wire string. The token is what the published schema enumerates
// and what a consumer receives; a test written against the Go constant agrees
// with the Go side by construction. It also keeps this file compiling at the
// parent commit, so the red-first proof is a behavioural failure and not a
// build failure.

import (
	"strings"
	"testing"
)

// censusQualifiedToken is the wire token of the population-qualifying
// coverage code. `census_qualified_vocabulary_test.go` binds the Go constant
// to this same string, so the two spellings cannot drift.
const censusQualifiedToken = ContextFabricCoverageDetailCode("population_truncated")

// censusQualifiedCountRow is the row the membership-cardinality step emits for
// a count taken over an incomplete population: exact over what it resolved,
// qualified about what it did not.
func censusQualifiedCountRow() ContextFabricPlanRequirementOutcomeRow {
	return ContextFabricPlanRequirementOutcomeRow{
		Stage:       ContextFabricOutcomeStageAssembledResult,
		Requirement: "count/member/team",
		Obligation:  "count",
		Outcome:     ContextFabricRequirementNarrowed,
		// Scope: fewer of the counted things reached the reader than exist.
		Impact:        ContextFabricAnswerImpactScope,
		CauseCoverage: censusQualifiedToken,
		// Observed: the cohort REPORTED its own incompleteness.
		CauseObserved: true,
		Served:        5,
		Declared:      5,
	}
}

// TestACensusQualifiedCountIsAdmittedWithEqualCounts is the admission, and it
// carries the harm: without it the engine cannot state a qualified count at
// all, so the answer keeps saying `satisfied` over a population it never saw.
func TestACensusQualifiedCountIsAdmittedWithEqualCounts(t *testing.T) {
	t.Parallel()
	if err := ValidateContextFabricPlanRequirementOutcomeRow(censusQualifiedCountRow()); err != nil {
		t.Fatalf("a count qualified by an observed census cause is refused by the wire: %v\n"+
			"the number is exact over the resolved set and the set is not the population, so served == declared is "+
			"the truthful pair; with no legal row shape for it the false `satisfied` stands by default", err)
	}
}

// TestNarrowedWithEqualCountsIsStillRefusedWithoutACensusCause is the NEGATIVE
// CONTROL, and it is what stops the exception from becoming a hole.
//
// It passes at the parent too. That is the point: a mutant deleting the whole
// exception clause would leave the admission test failing and this one
// passing, and a mutant deleting the RULE would leave this one failing. The
// pair distinguishes them.
func TestNarrowedWithEqualCountsIsStillRefusedWithoutACensusCause(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.CauseCoverage = ""
	row.CauseNarrowing = ContextFabricNarrowingBasisCanonicalIDLexical

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a narrowed row that served everything it declared, naming an ORDERING as its cause, was accepted: " +
			"a selection order that removed nothing narrowed nothing")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheCensusExceptionRequiresAnObservedCause plants the defect the
// CauseObserved conjunct exists to catch.
//
// CauseObserved says whether a mechanism REPORTED the cause or the assembly
// layer DEFAULTED to it. An exception admitted on a defaulted cause is an
// exception any producer can take by naming a code it never measured, which
// is the assumption CauseObserved was added to make unsafe.
func TestTheCensusExceptionRequiresAnObservedCause(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.CauseObserved = false

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a census-qualified row with a DEFAULTED cause was accepted; the exception is for a population a " +
			"mechanism actually reported on, not for one the assembly layer assumed")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheCensusExceptionRequiresACensusCode plants the defect the allow-list
// exists to catch.
//
// A non-census coverage code describes something that happened to a READ -- a
// fact was pruned, a source failed. None of those makes served == declared a
// truthful narrowing, and admitting any coverage code would let every one of
// them through.
//
// Several codes, not one. A single-literal negative control is defeated
// exactly by a mutant that special-cases that literal.
func TestTheCensusExceptionRequiresACensusCode(t *testing.T) {
	t.Parallel()
	nonCensus := []ContextFabricCoverageDetailCode{
		ContextFabricCoverageDetailFactPruned,
		ContextFabricCoverageDetailFactUnconfigured,
		ContextFabricCoverageDetailFactReadFailed,
		ContextFabricCoverageDetailGraphExactNameCandidatesTruncated,
		ContextFabricCoverageDetailAnswerTerminatedBeforeAttempt,
		ContextFabricCoverageDetailReuseAuxiliaryRefsStripped,
	}
	if len(nonCensus) == 0 {
		t.Fatal("no codes enumerated; this test would pass while proving nothing")
	}
	for _, code := range nonCensus {
		code := code
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			row := censusQualifiedCountRow()
			row.CauseCoverage = code

			err := ValidateContextFabricPlanRequirementOutcomeRow(row)
			if err == nil {
				t.Fatalf("a narrowed row serving everything it declared was accepted under coverage code %q: "+
					"that code describes something that happened to a read, which does not make served == declared "+
					"a truthful narrowing", code)
			}
			if !strings.Contains(err.Error(), "is not a reduction") {
				t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
			}
		})
	}
}

// TestTheCensusExceptionDoesNotAdmitServedAboveDeclared checks the exception
// did not become a licence on the neighbouring bound.
//
// It asserts the REASON, not merely that an error came back: `Served >
// Declared` is refused by its own bound, and a test that accepted any error
// would pass if the exception had swallowed that bound and the reduction rule
// had caught it instead -- a different rule doing a different job.
func TestTheCensusExceptionDoesNotAdmitServedAboveDeclared(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.Served = 6
	row.Declared = 5

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a row serving MORE than it declared was accepted under the census exception")
	}
	if !strings.Contains(err.Error(), "violates v1 bounds") {
		t.Fatalf("rejected for the wrong reason: %v (want the served/declared bound, not the reduction rule)", err)
	}
}

// TestACensusQualifiedRowCannotCarryARefinement pins the interaction with the
// refinement chain.
//
// A refinement says the population shrank from Before to After. A
// census-qualified row reduced nothing between its own two numbers, so no
// chain can reconcile with them -- and the derivation that builds refinements
// already declines to mint one here. This asserts the validator agrees, so the
// two halves cannot drift into disagreeing about the same row.
func TestACensusQualifiedRowCannotCarryARefinement(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.Refinements = []ContextFabricRequirementRefinement{
		{Stage: ContextFabricOutcomeStageAssembledResult, Coverage: censusQualifiedToken, Before: 5, After: 5},
	}

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a census-qualified row carrying a refinement was accepted; the step it records reduced nothing, " +
			"which puts a stage's name against a reduction it did not make")
	}
	if !strings.Contains(err.Error(), "not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v", err)
	}
}

// TestTheCensusExceptionDoesNotReachOtherOutcomes checks the exception is
// scoped to `narrowed`.
//
// `satisfied` and `not_applicable` lose nothing and must name NO cause, so a
// census code on either is still a pairing failure. Asserting it here is what
// keeps a widened exception from quietly making the lossless rule optional.
func TestTheCensusExceptionDoesNotReachOtherOutcomes(t *testing.T) {
	t.Parallel()
	for _, outcome := range []ContextFabricPlanRequirementOutcome{
		ContextFabricRequirementSatisfied,
		ContextFabricRequirementNotApplicable,
	} {
		outcome := outcome
		t.Run(string(outcome), func(t *testing.T) {
			t.Parallel()
			row := censusQualifiedCountRow()
			row.Outcome = outcome
			row.Impact = ContextFabricAnswerImpactNone

			if err := ValidateContextFabricPlanRequirementOutcomeRow(row); err == nil {
				t.Fatalf("outcome %q was accepted while naming a census cause; a row that lost nothing must name none", outcome)
			}
		})
	}
}

// TestACensusQualifiedRowDerivesPartialCompleteness is the consequence the
// whole change exists for, asserted on the derivation rather than inferred.
//
// The row is `narrowed`, and `narrowed` contributes `partial`. Nothing in the
// derivation changes for this ticket, and that is worth an assertion rather
// than an assumption: the acceptance criterion is a completeness state, and a
// state nobody asserts is a state nobody has.
func TestACensusQualifiedRowDerivesPartialCompleteness(t *testing.T) {
	t.Parallel()
	rows := []ContextFabricPlanRequirementOutcomeRow{
		{
			Stage: ContextFabricOutcomeStagePlanning, Requirement: "count/member/team", Obligation: "count",
			Outcome: ContextFabricRequirementSatisfied, Impact: ContextFabricAnswerImpactNone,
		},
		censusQualifiedCountRow(),
	}
	if got := DeriveContextFabricAnswerCompletenessState(rows); got != ContextFabricAnswerCompletenessPartial {
		t.Fatalf("a set carrying a census-qualified count derives %q, want %q", got, ContextFabricAnswerCompletenessPartial)
	}
}

// The three tests below plant the defect the ROW-CLASS conjuncts exist to
// catch, and they exist because the first version of this exception did not
// have them.
//
// The original three conjuncts (equal counts, an observed cause, a census
// code) all describe whether the row is HONEST. None of them says WHOSE row
// it is. So the exception was available to any narrowed row that could
// arrange those three -- a `state` obligation, a seed row at the `planning`
// stage, a row claiming `impact: depth` while naming a scope loss -- none of
// which is a count taken over a population, and every one of which would have
// been admitted with equal counts against the reduction rule.
//
// That is the same defect this whole change removes, one layer up: a
// predicate standing for more states than it names. The producer sets stage,
// obligation and impact on the single site that emits this shape, so scoping
// to them costs no legitimate row.

// TestTheCensusExceptionIsScopedToTheAssembledResultStage plants a seed row.
//
// A `planning` row is written BEFORE anything was read. It cannot have taken
// a count over a population, so a census cause on one describes a measurement
// that had not happened yet.
func TestTheCensusExceptionIsScopedToTheAssembledResultStage(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.Stage = ContextFabricOutcomeStagePlanning

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a PLANNING row took the census exception; the exception is for a count that was actually taken, " +
			"and a planning row is written before anything is read")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheCensusExceptionIsScopedToTheCountObligation plants a different
// obligation on an otherwise identical row.
//
// Only a count is a value computed OVER a population. A `state` obligation
// serving everything it declared narrowed nothing, and admitting it would
// make the reduction rule optional for every obligation that can name a
// census code.
func TestTheCensusExceptionIsScopedToTheCountObligation(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.Requirement = "state/subject/team"
	row.Obligation = "state"

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a STATE obligation took the census exception; only a count is a value computed over a population, " +
			"so only a count can be qualified by one")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheCensusExceptionIsScopedToAScopeImpact plants an impact that
// contradicts the cause.
//
// `depth` means less evidence per subject; `scope` means fewer subjects than
// exist. An incomplete population is a SCOPE loss by definition, so a row
// naming a census cause while reporting `depth` states two different losses
// and the exception must not choose one for it.
func TestTheCensusExceptionIsScopedToAScopeImpact(t *testing.T) {
	t.Parallel()
	row := censusQualifiedCountRow()
	row.Impact = ContextFabricAnswerImpactDepth

	err := ValidateContextFabricPlanRequirementOutcomeRow(row)
	if err == nil {
		t.Fatal("a row reporting impact DEPTH took the census exception while naming a population cause; " +
			"an unseen population is a scope loss, not a per-subject depth loss")
	}
	if !strings.Contains(err.Error(), "is not a reduction") {
		t.Fatalf("rejected for the wrong reason: %v (want the reduction rule)", err)
	}
}

// TestTheCountObligationConstantIsTheWireToken binds the constant the rule is
// written against to its literal AND to the closed vocabulary.
//
// This is the single gap the literal-only discipline in the rest of this file
// leaves. The guard above names a Go constant; the wire carries a string; the
// mirror carries a third copy. Asserting all three in one place is what keeps
// a rename of the constant from silently changing which rows the exception
// admits while every other test goes on passing.
func TestTheCountObligationConstantIsTheWireToken(t *testing.T) {
	t.Parallel()
	if ContextFabricAnswerObligationCount != "count" {
		t.Fatalf("the count obligation constant is %q, want the wire token %q",
			ContextFabricAnswerObligationCount, "count")
	}
	if !ValidContextFabricAnswerObligation(ContextFabricAnswerObligationCount) {
		t.Fatal("the constant the census exception is written against is not a member of the obligation mirror; " +
			"a rule keyed on a non-member can never fire")
	}
	if ContextFabricAnswerObligationCoverage != "coverage" {
		t.Fatalf("the coverage obligation constant is %q, want the wire token %q",
			ContextFabricAnswerObligationCoverage, "coverage")
	}
	if !ValidContextFabricAnswerObligation(ContextFabricAnswerObligationCoverage) {
		t.Fatal("the coverage obligation constant is not a member of the obligation mirror")
	}
}

// TestTheCensusExceptionRejectsAMixedPopulationAndReductionCause plants the
// defect the two EMPTINESS conjuncts exist to catch, ACROSS BOTH WHOLE
// VOCABULARIES.
//
// `named` is an OR across the three cause fields, so a row may carry more than
// one. That is right in general — a reduction can have both a basis and an
// overrun — and it is wrong for THIS row. A narrowing basis says the survivors
// were selected out of a larger set, and an overrun says a budget cut one
// short; both assert a reduction, and both are contradicted by the equal counts
// the census exception exists to permit. A row carrying one of those beside the
// population cause states two incompatible stories and the exception must not
// choose between them.
//
// It WALKS both closed vocabularies rather than naming one member of each, and
// that is the third time this change has learned the same thing: a per-member
// negative test closes only the member it names. The first version of this test
// used one narrowing basis and one overrun, which would have left a conjunct
// widened to admit any OTHER member passing every test in the file.
//
// The producer never writes this shape: it copies a basis and an overrun only
// when a member step actually reduced the set, and that row takes the ordinary
// narrowed path, not this one. So the refusal costs nothing real.
func TestTheCensusExceptionRejectsAMixedPopulationAndReductionCause(t *testing.T) {
	t.Parallel()

	t.Run("every narrowing basis", func(t *testing.T) {
		t.Parallel()
		bases := ContextFabricNarrowingBasisVocabulary()
		if len(bases) == 0 {
			t.Fatal("the narrowing vocabulary is empty, so this walk asserts nothing")
		}
		for _, basis := range bases {
			row := censusQualifiedCountRow()
			row.CauseNarrowing = basis

			err := ValidateContextFabricPlanRequirementOutcomeRow(row)
			if err == nil {
				t.Errorf("a census-qualified row also naming narrowing basis %q was accepted; that cause asserts "+
					"a reduction, and this row's equal counts assert there was none", basis)
				continue
			}
			if !strings.Contains(err.Error(), "is not a reduction") {
				t.Errorf("basis %q was refused for the wrong reason: %v (want the reduction rule)", basis, err)
			}
		}
	})

	t.Run("every budget overrun", func(t *testing.T) {
		t.Parallel()
		overruns := ContextFabricBudgetOverrunVocabulary()
		if len(overruns) == 0 {
			t.Fatal("the overrun vocabulary is empty, so this walk asserts nothing")
		}
		for _, overrun := range overruns {
			row := censusQualifiedCountRow()
			row.CauseOverrun = overrun

			err := ValidateContextFabricPlanRequirementOutcomeRow(row)
			if err == nil {
				t.Errorf("a census-qualified row also naming overrun %q was accepted; a budget that cut the answer "+
					"short asserts a reduction, and this row's equal counts assert there was none", overrun)
				continue
			}
			if !strings.Contains(err.Error(), "is not a reduction") {
				t.Errorf("overrun %q was refused for the wrong reason: %v (want the reduction rule)", overrun, err)
			}
		}
	})
}

// The three walks below assert the exception admits EXACTLY ONE member of each
// closed vocabulary it is scoped on.
//
// They exist because a per-member negative test only closes the member it
// names. The first review round's fix was pinned by one `planning` row, one
// `state` obligation and one `depth` impact — which leaves `reuse`, `coverage`,
// `dimension` and every future member admitted by a widened conjunct that no
// test would notice. A walk over the whole vocabulary is the allow-list form of
// the same assertion, and it is the form that survives the vocabulary growing.

// TestTheCensusExceptionAdmitsExactlyOneStage walks the stage vocabulary.
func TestTheCensusExceptionAdmitsExactlyOneStage(t *testing.T) {
	t.Parallel()
	admitted := 0
	for _, stage := range ContextFabricOutcomeStageVocabulary() {
		row := censusQualifiedCountRow()
		row.Stage = stage
		err := ValidateContextFabricPlanRequirementOutcomeRow(row)
		if stage == ContextFabricOutcomeStageAssembledResult {
			if err != nil {
				t.Errorf("the assembled-result stage was REFUSED: %v -- the exception admits no stage at all", err)
				continue
			}
			admitted++
			continue
		}
		if err == nil {
			t.Errorf("stage %q took the census exception; only the stage that actually takes the count may", stage)
			continue
		}
		if !strings.Contains(err.Error(), "is not a reduction") {
			t.Errorf("stage %q was refused for the wrong reason: %v (want the reduction rule)", stage, err)
		}
	}
	if admitted != 1 {
		t.Errorf("the exception admitted %d stages, want exactly 1", admitted)
	}
}

// TestTheCensusExceptionAdmitsExactlyOneObligation walks the obligation mirror.
//
// The requirement identity is the obligation/role/subject coordinate and its
// first segment must equal the row's own obligation, so the identity moves with
// the obligation here -- otherwise every case would be refused by the identity
// rule and the walk would pass while proving nothing about the exception.
func TestTheCensusExceptionAdmitsExactlyOneObligation(t *testing.T) {
	t.Parallel()
	admitted := 0
	for _, obligation := range ContextFabricAnswerObligationVocabulary() {
		row := censusQualifiedCountRow()
		row.Obligation = obligation
		row.Requirement = obligation + "/member/team"
		err := ValidateContextFabricPlanRequirementOutcomeRow(row)
		if obligation == ContextFabricAnswerObligationCount {
			if err != nil {
				t.Errorf("the count obligation was REFUSED: %v -- the exception admits no obligation at all", err)
				continue
			}
			admitted++
			continue
		}
		if err == nil {
			t.Errorf("obligation %q took the census exception; only a count is a value computed over a population", obligation)
			continue
		}
		if !strings.Contains(err.Error(), "is not a reduction") {
			t.Errorf("obligation %q was refused for the wrong reason: %v (want the reduction rule)", obligation, err)
		}
	}
	if admitted != 1 {
		t.Errorf("the exception admitted %d obligations, want exactly 1", admitted)
	}
}

// TestTheCensusExceptionAdmitsExactlyOneImpact walks the impact vocabulary.
//
// `none` is the one member refused by a DIFFERENT rule and it is called out
// rather than folded in: `narrowed` with impact `none` fails the outcome/impact
// pairing before the reduction rule is ever reached. Asserting the reduction
// message for it would be asserting something untrue, and asserting only "some
// error" would let the pairing rule stand in for the conjunct this test is
// about.
func TestTheCensusExceptionAdmitsExactlyOneImpact(t *testing.T) {
	t.Parallel()
	admitted := 0
	for _, impact := range ContextFabricAnswerImpactKindVocabulary() {
		row := censusQualifiedCountRow()
		row.Impact = impact
		err := ValidateContextFabricPlanRequirementOutcomeRow(row)
		switch impact {
		case ContextFabricAnswerImpactScope:
			if err != nil {
				t.Errorf("the scope impact was REFUSED: %v -- the exception admits no impact at all", err)
				continue
			}
			admitted++
		case ContextFabricAnswerImpactNone:
			if err == nil {
				t.Error("impact `none` on a narrowed row was accepted; the pairing rule alone should refuse it")
				continue
			}
			if !strings.Contains(err.Error(), "not a legal pairing") {
				t.Errorf("impact `none` was refused for the wrong reason: %v (want the pairing rule)", err)
			}
		default:
			if err == nil {
				t.Errorf("impact %q took the census exception; an unseen population is a scope loss", impact)
				continue
			}
			if !strings.Contains(err.Error(), "is not a reduction") {
				t.Errorf("impact %q was refused for the wrong reason: %v (want the reduction rule)", impact, err)
			}
		}
	}
	if admitted != 1 {
		t.Errorf("the exception admitted %d impacts, want exactly 1", admitted)
	}
}
