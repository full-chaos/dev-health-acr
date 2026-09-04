package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Tests for the §13.2.3 amendment: a computed obligation declares the fact
// kinds its SERVER STEP consumes.
//
// WHAT THE AMENDMENT IS FOR, stated once here because every assertion below
// is a consequence of it. Before it, a computed requirement row named its
// step and nothing else, and the derivation's own comment promised more than
// it delivered: "a computed obligation has NO FactKinds of its own and is
// unavailable only when ITS INPUTS ARE" made availability depend on inputs
// while naming no inputs. The six-authority parity proof could therefore not
// rule that a lost fact kind was NOT an input of a computed step, so it had
// to assume every loss on such a frame might be, and no authority was
// retirable. Declaring the inputs is what turns that assumption into a
// measurement.
//
// THE INPUTS ARE BUILT FROM THE EXECUTED SIGNALS, NEVER FROM A DOCSTRING.
// ComputedStepRankCohort's comment said it "depends on the read obligation
// principal_drivers". RankCohort does not read an obligation; it reads five
// named fact kinds directly, one per signal family, and that is what
// TestRankCohortInputsAreTheKindsItsSignalsActuallyRead pins.

// TestEveryComputedStepDeclaresItsInputs is the totality assertion, and it is
// the same shape as the kinds/steps agreement already asserted beside it: a
// step with no input declaration is exactly the hole this amendment closes,
// so it must be a failure rather than a zero value.
func TestEveryComputedStepDeclaresItsInputs(t *testing.T) {
	for _, step := range ComputedObligationStepVocabulary() {
		inputs, declared := InputsForComputedStep(step)
		if !declared {
			t.Errorf("computed step %q declares no inputs -- the whole point of the amendment is that this cannot happen", step)
			continue
		}
		if !ValidComputedStepInputClass(inputs.Class) {
			t.Errorf("computed step %q declares input class %q, which is not in the closed vocabulary", step, inputs.Class)
		}
		// Execution is required, and the empty value is NOT a member: an
		// undeclared execution must not read as either answer. A step that
		// forgot to say would otherwise default to the permissive one.
		if !ValidComputedStepExecution(inputs.Execution) {
			t.Errorf("computed step %q declares execution %q, which is not in the closed vocabulary -- an undeclared execution must not read as server-executed", step, inputs.Execution)
		}
		// The class and the kinds must agree in BOTH directions. A
		// fact-reading step with no kinds is an undeclared input wearing a
		// declaration; a non-fact-reading step WITH kinds is a declaration
		// contradicting its own class. Either would let the parity
		// classifier read a confident answer off an incoherent row.
		switch inputs.Class {
		case ComputedInputFactKinds:
			if len(inputs.FactKinds) == 0 {
				t.Errorf("computed step %q declares class %q with NO fact kinds", step, inputs.Class)
			}
		case ComputedInputResolvedMemberSet:
			if len(inputs.FactKinds) != 0 {
				t.Errorf("computed step %q declares class %q yet names fact kinds %v", step, inputs.Class, inputs.FactKinds)
			}
		}
	}

	// Both tables are keyed by step and must cover the same steps. A step
	// present in one and absent from the other is the drift the obligation
	// kinds/steps pair already guards against, applied to the third table.
	for _, obligation := range AnswerObligationVocabulary() {
		step, hasStep := StepForComputedObligation(obligation)
		if !hasStep {
			continue
		}
		if _, declared := InputsForComputedStep(step); !declared {
			t.Errorf("obligation %q names step %q, which has no input declaration", obligation, step)
		}
	}
}

// TestRankCohortInputsAreTheKindsItsSignalsActuallyRead is the R3 assertion:
// the declaration is built from what RankCohort EXECUTES, not from what its
// comment claims.
//
// RankCohort's five signal families each read exactly one fact kind --
// investmentMixSignal reads FactInvestment, healthRiskSignal FactHealth,
// deficiencySeveritySignal FactOperationalDeficiencies, readinessGapSignal
// FactReadiness, workloadPressureSignal FactWorkload. cohortRankingFormulaKinds
// is that same set, already named once in this package, and the declaration
// REFERENCES it rather than restating it: two hand-maintained copies of one
// formula's inputs is the drift this package has paid for repeatedly.
func TestRankCohortInputsAreTheKindsItsSignalsActuallyRead(t *testing.T) {
	inputs, declared := InputsForComputedStep(ComputedStepRankCohort)
	if !declared {
		t.Fatal("rank_cohort declares no inputs")
	}
	if inputs.Class != ComputedInputFactKinds {
		t.Fatalf("rank_cohort input class = %q, want %q -- it reads canonical facts", inputs.Class, ComputedInputFactKinds)
	}
	if !sameKindSet(inputs.FactKinds, cohortRankingFormulaKinds) {
		t.Fatalf("rank_cohort declared inputs = %v, want the ranking formula's own kinds %v", inputs.FactKinds, cohortRankingFormulaKinds)
	}
	// Named individually as well as by set equality, so that a future edit
	// that changes BOTH the formula set and this declaration together still
	// has to face the question of whether the signals moved with them.
	for _, want := range []FactKind{FactHealth, FactWorkload, FactReadiness, FactOperationalDeficiencies, FactInvestment} {
		if !containsKind(inputs.FactKinds, want) {
			t.Errorf("rank_cohort declared inputs omit %q, which one of its five signal families reads", want)
		}
	}
}

// TestMembershipCardinalityDeclaresNoFactInput pins the other half, and it is
// the half that does the retirement work: `count` is satisfied by counting
// the resolved member set, so a fact kind lost on a counting frame is not an
// input of anything and the loss can be ruled on its own merits.
func TestMembershipCardinalityDeclaresNoFactInput(t *testing.T) {
	inputs, declared := InputsForComputedStep(ComputedStepMembershipCardinality)
	if !declared {
		t.Fatal("membership_cardinality declares no inputs")
	}
	if inputs.Class != ComputedInputResolvedMemberSet {
		t.Fatalf("membership_cardinality input class = %q, want %q", inputs.Class, ComputedInputResolvedMemberSet)
	}
	if len(inputs.FactKinds) != 0 {
		t.Fatalf("membership_cardinality declares fact-kind inputs %v -- it counts members, it does not read facts", inputs.FactKinds)
	}
}

// TestInputsForComputedStepReturnsACopy: the accessor must not hand out the
// package's own backing array. cohortRankingFormulaKinds is shared with the
// engine's unconditional cohort injection, so a caller that sorted or
// appended to a returned slice could reorder what the engine plans to read.
func TestInputsForComputedStepReturnsACopy(t *testing.T) {
	first, _ := InputsForComputedStep(ComputedStepRankCohort)
	if len(first.FactKinds) == 0 {
		t.Fatal("no kinds to mutate -- this test proves nothing")
	}
	first.FactKinds[0] = FactKind("zz_mutated")
	second, _ := InputsForComputedStep(ComputedStepRankCohort)
	if containsKind(second.FactKinds, FactKind("zz_mutated")) {
		t.Fatal("mutating a returned input slice changed the declaration -- the accessor is handing out package state")
	}
	if containsKind(cohortRankingFormulaKinds, FactKind("zz_mutated")) {
		t.Fatal("mutating a returned input slice reached cohortRankingFormulaKinds, which the ENGINE reads")
	}
}

// TestComputedRequirementRowCarriesItsDeclaredInputs is the derivation-side
// assertion. The row is where the parity proof and any future consumer read
// the inputs from, so a correct table with an unwired derivation would be the
// dead tier this package keeps finding.
//
// FactKinds stays EMPTY on a computed row. That is the R1 ruling and it is
// load-bearing: FactKinds means "kinds that can SERVE this cell", and every
// existing reader -- the plan projection included -- would start counting a
// computation's inputs as reads if the inputs landed there.
func TestComputedRequirementRowCarriesItsDeclaredInputs(t *testing.T) {
	rows, capabilities := requirementRowsForCountingFrame(t)
	_ = capabilities

	computed := 0
	for _, row := range rows {
		if row.Kind != ObligationKindComputed || !row.Served() {
			continue
		}
		computed++
		inputs, declared := InputsForComputedStep(row.Step)
		if !declared {
			t.Errorf("row for %q names step %q with no declaration", row.Obligation, row.Step)
			continue
		}
		if row.InputClass != inputs.Class {
			t.Errorf("row for %q: InputClass = %q, want %q", row.Obligation, row.InputClass, inputs.Class)
		}
		if !sameKindSet(row.InputFactKinds, inputs.FactKinds) {
			t.Errorf("row for %q: InputFactKinds = %v, want %v", row.Obligation, row.InputFactKinds, inputs.FactKinds)
		}
		if len(row.FactKinds) != 0 {
			t.Errorf("row for %q: FactKinds = %v on a COMPUTED row -- it must stay empty (R1: inputs are not reads)", row.Obligation, row.FactKinds)
		}
	}
	if computed == 0 {
		t.Fatal("no served computed row was reached, so this test asserted nothing about computed rows")
	}
}

// TestComputedInputKindsAreSortedAndDeduplicated: the rows feed a regenerated
// artifact and a telemetry histogram, both of which must be diffable between
// runs. An unsorted or duplicated input list would make two runs of one frame
// produce different bytes.
func TestComputedInputKindsAreSortedAndDeduplicated(t *testing.T) {
	rows, _ := requirementRowsForRankingFrame(t)
	checked := 0
	for _, row := range rows {
		if row.Kind != ObligationKindComputed || len(row.InputFactKinds) == 0 {
			continue
		}
		checked++
		// VOCABULARY order, not lexical. This assertion was written as a
		// lexical sort first and failed, which was the test being wrong
		// rather than the code: every other closed-vocabulary rendering in
		// this package is in vocabulary order, and lexical order would let
		// a kind RENAMED in a later contract revision silently reorder a
		// persisted artifact. The property that matters is that the order
		// is fixed by the vocabulary, so that is what is asserted.
		if !kindsInVocabularyOrder(row.InputFactKinds) {
			t.Errorf("row for %q: InputFactKinds %v is not in fact-kind vocabulary order", row.Obligation, row.InputFactKinds)
		}
		seen := map[FactKind]bool{}
		for _, kind := range row.InputFactKinds {
			if seen[kind] {
				t.Errorf("row for %q: InputFactKinds %v repeats %q", row.Obligation, row.InputFactKinds, kind)
			}
			seen[kind] = true
		}
	}
	if checked == 0 {
		t.Fatal("no computed row with declared inputs was reached -- this test proved nothing")
	}
}

// TestReadRequirementRowsDeclareNoInputs is the negative direction, and it
// has a positive control in the test above: without it, a derivation that set
// InputClass on EVERY row would pass every assertion here.
func TestReadRequirementRowsDeclareNoInputs(t *testing.T) {
	rows, _ := requirementRowsForRankingFrame(t)
	checked := 0
	for _, row := range rows {
		if row.Kind == ObligationKindComputed {
			continue
		}
		checked++
		if row.InputClass != "" {
			t.Errorf("read row for %q carries InputClass %q -- only a computation has inputs", row.Obligation, row.InputClass)
		}
		if len(row.InputFactKinds) != 0 {
			t.Errorf("read row for %q carries InputFactKinds %v", row.Obligation, row.InputFactKinds)
		}
	}
	if checked == 0 {
		t.Fatal("no read row was reached -- this test proved nothing")
	}
}

// TestRequirementTelemetryCountsComputedInputs: the same-change telemetry
// bar. The resolved input kinds must be readable from the run's own event,
// and they are counted per closed-vocabulary member -- a histogram, never a
// per-row list -- for the reason requirement_telemetry.go's own header gives:
// an omitted zero is indistinguishable from a tier that never ran.
func TestRequirementTelemetryCountsComputedInputs(t *testing.T) {
	rows, _ := requirementRowsForRankingFrame(t)
	summary := RequirementDerivationSummaryFrom(rows)

	if summary.ComputedRowsWithDeclaredInputs == 0 {
		t.Fatal("summary counted no computed row with declared inputs, but the ranking frame derives one")
	}
	classTotal := 0
	for _, count := range summary.ComputedInputClasses {
		classTotal += count
	}
	if classTotal != summary.ComputedRowsWithDeclaredInputs {
		t.Errorf("input-class counts sum to %d, want %d -- every declared computed row lands in exactly one class", classTotal, summary.ComputedRowsWithDeclaredInputs)
	}
	// The kinds themselves must be visible, not merely counted in bulk.
	if summary.ComputedInputKinds[factKindIndexForTest(t, FactHealth)] == 0 {
		t.Error("summary does not record FactHealth as a resolved computed input, but rank_cohort reads it")
	}
	// An observed zero, which is the property the fixed-length array exists
	// to give: a kind no computed step consumes must be present and zero.
	if summary.ComputedInputKinds[factKindIndexForTest(t, FactIdentity)] != 0 {
		t.Error("summary records FactIdentity as a computed input, which no step declares")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// requirementRowsForRankingFrame derives rows for a frame whose defining
// obligation is `ranking` -- a discovered team cohort. That is the frame that
// exercises ComputedStepRankCohort, the fact-reading input class.
func requirementRowsForRankingFrame(t *testing.T) ([]DerivedRequirement, []FactCapability) {
	t.Helper()
	return requirementRowsForGoals(t, GoalRankOrSurvey)
}

// requirementRowsForCountingFrame derives rows for a frame whose defining
// obligation is `count`, exercising ComputedStepMembershipCardinality -- the
// input class that reads no fact and so does the retirement work.
func requirementRowsForCountingFrame(t *testing.T) ([]DerivedRequirement, []FactCapability) {
	t.Helper()
	return requirementRowsForGoals(t, GoalCountOrAggregate)
}

// requirementRowsForGoals builds a DISCOVERED-team frame through the shipped
// frame layer (never a hand-typed obligation list -- the ticket's own rule)
// and derives its rows against the fixture registry.
//
// Discovered rather than named, because a computed obligation needs a
// POPULATION: the derivation returns `computed_population_absent` for a single
// subject, and a fixture that could not reach a served computed row would make
// every assertion above vacuous. The reach counts in the tests are what catch
// that if this ever stops being true.
func requirementRowsForGoals(t *testing.T, goals ...InvestigationGoal) ([]DerivedRequirement, []FactCapability) {
	t.Helper()
	capabilities := fixtureCapabilities()
	frame := DeriveFrameObligations(QuestionFrame{
		Goals: goals,
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam},
		},
		Temporal: TemporalIntentCurrent,
		Version:  QuestionFrameVersion,
	}, nil)
	return DeriveRequirements(frame, GenerateObligationSeed(capabilities), capabilities), capabilities
}

func containsKind(kinds []FactKind, want FactKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// sameKindSet compares as SETS, so a declaration that is correct but ordered
// differently from the formula's own slice is not reported as a mismatch --
// the ordering property has its own test, and conflating the two would make a
// sort bug and a wrong-kinds bug indistinguishable.
func sameKindSet(left, right []FactKind) bool {
	if len(left) != len(right) {
		return false
	}
	for _, kind := range left {
		if !containsKind(right, kind) {
			return false
		}
	}
	return true
}

// factKindIndexForTest resolves a kind's position in the closed vocabulary the
// telemetry histogram is indexed by. It FAILS rather than returning zero for
// an unknown kind: index 0 is a real bucket, so a silent zero would make an
// assertion about one kind quietly become an assertion about another.
func factKindIndexForTest(t *testing.T, kind FactKind) int {
	t.Helper()
	index, ok := factKindIndex(kind)
	if !ok {
		t.Fatalf("fact kind %q is not in the closed vocabulary", kind)
	}
	return index
}

// kindsInVocabularyOrder reports whether kinds appear in the same relative
// order as the contracts' closed fact-kind vocabulary. A kind outside the
// vocabulary fails the check rather than being skipped: a declaration naming
// an unknown kind is a defect, and skipping it would let one hide inside an
// otherwise-ordered list.
func kindsInVocabularyOrder(kinds []FactKind) bool {
	last := -1
	for _, kind := range kinds {
		index, ok := factKindIndex(kind)
		if !ok || index <= last {
			return false
		}
		last = index
	}
	return true
}

// TestSortedFactKindsPreservesAKindOutsideTheVocabulary is the POSITIVE FIXTURE
// for a tier that had none.
//
// FOUND BY A CODEX ROUND AS A SURVIVING MUTATION, and re-executed here before
// being ledgered: deleting `sortedFactKinds`'s unknown-kind preservation loop
// left the entire package suite green, because every kind in the live
// declaration table is a vocabulary member and nothing ever reached the branch.
// That is the "a gate tier with no positive fixture can be dead for its whole
// life and read as green" failure, arriving inside the very change that adds
// the tier.
//
// WHY THE BRANCH MATTERS, which is what makes this worth a test rather than a
// deletion: the branch exists so a declaration naming a kind outside the closed
// vocabulary produces a LONGER list containing the bad kind, which the totality
// test can see and fail on. Without it the bad kind is silently dropped, the
// list reads as merely shorter, and a declaration error becomes invisible --
// and a dropped input is precisely what makes the parity classifier rule a loss
// "not required" and authorize a retirement that removes a real read.
func TestSortedFactKindsPreservesAKindOutsideTheVocabulary(t *testing.T) {
	outsider := FactKind("zz_not_in_the_vocabulary")
	if _, member := factKindIndex(outsider); member {
		t.Fatalf("%q is in the vocabulary, so this test cannot exercise the branch it exists for", outsider)
	}

	got := sortedFactKinds([]FactKind{FactWorkload, outsider, FactHealth})

	if len(got) != 3 {
		t.Fatalf("sortedFactKinds dropped a declared input: got %v, want all three kinds", got)
	}
	if !containsKind(got, outsider) {
		t.Fatalf("sortedFactKinds dropped %q -- a declaration error must read as a WRONG list, never as a shorter one", outsider)
	}
	// The vocabulary members still lead, in vocabulary order, and the
	// outsider is appended rather than interleaved -- otherwise the ordering
	// guarantee the artifact depends on would hold only for valid tables.
	if !kindsInVocabularyOrder(got[:2]) {
		t.Errorf("known kinds are not in vocabulary order: %v", got)
	}
	if got[2] != outsider {
		t.Errorf("the out-of-vocabulary kind is not appended last: %v", got)
	}
}

// TestComputedStepExecutionMatchesTheTree pins each step's declared execution
// against whether the tree actually runs it.
//
// THE HISTORY THIS TEST CARRIES. `membership_cardinality` was declared with an
// input class and no execution statement, and the parity proof read "consumes
// no fact" as evidence about the ANSWER -- clearing five blocking cells and
// reporting two planning authorities retirable. Nothing executed that step:
// the cardinality reached the user through narration, so the reads those
// authorities cause could still change the answer. The execution field was
// added to say so, and this test pinned the step DECLARED-ONLY with a note
// that "flipping it without wiring the step re-opens the defect."
//
// THE STEP IS NOW WIRED, so the pin moved -- and it moved AFTER the wiring,
// not to make room for it. `ComputeMembershipCardinality` counts the resolved
// member set and `finalizeResult` states the result on the served document.
// The claim that it genuinely runs is not made here: it is made by
// TestTheCountObligationReachesTheServedDocumentAsACountableField, which
// drives Engine.Investigate and reads the count off the document the engine
// served. This test's job is the AGREEMENT between the two records.
//
// IT IS A PROPERTY OVER THE STEP VOCABULARY, not two hand-pinned constants.
// Two constants are exactly what let one record be flipped while the other
// stayed: the shadow's observation declaration and this table are two
// statements of one fact, and either alone can authorize a retirement. Stated
// as a property, a step added later cannot be declared executed while the
// layer that would have to see its output still says it cannot.
func TestComputedStepExecutionMatchesTheTree(t *testing.T) {
	checked := 0
	for _, step := range ComputedObligationStepVocabulary() {
		inputs, ok := InputsForComputedStep(step)
		if !ok {
			t.Errorf("step %q has no input declaration", step)
			continue
		}
		if !ValidComputedStepExecution(inputs.Execution) {
			t.Errorf("step %q declares execution %q, which is not a vocabulary member", step, inputs.Execution)
			continue
		}
		obligation, found := obligationForComputedStepInTest(step)
		if !found {
			t.Errorf("step %q satisfies no obligation -- the two vocabulary tables have drifted", step)
			continue
		}
		observation, declared := obligationObservations[obligation]
		if !declared {
			t.Errorf("obligation %q has no observation declaration", obligation)
			continue
		}
		// THE INVARIANT. A step the server executes produces something the
		// answer can carry, so the shadow can observe the obligation. A step
		// nothing executes leaves the value to reach the reader some other
		// way, which the shadow by construction cannot see. The two records
		// disagreeing means one of them is authorizing retirements on a
		// mechanism that is not the answering mechanism.
		wantObserved := inputs.Execution == ComputedStepServerExecuted
		if observation.observed != wantObserved {
			t.Errorf("step %q declares execution %q while the shadow records obligation %q as observed=%v -- "+
				"the two records disagree, and one of them is authorizing retirements",
				step, inputs.Execution, obligation, observation.observed)
		}
		checked++
	}
	if checked != ComputedObligationStepCount {
		t.Fatalf("checked %d of %d computed steps -- a step that skipped its assertions proves nothing about itself",
			checked, ComputedObligationStepCount)
	}

	// Both live steps are wired today, so the assertion above is satisfied
	// by two `server_executed` rows. Stated explicitly, because a property
	// every member satisfies the same way cannot distinguish itself from a
	// property that always holds: the DECLARED-ONLY side of the invariant is
	// exercised by TestADeclaredOnlyStepIsNotReportedAsWired below.
	for _, step := range ComputedObligationStepVocabulary() {
		inputs, _ := InputsForComputedStep(step)
		if inputs.Execution != ComputedStepServerExecuted {
			t.Fatalf("step %q is declared %q; this comment and the declared-only companion test are stale",
				step, inputs.Execution)
		}
	}
}

// obligationForComputedStepInTest inverts the step table, so the property
// above reads the shipped mapping rather than a copy of it.
func obligationForComputedStepInTest(step ComputedObligationStep) (AnswerObligation, bool) {
	for _, obligation := range AnswerObligationVocabulary() {
		if mapped, ok := StepForComputedObligation(obligation); ok && mapped == step {
			return obligation, true
		}
	}
	return "", false
}

// TestADeclaredOnlyStepIsNotReportedAsWired keeps the declared-only half of
// the invariant alive now that no live step declares it.
//
// A closed token no input can produce is a dead tier that reads as green --
// the same class this file's own histogram tests were written for. Both live
// steps are `server_executed` today, so the ONLY way to exercise the other
// side is a constructed row. That is stated rather than hidden: the row is
// input to the summary fold, which is the unit under test.
func TestADeclaredOnlyStepIsNotReportedAsWired(t *testing.T) {
	rows := []DerivedRequirement{{
		Kind:          ObligationKindComputed,
		Step:          ComputedStepMembershipCardinality,
		InputClass:    ComputedInputResolvedMemberSet,
		StepExecution: ComputedStepDeclaredOnly,
	}}
	summary := RequirementDerivationSummaryFrom(rows)

	declaredOnly := summary.ComputedStepExecutions[executionIndexForTest(t, ComputedStepDeclaredOnly)]
	serverExecuted := summary.ComputedStepExecutions[executionIndexForTest(t, ComputedStepServerExecuted)]
	if declaredOnly != 1 {
		t.Errorf("declared-only bucket = %d, want 1 -- the tier a token can never reach is a tier that cannot fail", declaredOnly)
	}
	if serverExecuted != 0 {
		t.Errorf("server-executed bucket = %d for a declared-only row, want 0", serverExecuted)
	}
}

// TestEveryHistogramThisSliceAddsIsIndexedByVocabularyPosition is a CLASS fix,
// written after a second review round found the same defect a second time.
//
// THE CLASS: a closed-vocabulary histogram whose test asserts only a TOTAL.
// Round 1 found `sortedFactKinds`'s unfixtured branch and I fixed that one
// instance. Round 2 then found `ComputedStepExecutions`, which no test touched
// at all. Sweeping for the shape rather than the instance found a THIRD site
// the round did not report: `ComputedInputClasses` was asserted only by its sum
// against ComputedRowsWithDeclaredInputs, so collapsing every class into slot 0
// keeps the sum correct and the suite green -- while the log line, which labels
// slots by vocabulary position, reports the wrong token.
//
// Both misbucketings were confirmed by mutation before this test was written:
// each left `go test ./internal/contextfabric/ -count=1` green.
//
// WHY THIS SHAPE OF TEST. Asserting one bucket's value per histogram would fix
// today's three sites and rot the moment a fourth is added or a vocabulary
// grows. This asserts the PROPERTY the log line depends on -- that each index
// function is a bijection onto its vocabulary's positions -- for every member
// of every vocabulary this slice introduced. A new member is covered because it
// is in the vocabulary, not because someone remembered to add a case.
func TestEveryHistogramThisSliceAddsIsIndexedByVocabularyPosition(t *testing.T) {
	checked := 0

	// The log line renders slot N with vocabulary member N's name. If the
	// index function disagrees with the vocabulary's own order, the label and
	// the count belong to different tokens -- which is how an UNWIRED step
	// gets reported as wired.
	for want, member := range ComputedStepInputClassVocabulary() {
		got, ok := computedStepInputClassIndex(member)
		if !ok {
			t.Errorf("input class %q has no histogram slot", member)
			continue
		}
		if got != want {
			t.Errorf("input class %q indexes to slot %d, but the log line labels that slot %q", member, got, ComputedStepInputClassVocabulary()[got])
		}
		checked++
	}

	for want, member := range ComputedStepExecutionVocabulary() {
		got, ok := computedStepExecutionIndex(member)
		if !ok {
			t.Errorf("step execution %q has no histogram slot", member)
			continue
		}
		if got != want {
			t.Errorf("step execution %q indexes to slot %d, but the log line labels that slot %q", member, got, ComputedStepExecutionVocabulary()[got])
		}
		checked++
	}

	for want, member := range contractsv1.ContextFabricFactKindVocabulary() {
		got, ok := factKindIndex(member)
		if !ok {
			t.Errorf("fact kind %q has no histogram slot", member)
			continue
		}
		if got != want {
			t.Errorf("fact kind %q indexes to slot %d, not its vocabulary position", member, got)
		}
		checked++
	}

	// A non-member must NOT resolve to a slot. Without this, an index function
	// that returned (0, true) for everything would satisfy every check above
	// for the member at position 0.
	if _, ok := computedStepInputClassIndex(ComputedStepInputClass("zz_not_a_class")); ok {
		t.Error("a non-member input class resolved to a histogram slot")
	}
	if _, ok := computedStepExecutionIndex(ComputedStepExecution("zz_not_an_execution")); ok {
		t.Error("a non-member execution resolved to a histogram slot")
	}
	if _, ok := factKindIndex(FactKind("zz_not_a_kind")); ok {
		t.Error("a non-member fact kind resolved to a histogram slot")
	}

	if checked == 0 {
		t.Fatal("no vocabulary member was checked -- this test proved nothing")
	}
}

// TestComputedStepExecutionCountsLandInTheRightBucket is the behavioural half:
// the property test above pins the index functions, and this pins that a real
// summary routes a DECLARED-ONLY row to the declared-only bucket.
//
// Both halves are needed and neither subsumes the other -- correct indexing
// with a miswired fold, or a correct fold over a broken index, each produce the
// same wrong log line.
func TestComputedStepExecutionCountsLandInTheRightBucket(t *testing.T) {
	rows, _ := requirementRowsForCountingFrame(t)
	summary := RequirementDerivationSummaryFrom(rows)

	declaredOnly := summary.ComputedStepExecutions[executionIndexForTest(t, ComputedStepDeclaredOnly)]
	serverExecuted := summary.ComputedStepExecutions[executionIndexForTest(t, ComputedStepServerExecuted)]

	// A counting frame's only computation is membership_cardinality, and the
	// server now executes it. Reporting a wired step as declared-only would
	// understate what the answer is built from, which is the mirror of the
	// over-claim this histogram was originally written to prevent -- both
	// directions are misinformation, and the bucket has to be right.
	if serverExecuted == 0 {
		t.Errorf("a counting frame recorded %d server-executed computed rows, want at least one", serverExecuted)
	}
	if declaredOnly != 0 {
		t.Errorf("a counting frame recorded %d DECLARED-ONLY computed rows -- the server computes this cardinality and states it on the answer", declaredOnly)
	}

	classTotal := 0
	for _, count := range summary.ComputedInputClasses {
		classTotal += count
	}
	memberSet := summary.ComputedInputClasses[classIndexForTest(t, ComputedInputResolvedMemberSet)]
	if memberSet == 0 || memberSet != classTotal {
		t.Errorf("counting frame input classes = %v, want every declared row in the resolved_member_set bucket", summary.ComputedInputClasses)
	}
}

func executionIndexForTest(t *testing.T, value ComputedStepExecution) int {
	t.Helper()
	index, ok := computedStepExecutionIndex(value)
	if !ok {
		t.Fatalf("execution %q is not in the closed vocabulary", value)
	}
	return index
}

func classIndexForTest(t *testing.T, value ComputedStepInputClass) int {
	t.Helper()
	index, ok := computedStepInputClassIndex(value)
	if !ok {
		t.Fatalf("input class %q is not in the closed vocabulary", value)
	}
	return index
}
