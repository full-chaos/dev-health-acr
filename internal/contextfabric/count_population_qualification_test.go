package contextfabric

// A count over a population the answer never saw all of is not exact.
//
// WHAT THESE TESTS ARE FOR. `ComputeMembershipCardinality` counts the
// RESOLVED member set and initializes Served == Declared == len(Members).
// Whether that set is the whole population is a separate question, and the
// answer already carries it -- `Cohort.Complete` and `Cohort.Truncated`.
// The step's own file header says the count is "a lower bound on the
// population, and this file does not pretend otherwise". The emitted row
// pretended: `satisfied`, impact `none`, no cause, and the completeness
// derivation then read `complete`.
//
// So a discovery clamp at N produced an answer stating an exact count over a
// population it had stopped short of, and stating it as the strongest
// completeness there is. That is the "missing is not healthy" shape from the
// other side -- a truncated census and a full one shared one token.
//
// EVERY TEST HERE DRIVES THE PUBLIC ENTRY POINT and reads the SERVED
// document. None constructs the row it asserts on: a regression test that
// builds the decision it asserts on stays green when the production bug
// returns, and this package has paid for that shape more than once.
//
// THE WIRE TOKEN, NOT THE GO SYMBOL. These tests compare against the literal
// string `population_truncated` rather than the exported constant. That is
// deliberate and it is the same discipline the schema-parity test states: a
// test that reads the Go vocabulary function agrees with the Go side BY
// CONSTRUCTION, and the thing a consumer receives is the token. Comparing to
// the literal also keeps this file compiling at the parent commit, so the
// red-first proof is a behavioural failure rather than a build failure -- a
// build failure proves the identifier is absent, never that the behaviour is
// wrong.

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// populationTruncatedToken is the WIRE token, spelled once. The published
// JSON Schema enumerates this string; `TestThePopulationQualifyingCodeIsTheWireToken`
// in internal/contracts/v1 binds the Go constant to it.
const populationTruncatedToken = contractsv1.ContextFabricCoverageDetailCode("population_truncated")

// censusCohort builds a member set of the requested size with the cohort's
// own coverage flags set explicitly.
//
// It exists beside `countingCohort` rather than replacing it because the two
// say different things: `countingCohort` is a COMPLETE census and its
// `Complete: true` is an assertion about the fixture, while this one is
// parameterised precisely so a test can state which coverage arm it drives.
func censusCohort(kind SubjectKind, size int, complete, truncated bool) *Cohort {
	cohort := countingCohort(kind, size)
	cohort.Complete = complete
	cohort.Truncated = truncated
	return cohort
}

// runCensusInvestigation drives Engine.Investigate end to end for a counting
// question over a cohort whose coverage flags the caller states, and returns
// the SERVED result together with the telemetry recorder.
//
// maxMembers is threaded through unchanged: zero means "wide enough that the
// engine's own narrowing never runs", which is what isolates the population
// arms from the member-narrowing arm.
func runCensusInvestigation(t *testing.T, size, maxMembers int, complete, truncated bool) (InvestigationResult, *recordingTelemetry) {
	t.Helper()
	telemetry := &recordingTelemetry{}
	frame := countingFrame(SubjectTeam)
	cohort := censusCohort(SubjectTeam, size, complete, truncated)
	engine := newCountingEngine(t, cohort, frame, telemetry)
	return runCountingRequest(t, engine, maxMembers), telemetry
}

// theCountRow returns the single assembled-result `count` row, failing if
// there is not exactly one.
//
// Exactly one, never "at least one": two cardinalities for one requirement
// give a reader two answers to one question, and a test that took the first
// of several would not notice.
func theCountRow(t *testing.T, result InvestigationResult) RequirementOutcomeRow {
	t.Helper()
	rows := countOutcomeRows(result, contractsv1.ContextFabricOutcomeStageAssembledResult)
	if len(rows) != 1 {
		t.Fatalf("assembled-result `count` rows = %d, want exactly 1", len(rows))
	}
	return rows[0]
}

// assertPopulationQualified is the shared assertion for the two arms that
// change, written once so the two arms cannot drift into asserting different
// things about the same row shape.
//
// THE HARM IS THE LAST ASSERTION. The outcome token and the cause are the
// mechanism; what a reader actually suffers is an answer that calls itself
// `complete` while its own count was taken over a population it never saw.
func assertPopulationQualified(t *testing.T, result InvestigationResult, members int) {
	t.Helper()
	row := theCountRow(t, result)

	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Errorf("count row outcome = %q over an INCOMPLETE population, want %q -- the number is exact over the "+
			"resolved member set and the resolved set is not the population, so calling it satisfied states a "+
			"census the answer never took",
			row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.CauseCoverage != populationTruncatedToken {
		t.Errorf("count row cause_coverage = %q, want %q -- a qualified count must name WHY it is qualified, and "+
			"the qualification is about the population, not about a fact read",
			row.CauseCoverage, populationTruncatedToken)
	}
	if !row.CauseObserved {
		t.Error("count row reports its cause as DEFAULTED; the cohort REPORTED its own incompleteness, so nothing " +
			"here defaulted -- and a defaulted census cause would let any producer opt out of the reduction rule")
	}
	if row.Impact != contractsv1.ContextFabricAnswerImpactScope {
		t.Errorf("count row impact = %q, want %q -- the reader is shown fewer of the counted things than exist; "+
			"the ones shown are unchanged, so this is scope and not depth",
			row.Impact, contractsv1.ContextFabricAnswerImpactScope)
	}
	// Served AND Declared both stay at the member count. This is the whole
	// reason the contract needed an exception: the loss is on an axis these
	// two numbers do not measure, so inventing a larger Declared to make the
	// row "look narrowed" would be a fabricated population size.
	if row.Served != members || row.Declared != members {
		t.Errorf("count row served/declared = %d/%d, want %d/%d -- the count over the resolved set is exact and "+
			"must not be restated as a reduction of a population size nobody measured",
			row.Served, row.Declared, members, members)
	}
	if len(row.Refinements) != 0 {
		t.Errorf("count row records %d refinements; nothing was reduced between declared and served, so a "+
			"refinement here would put a stage's name against a reduction it did not make", len(row.Refinements))
	}
	// THE HARM.
	if got := result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessPartial {
		t.Errorf("the served answer states completeness %q, want %q -- it counted a population it had stopped "+
			"short of and claimed the strongest completeness there is",
			got, contractsv1.ContextFabricAnswerCompletenessPartial)
	}
}

// TestACountOverAClampedPopulationIsNotSatisfied is the discovery-clamp arm:
// the cohort reports Complete=false AND Truncated=true, which is what the
// graph adapter writes when discovery stopped at MaxCohortMembers, and what
// the reuse degrade writes when it drops members.
//
// No member-narrowing step ran, so nothing on the plan's own narrowing record
// can catch this. That is precisely why the cohort's flags have to be
// consulted: they are the only witness.
func TestACountOverAClampedPopulationIsNotSatisfied(t *testing.T) {
	t.Parallel()
	const members = 4
	result, _ := runCensusInvestigation(t, members, 0, false, true)
	assertPopulationQualified(t, result, members)
}

// TestACountOverAnUpstreamTruncatedCensusIsNotSatisfied is the OTHER arm, and
// it is the reason the condition is a disjunction rather than a check on
// Truncated.
//
// falkorgraph's reader sets `cohort.Complete = false` on an upstream
// exact-name truncation and does NOT set Truncated: a truncated census with
// fewer than MaxCohortMembers matching members would otherwise report
// Complete=true despite genuinely missing some. A predicate keyed on
// Truncated alone reads that cohort as a full census and is silent.
//
// This is the only test that kills a mutant deleting `!cohort.Complete` from
// the disjunct.
func TestACountOverAnUpstreamTruncatedCensusIsNotSatisfied(t *testing.T) {
	t.Parallel()
	const members = 4
	result, _ := runCensusInvestigation(t, members, 0, false, false)
	assertPopulationQualified(t, result, members)
}

// TestACountOverACompleteCensusStaysSatisfied is the POSITIVE CONTROL, and it
// is not optional.
//
// A change that qualified every count would pass both tests above and be
// completely wrong. This one is what makes them mean something: a full census
// must still read `satisfied`, must still name NO cause of any kind, and must
// still leave the answer `complete`.
func TestACountOverACompleteCensusStaysSatisfied(t *testing.T) {
	t.Parallel()
	const members = 4
	result, _ := runCensusInvestigation(t, members, 0, true, false)
	row := theCountRow(t, result)

	if row.Outcome != contractsv1.ContextFabricRequirementSatisfied {
		t.Errorf("count row outcome = %q over a COMPLETE census, want %q", row.Outcome, contractsv1.ContextFabricRequirementSatisfied)
	}
	if row.Impact != contractsv1.ContextFabricAnswerImpactNone {
		t.Errorf("count row impact = %q over a complete census, want %q", row.Impact, contractsv1.ContextFabricAnswerImpactNone)
	}
	// Every cause field, not just the coverage one. A row that lost nothing
	// must name no cause at all, and asserting only the field this change
	// writes would let a mutant set one of the other two unnoticed.
	if row.CauseCoverage != "" || row.CauseNarrowing != "" || row.CauseOverrun != "" {
		t.Errorf("an exact count over a complete census names a cause: coverage=%q narrowing=%q overrun=%q",
			row.CauseCoverage, row.CauseNarrowing, row.CauseOverrun)
	}
	if row.CauseObserved {
		t.Error("an exact count claims an observed cause while naming none")
	}
	if got := result.Completeness.State; got != contractsv1.ContextFabricAnswerCompletenessComplete {
		t.Errorf("the served answer states completeness %q over a complete census, want %q", got, contractsv1.ContextFabricAnswerCompletenessComplete)
	}
}

// TestACountNarrowedByMembersKeepsItsRecordedCause pins the arm that must NOT
// change.
//
// Where a member-narrowing step DID run, the row already says `narrowed` and
// already names the mechanism the plan recorded. The population qualification
// must not reach it: replacing a recorded basis with a coverage code would
// lose the mechanism that actually cut the set, and would do it on the one
// arm where the engine knows exactly what happened.
//
// The cohort here is ALSO incomplete, which is the point -- this is the
// overlap case, and the recorded narrowing wins it.
func TestACountNarrowedByMembersKeepsItsRecordedCause(t *testing.T) {
	t.Parallel()
	const discovered = 8
	const ceiling = 3
	result, _ := runCensusInvestigation(t, discovered, ceiling, false, true)
	row := theCountRow(t, result)

	if row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Fatalf("count row outcome = %q over a narrowed member set, want %q", row.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if row.Served >= row.Declared {
		t.Fatalf("count row served/declared = %d/%d: a member narrowing ran, so this row must be a real reduction",
			row.Served, row.Declared)
	}
	if row.CauseNarrowing == "" && row.CauseOverrun == "" {
		t.Error("a count narrowed by a recorded member step names neither a basis nor a ceiling -- the population " +
			"qualification has overwritten the mechanism that actually cut the set")
	}
	if row.CauseCoverage == populationTruncatedToken {
		t.Error("a count narrowed by a recorded member step names the population qualification as its cause; " +
			"the recorded mechanism is what cut it, and naming the census instead loses that")
	}
	// The reduction chain must still reconcile. A refinement is derived from
	// the row's own counts, so a change that disturbed either number would
	// break the chain here rather than at the wire.
	if len(row.Refinements) != 1 {
		t.Fatalf("count row records %d refinements over a real reduction, want 1", len(row.Refinements))
	}
	if got := row.Refinements[0]; got.Before != row.Declared || got.After != row.Served {
		t.Errorf("refinement chain %d->%d does not reconcile with the row's %d/%d", got.Before, got.After, row.Served, row.Declared)
	}
}

// TestEveryPopulationArmProducesAWireValidRow runs the arms through the
// contract validator the wire boundary uses.
//
// It is separate from the per-arm assertions on purpose: those say what the
// row MEANS, this says the row is REPRESENTABLE. Before the validator
// exception, arm 3 and arm 5 produced a row the wire refuses -- so a change
// that emitted the honest outcome without the contract work would 500 the
// answer instead of qualifying it, which is worse than the defect.
func TestEveryPopulationArmProducesAWireValidRow(t *testing.T) {
	t.Parallel()
	arms := []struct {
		name       string
		size       int
		maxMembers int
		complete   bool
		truncated  bool
	}{
		{"complete census", 4, 0, true, false},
		{"clamped population", 4, 0, false, true},
		{"upstream truncated census", 4, 0, false, false},
		{"narrowed by members over an incomplete population", 8, 3, false, true},
	}
	if len(arms) == 0 {
		t.Fatal("no arms enumerated; this test would pass while proving nothing")
	}
	reached := 0
	for _, arm := range arms {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			result, _ := runCensusInvestigation(t, arm.size, arm.maxMembers, arm.complete, arm.truncated)
			row := theCountRow(t, result)
			if err := contractsv1.ValidateContextFabricPlanRequirementOutcomeRow(row); err != nil {
				t.Fatalf("the count row this engine served for %q does not validate: %v", arm.name, err)
			}
		})
		reached++
	}
	if reached != len(arms) {
		t.Fatalf("ran %d of %d arms", reached, len(arms))
	}
}

// TestACountWithNoResolvedPopulationIsUnchanged is the regression guard on the
// absence arm.
//
// A question whose population could not be resolved at all already has its
// own row -- `unavailable`, impact `dimension` -- and it must not be recast as
// a truncated population. "We found none of it" and "we found some of it" are
// different answers, and a cohort that does not exist has no coverage flags to
// consult.
func TestACountWithNoResolvedPopulationIsUnchanged(t *testing.T) {
	t.Parallel()
	seed := SeedRequirementOutcomes(registryDeriver{}.DeriveRequirements(*countingFrame(SubjectTeam)))
	if len(seed) == 0 {
		t.Fatal("the counting frame derived no requirements; this fixture proves nothing")
	}
	rows, _, counted := appendMembershipCardinality(seed, nil, nil)
	if counted {
		t.Fatal("a nil cohort reported a counted cardinality")
	}
	var found []RequirementOutcomeRow
	for _, row := range rows {
		if row.Obligation == string(ObligationCount) && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		t.Fatalf("assembled-result `count` rows for an absent population = %d, want exactly 1", len(found))
	}
	row := found[0]
	if row.Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Errorf("count row outcome = %q with NO resolved population, want %q -- nothing was counted, so nothing "+
			"was counted over a truncated population either", row.Outcome, contractsv1.ContextFabricRequirementUnavailable)
	}
	if row.Impact != contractsv1.ContextFabricAnswerImpactDimension {
		t.Errorf("count row impact = %q with no resolved population, want %q", row.Impact, contractsv1.ContextFabricAnswerImpactDimension)
	}
	if row.CauseCoverage == populationTruncatedToken {
		t.Error("a count with no population at all names the population as TRUNCATED; nothing was resolved, so " +
			"there is no partial census to describe")
	}
}

// TestTheQualifiedCountReachesTheOperator is the telemetry half, in the same
// change as the branch it describes.
//
// A qualified count that no run artifact records is a decision an operator
// cannot diagnose. The event is read off the SERVED row rather than
// recomputed, so this also pins that the line and the answer cannot state
// different outcomes -- and it asserts the two cohort flags ride the same
// line, which is what makes the number readable as a lower bound.
func TestTheQualifiedCountReachesTheOperator(t *testing.T) {
	t.Parallel()
	const members = 4
	result, telemetry := runCensusInvestigation(t, members, 0, false, true)
	row := theCountRow(t, result)

	if len(telemetry.membershipCardinalities) != 1 {
		t.Fatalf("membership cardinality events = %d, want exactly 1", len(telemetry.membershipCardinalities))
	}
	event := telemetry.membershipCardinalities[0]
	if event.Outcome != row.Outcome {
		t.Errorf("telemetry outcome = %q while the served row says %q -- the run's own artifacts hold two answers",
			event.Outcome, row.Outcome)
	}
	if event.Outcome != contractsv1.ContextFabricRequirementNarrowed {
		t.Errorf("telemetry outcome = %q for a count over an incomplete population, want %q",
			event.Outcome, contractsv1.ContextFabricRequirementNarrowed)
	}
	if !event.CohortTruncated {
		t.Error("the telemetry line reports cohort_truncated=false for a truncated cohort; without it the number " +
			"reads as a claim about the population that the step does not make")
	}
	if event.CohortComplete {
		t.Error("the telemetry line reports cohort_complete=true for an incomplete cohort")
	}
	if event.Served != row.Served || event.Declared != row.Declared {
		t.Errorf("telemetry served/declared = %d/%d while the served row says %d/%d",
			event.Served, event.Declared, row.Served, row.Declared)
	}
}
