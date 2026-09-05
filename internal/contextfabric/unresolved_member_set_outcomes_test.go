package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE CASE NO LAYER ABOVE ASSEMBLY CAN DECIDE.
//
// The requirement derivation refuses a computed step whose FRAME can never
// produce a member set. Every condition it tests is decidable before
// retrieval: is the expression a cohort variant, does it declare a member
// kind, is there a discovery arm for that kind.
//
// There is a fourth condition and it is a RUNTIME fact: a perfectly servable
// kind whose search returns no members. `DiscoveredCohort` returns a nil
// cohort when it retains none, so the frame was legitimate, the arm exists,
// the search ran, and there is nothing to rank. The requirement was seeded
// `satisfied` on the strength of naming a step, the step never ran, and until
// the change these tests carry, nothing on the served document said so.
//
// THE FIXTURES DECLARE `team`, WHICH HAS A PROVEN ARM. That is what makes
// these tests about the runtime hole and not about the derivation's own guard:
// if the fixture kind were unservable the derivation would refuse it before
// retrieval, the planning row would already read `unavailable`, and the sweep
// under test would correctly do nothing -- a green test proving the wrong
// thing.

// rankingCohortFrame is a discovered-kind cohort over `memberKind`, ranked.
//
// Built THROUGH DeriveFrameObligations like every other frame fixture in this
// package, so a change to the obligation tables moves it rather than leaving
// it asserting against a shape the derivation no longer produces.
func rankingCohortFrame(memberKind SubjectKind) *QuestionFrame {
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		discoveredExpression(memberKind),
		TemporalIntentCurrent,
		nil,
	)
	return &frame
}

// outcomeRowsFor returns every row on the SERVED document for one obligation
// at one stage.
//
// It reads the served document and nothing else. A test that reached into the
// engine would measure the engine's opinion of what it sent rather than what
// it sent.
func outcomeRowsFor(result InvestigationResult, obligation AnswerObligation, stage contractsv1.ContextFabricOutcomeStage) []RequirementOutcomeRow {
	var found []RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Obligation == string(obligation) && row.Stage == stage {
			found = append(found, row)
		}
	}
	return found
}

// runRankingInvestigation drives Engine.Investigate end to end for a ranking
// question, with whatever cohort the graph returns.
//
// `cohort` nil is the case under test: a servable kind whose search retained
// no members.
func runRankingInvestigation(t *testing.T, memberKind SubjectKind, cohort *Cohort) InvestigationResult {
	t.Helper()
	frame := rankingCohortFrame(memberKind)
	engine := newCountingEngine(t, cohort, frame, &recordingTelemetry{})
	return runCountingRequest(t, engine, 0)
}

// TestARankingRequirementOverAnEmptySearchIsStatedUnavailable IS THE HARM.
//
// A `team` ranking question -- a kind with a proven discovery arm -- whose
// search retains no members. At the parent the served document carries a
// planning row saying the ranking requirement is satisfied, a plan naming
// `rank_cohort` as its server, and nothing anywhere saying the step never ran.
// The reader is told an ordering was produced over a set that was never
// resolved.
//
// THE HARM ASSERTION IS THE ASSEMBLED-RESULT ROW, and it is stated separately
// from the mechanism assertions so a reader of a failure knows which one
// carries it: without that row the answer claims an ordering nothing computed.
func TestARankingRequirementOverAnEmptySearchIsStatedUnavailable(t *testing.T) {
	t.Parallel()
	// The premise. If `team` ever loses its discovery arm this fixture stops
	// being about the runtime hole and starts being about the derivation's
	// own guard, and every assertion below would pass for the wrong reason.
	if !CohortMemberSetResolvable(rankingCohortFrame(SubjectTeam).SubjectExpression) {
		t.Fatal("the fixture kind has no discovery arm, so the derivation would refuse this frame before retrieval and this test would prove nothing about the post-discovery correction")
	}

	result := runRankingInvestigation(t, SubjectTeam, nil)

	// The derivation was RIGHT to serve it: the frame is legitimate and the
	// arm exists. Asserting this is what keeps the fix from being "make the
	// derivation pessimistic", which would refuse real questions.
	planning := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning)
	if len(planning) == 0 {
		t.Fatal("the served document carries no PLANNING row for `ranking`; this fixture never derived the requirement it is about")
	}
	for _, row := range planning {
		if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
			t.Fatalf("planning row for `ranking` reads %q, want %q -- the derivation must still serve a legitimate frame over a servable kind, or this test is measuring the pre-retrieval guard instead of the post-discovery correction",
				row.Outcome, contractsv1.ContextFabricRequirementSatisfied)
		}
	}

	// THE HARM.
	assembled := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(assembled) != 1 {
		t.Fatalf("the served document carries %d assembled-result rows for `ranking`, want exactly 1: the search resolved no member set, `rank_cohort` never ran, and the answer says nothing about it -- it claims an ordering nothing computed", len(assembled))
	}

	row := assembled[0]
	if row.Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Errorf("assembled `ranking` outcome = %q, want %q", row.Outcome, contractsv1.ContextFabricRequirementUnavailable)
	}
	if row.Impact != contractsv1.ContextFabricAnswerImpactDimension {
		t.Errorf("assembled `ranking` impact = %q, want %q -- the reader asked in what order and gets no answer to that question at all, which is the dimension and not the scope or the depth",
			row.Impact, contractsv1.ContextFabricAnswerImpactDimension)
	}
	// Pinned to the SHIPPED mapping rather than to a literal, so a re-point
	// of the cause table is caught while this test stays silent about whether
	// that table's token names the mechanism well -- which is argued under
	// its own ticket.
	wantCause := unavailableRequirementCause(RequirementReasonComputedPopulationAbsent)
	if wantCause == "" {
		t.Fatal("the shipped mapping yields no coverage code for computed_population_absent, so the assertion below would be vacuous")
	}
	if row.CauseCoverage != wantCause {
		t.Errorf("assembled `ranking` cause = %q, want %q", row.CauseCoverage, wantCause)
	}
	if !row.CauseObserved {
		t.Error("assembled `ranking` row reports CauseObserved false; assembly LOOKED for a member set and there was none, so nothing here defaulted")
	}
	// The identity is the derivation's, carried, never minted here.
	if row.Requirement == "" {
		t.Error("assembled `ranking` row carries no requirement identity, so no reader can join it to the plan row it corrects")
	}
	if row.Requirement != planning[0].Requirement {
		t.Errorf("assembled `ranking` row names requirement %q, the planning row names %q -- two authorities for which requirement a row is about",
			row.Requirement, planning[0].Requirement)
	}
}

// TestARankingRequirementOverAResolvedMemberSetIsLeftAlone is the CONTROL, and
// without it the test above is satisfied by a sweep that refuses everything.
//
// Same frame, same kind, same question -- the ONLY difference is that the
// search retained members. Nothing may be appended.
func TestARankingRequirementOverAResolvedMemberSetIsLeftAlone(t *testing.T) {
	t.Parallel()
	cohort := countingCohort(SubjectTeam, 3)
	if cohort == nil || len(cohort.Members) == 0 {
		t.Fatal("the control fixture carries no members, so it is the same case as the test above and controls nothing")
	}

	result := runRankingInvestigation(t, SubjectTeam, cohort)

	if assembled := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult); len(assembled) != 0 {
		t.Fatalf("a ranking question over a RESOLVED member set acquired %d assembled-result row(s) (%+v); the correction must fire on an absent member set and nothing else", len(assembled), assembled)
	}
	// The document still carries the requirement, so the absence above is a
	// decision and not a fixture that derived nothing.
	if planning := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning); len(planning) == 0 {
		t.Fatal("the control derived no `ranking` requirement at all, so its empty assembled set proves nothing")
	}
}

// TestARequirementTheDerivationAlreadyRefusedIsNotRestated closes the
// double-statement direction.
//
// A `named_subject` ranking frame can never produce a member set, so the
// derivation refuses it before retrieval and the planning row already carries
// the reason. Re-stating the same refusal at the assembled stage would publish
// one cell twice under two stage labels, which reads as two findings.
func TestARequirementTheDerivationAlreadyRefusedIsNotRestated(t *testing.T) {
	t.Parallel()
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		namedExpression(SubjectTeam),
		TemporalIntentCurrent,
		nil,
	)
	// The premise: this frame is one the derivation refuses.
	if CohortMemberSetResolvable(frame.SubjectExpression) {
		t.Fatal("the fixture frame CAN resolve a member set, so the derivation would serve it and this test would be measuring the served case instead of the already-refused one")
	}

	engine := newCountingEngine(t, nil, &frame, &recordingTelemetry{})
	result := runCountingRequest(t, engine, 0)

	planning := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning)
	if len(planning) == 0 {
		t.Fatal("the fixture derived no `ranking` requirement, so it cannot show a refusal being restated or not")
	}
	refused := false
	for _, row := range planning {
		if row.Outcome == contractsv1.ContextFabricRequirementUnavailable {
			refused = true
		}
	}
	if !refused {
		t.Fatal("no planning row for `ranking` reads unavailable, so the derivation did not refuse this frame and this test controls nothing")
	}
	if assembled := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult); len(assembled) != 0 {
		t.Fatalf("a requirement the derivation already refused acquired %d assembled-result row(s); the same refusal is now stated twice under two stage labels", len(assembled))
	}
}

// TestTheCountSiblingStillStatesExactlyOneRow is the ordering pin.
//
// `count` and `ranking` are both computed steps that run over the resolved
// member set, so the general sweep would append a row for `count` too if the
// count step had not already appended its own. Exactly one row, from the
// richer producer, is the property.
func TestTheCountSiblingStillStatesExactlyOneRow(t *testing.T) {
	t.Parallel()
	// The premise this pin depends on: both obligations really are in the
	// sweep's population. If `count` ever stops running over the member set,
	// this test stops testing an interaction.
	if !stepRunsOverResolvedMemberSet(ObligationCount) || !stepRunsOverResolvedMemberSet(ObligationRanking) {
		t.Fatalf("count=%v ranking=%v; both must run over the resolved member set or this ordering pin has nothing to order",
			stepRunsOverResolvedMemberSet(ObligationCount), stepRunsOverResolvedMemberSet(ObligationRanking))
	}

	frame := countingFrame(SubjectTeam)
	engine := newCountingEngine(t, nil, frame, &recordingTelemetry{})
	result := runCountingRequest(t, engine, 0)

	assembled := outcomeRowsFor(result, ObligationCount, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(assembled) != 1 {
		t.Fatalf("the served document carries %d assembled-result rows for `count`, want exactly 1 -- the count step and the general sweep both spoke for one cell", len(assembled))
	}
	if assembled[0].Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Errorf("assembled `count` outcome = %q, want %q", assembled[0].Outcome, contractsv1.ContextFabricRequirementUnavailable)
	}
}

// TestFinalizingTwiceStatesOneUnavailableRankingRow is the re-entry guard.
//
// finalizeResult runs again on the stage-3 retry and again after the outcome
// layer narrows candidates, and an unguarded append would publish two accounts
// of one cell.
func TestFinalizingTwiceStatesOneUnavailableRankingRow(t *testing.T) {
	t.Parallel()
	frame := rankingCohortFrame(SubjectTeam)
	engine := newCountingEngine(t, nil, frame, &recordingTelemetry{})
	result := runCountingRequest(t, engine, 0)

	before := outcomeRowsFor(result, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(before) != 1 {
		t.Fatalf("the first finalization produced %d assembled `ranking` row(s), want 1; the re-entry guard has nothing to guard", len(before))
	}

	plan := AnswerPlan{}
	if result.AnswerPlan != nil {
		plan = *result.AnswerPlan
	}
	again := engine.finalizeResult(result, plan, frame)

	after := outcomeRowsFor(again, ObligationRanking, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(after) != 1 {
		t.Fatalf("re-finalizing produced %d assembled `ranking` row(s), want 1 -- a reader would receive two accounts of one requirement", len(after))
	}
}

// TestTheSweepPopulationComesFromTheDeclarationTable pins HOW the sweep
// chooses what to correct.
//
// The population is every computed obligation whose server step declares that
// it RUNS OVER the resolved member set. A hand-written list of obligation
// names would be correct today and wrong the first time a third step is added
// -- which is exactly the shape of the defect this whole change removes.
//
// Quantified over the shipped step vocabulary, so a new step is covered by the
// act of declaring itself, with a negative control on the read side.
func TestTheSweepPopulationComesFromTheDeclarationTable(t *testing.T) {
	t.Parallel()
	// Keyed on the OBLIGATION, because that is what a requirement row carries
	// and therefore what the sweep reads. The step-side coverage check below
	// is what stops that keying from silently missing a declared step.
	declaring := 0
	reachableSteps := make(map[ComputedObligationStep]bool, ComputedObligationStepCount)
	for _, obligation := range AnswerObligationVocabulary() {
		kind, known := KindOfObligation(obligation)
		if !known || kind != ObligationKindComputed {
			continue
		}
		step, named := StepForComputedObligation(obligation)
		if !named {
			t.Errorf("computed obligation %q names no server step, so a requirement row carrying it routes nowhere", obligation)
			continue
		}
		reachableSteps[step] = true
		inputs, declared := InputsForComputedStep(step)
		if !declared {
			t.Errorf("step %q has no declared inputs, so the sweep cannot tell whether it runs over the member set", step)
			continue
		}
		want := inputs.RunsOverResolvedMemberSet
		if got := stepRunsOverResolvedMemberSet(obligation); got != want {
			t.Errorf("obligation %q (step %q): stepRunsOverResolvedMemberSet = %v, the declaration says RunsOverResolvedMemberSet = %v", obligation, step, got, want)
		}
		if want {
			declaring++
		}
	}
	if declaring == 0 {
		t.Fatal("no shipped step declares that it runs over the resolved member set, so the sweep's population is empty and every assertion above is vacuous")
	}
	// COVERAGE IN THE OTHER DIRECTION: every declared step must be reachable
	// from some obligation. A step no obligation names could declare that it
	// runs over the member set and never be corrected, and the loop above
	// could not see it.
	for _, step := range ComputedObligationStepVocabulary() {
		if !reachableSteps[step] {
			t.Errorf("step %q is declared but no obligation names it, so no requirement row can ever route the sweep to it", step)
		}
	}

	// NEGATIVE CONTROL. A READ obligation names no computed step at all, so
	// it must never enter the sweep. Without this the function could return
	// true for everything and every assertion above would still pass.
	for _, obligation := range AnswerObligationVocabulary() {
		kind, known := KindOfObligation(obligation)
		if !known || kind == ObligationKindComputed {
			continue
		}
		if stepRunsOverResolvedMemberSet(obligation) {
			t.Errorf("read obligation %q entered the sweep's population; only a computed obligation names a server step", obligation)
		}
	}
}

// TestMemberSetResolvedTellsAnAbsentSetFromAnEmptyOne pins the distinction the
// whole correction rests on.
//
// A nil cohort is "no member set was resolved". A non-nil cohort carrying zero
// members is a resolved population that happens to be empty, and a count over
// it is genuinely satisfied at 0/0. Collapsing the two would either invent a
// refusal for a real empty answer or hide a real absence.
func TestMemberSetResolvedTellsAnAbsentSetFromAnEmptyOne(t *testing.T) {
	t.Parallel()
	if memberSetResolved(nil) {
		t.Error("memberSetResolved(nil) is true; a nil cohort is the shape a discovery that retained nothing returns")
	}
	empty := &Cohort{Kind: SubjectTeam, Members: []CohortMember{}, Complete: true}
	if !memberSetResolved(empty) {
		t.Error("memberSetResolved reported false for a resolved cohort carrying zero members; an empty population is an answer, not an absence")
	}
	if !memberSetResolved(countingCohort(SubjectTeam, 2)) {
		t.Error("memberSetResolved reported false for a cohort carrying members")
	}
	// The sweep must follow that distinction, not just the predicate.
	rows := []RequirementOutcomeRow{{
		Stage:       contractsv1.ContextFabricOutcomeStagePlanning,
		Requirement: "ranking/member/team",
		Obligation:  string(ObligationRanking),
		Outcome:     contractsv1.ContextFabricRequirementSatisfied,
	}}
	if got := appendUnresolvedMemberSetOutcomes(rows, empty); len(got) != len(rows) {
		t.Fatalf("the sweep appended %d row(s) over a resolved but empty cohort; it must fire on an absent member set only", len(got)-len(rows))
	}
	if got := appendUnresolvedMemberSetOutcomes(rows, nil); len(got) != len(rows)+1 {
		t.Fatalf("the sweep appended %d row(s) over an ABSENT member set, want 1 -- the control above would otherwise pass because the sweep never fires at all", len(got)-len(rows))
	}
}
