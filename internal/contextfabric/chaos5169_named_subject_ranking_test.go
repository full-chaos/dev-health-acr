package contextfabric

// A NAMED-SUBJECT RANKING FRAME MUST NOT DECLARE COMPUTED INPUTS THE FACT
// READER NEVER RECEIVES.
//
// THE DEFECT, and why the two tests that were supposed to cover it did not.
// `ComputedStepInputReads` widens the plan's fact kinds with every SERVED
// computed row's declared inputs, and the derivation served the
// `ranking/subject/<named>` row because a named subject is a population of
// one. But `rank_cohort`'s executor -- `RankCohort` -- is invoked only under
// `if graphContext.Cohort != nil`, and so is the engine's forwarding of
// `plan.FactKinds` into the fact request. A named subject is not a cohort.
// So the plan published five computed inputs, the fact reader received none
// of them, `RankCohort` never ran, and the row's planning seed said
// `satisfied` -- a cell claiming an ordering that nothing computed, over
// facts that nothing read.
//
// computed_step_input_wiring_test.go covers the two halves SEPARATELY and
// neither one can see this. Its plan-side test drives a non-cohort family but
// asserts only that `AnswerPlan.FactKinds` carries the declared inputs -- it
// reads no fact request. Its reader-side test asserts the reader receives
// them, but on a fixture that supplies a cohort, which is the branch that
// works. The property that fails is the JOIN of the two on ONE fixture, which
// is what this file asserts.
//
// EVERY TEST HERE DRIVES THE PUBLIC ENTRY POINT THROUGH THE PRODUCTION
// DERIVATION (`registryDeriver`, which calls `DeriveRequirements`). None
// hand-builds the requirement row it asserts on: a regression test that
// constructs the decision it checks stays green when the production bug
// returns, and that is the vacuity this package has paid for repeatedly.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// rankingFrameOverANamedSubject is the shape the ticket names: one named
// subject, asked to be ranked or surveyed.
//
// Built THROUGH `DeriveFrameObligations` (via `frameWith`), never by
// hand-typing an obligation list, so a change to §13.2.3's goal table moves
// this fixture with it rather than leaving it pinning a set the server no
// longer derives.
func rankingFrameOverANamedSubject() *QuestionFrame {
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		namedExpression(SubjectTeam),
		TemporalIntentCurrent,
		nil,
	)
	return &frame
}

// rankingFrameOverACohort is the SAME goal over a discovered set -- the
// complement fixture, and the control that keeps the fix honest. The
// cohort path must be byte-for-byte unchanged by this ticket.
func rankingFrameOverACohort() *QuestionFrame {
	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		discoveredExpression(SubjectTeam),
		TemporalIntentCurrent,
		nil,
	)
	return &frame
}

// rankingInvestigation is one end-to-end drive, returning the served
// document and everything the FACT READER was actually asked for.
//
// `cohort` nil is the non-cohort fixture. The interpreter's own fact
// requirement is deliberately `metrics`: it is outside
// `cohortRankingFormulaKinds`, so a reader that received a ranking kind can
// only have got it from the plan, never from the model's own widening or
// from the engine's unconditional cohort injection.
func rankingInvestigation(t *testing.T, cohort *Cohort, frame *QuestionFrame, family QuestionFamily, shape InvestigationShape) (InvestigationResult, map[FactKind]bool, int) {
	t.Helper()

	for _, injected := range cohortRankingFormulaKinds {
		if injected == FactMetrics {
			t.Fatalf("the fixture's own fact requirement %q is one of the ranking formula kinds, so this test could not tell the plan from the injection", FactMetrics)
		}
	}

	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:NAMED", Label: "Named"}
	graph := graphReaderStub{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{},
			Committed:  []SubjectRef{subject},
		},
		context: GraphContext{
			Cohort: cohort, Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}

	var observed map[FactKind]bool
	reads := 0
	deriver := &countingRegistryDeriver{}

	engine, err := NewEngine(EngineDependencies{
		Interpreter: familyInterpreter{
			interpreted: InterpretedQuestion{
				Shape: shape, RequestedJudgment: "ranking",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactMetrics}},
			},
			outcome: QuestionFamilyOutcome{
				Frame:            frame,
				FrameObligations: frame.Obligations,
				Family:           family,
				Source:           QuestionFamilySourceModel,
			},
		},
		Graph: graph,
		Facts: factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
			reads++
			observed = make(map[FactKind]bool, len(request.Requirements))
			for _, requirement := range request.Requirements {
				observed[requirement.Kind] = true
			}
			return CanonicalFactBundle{
				Facts:    []CanonicalFact{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version:  "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ranked.",
				CurrentState: "Nominal.", StrongestPressures: []string{}, Drivers: []DriverJudgment{},
				RemainingWork: []Finding{}, ReadinessGaps: []Finding{}, Paths: []RelationshipPath{},
				Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ranked.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &resultStoreStub{},
		Telemetry:    &recordingTelemetry{},
		Requirements: deriver,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(700, 0).UTC() },
		NewResultID:    func() string { return "result_51690001" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_51690001"
	request.Question = "ranking question"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil && !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("Investigate() error = %v", err)
	}

	// REACH, asserted before any assertion about content. A turn that never
	// derived, never read, or never planned would leave every membership
	// check below quantifying over nothing -- which reads as green.
	if deriver.calls == 0 {
		t.Fatal("the engine never called the requirement deriver, so nothing here describes the production derivation")
	}
	if reads == 0 || observed == nil {
		t.Fatal("the fact reader was never called, so this test asserted nothing about what it received")
	}
	if result.AnswerPlan == nil {
		t.Fatal("the served answer carries no answer plan")
	}
	return result, observed, reads
}

// countingRegistryDeriver is registryDeriver with a call counter, so the
// reach guard above can tell "derived nothing" from "was never asked".
type countingRegistryDeriver struct {
	calls int
}

func (d *countingRegistryDeriver) DeriveRequirements(frame QuestionFrame) []DerivedRequirement {
	d.calls++
	return DeriveRequirements(frame, GenerateObligationSeed(nil), nil)
}

// outcomeRowsForObligation returns the served document's outcome rows for one
// obligation at one stage. It reads the SERVED document and nothing else.
func outcomeRowsForObligation(result InvestigationResult, obligation AnswerObligation, stage contractsv1.ContextFabricOutcomeStage) []RequirementOutcomeRow {
	var found []RequirementOutcomeRow
	for _, row := range result.Completeness.Outcomes {
		if row.Obligation == string(obligation) && row.Stage == stage {
			found = append(found, row)
		}
	}
	return found
}

// planRequirementForObligation returns the served plan's requirement row for
// one obligation, and whether it was published at all.
func planRequirementForObligation(result InvestigationResult, obligation AnswerObligation) (contractsv1.ContextFabricPlanRequirement, bool) {
	if result.AnswerPlan == nil {
		return contractsv1.ContextFabricPlanRequirement{}, false
	}
	for _, row := range result.AnswerPlan.Requirements {
		if row.Obligation == string(obligation) {
			return row, true
		}
	}
	return contractsv1.ContextFabricPlanRequirement{}, false
}

// TestNamedSubjectRankingNeverDeclaresInputsTheFactReaderDoesNotReceive is the
// red-first pin, and it is deliberately ONE test over BOTH halves.
//
// Splitting them is exactly what let the defect ship: a plan-side assertion on
// a non-cohort fixture and a reader-side assertion on a cohort fixture are
// both green while the join of the two is false. So this drives ONE non-cohort
// turn and asserts, of that one turn:
//
//	(1) PLAN <-> READER. Every fact kind the served plan publishes reached the
//	    fact reader. This is the invariant the defect breaks directly: at the
//	    parent the plan published rank_cohort's five declared inputs and the
//	    reader received none of them.
//	(2) NO SILENT `satisfied`. The `ranking` requirement's planning row does
//	    not claim the cell was served in full. At the parent it said
//	    `satisfied` on a computation the engine can never invoke.
//
// Either fix shape has to satisfy both: forwarding the kinds would satisfy (1)
// and leave (2) false, because RankCohort still needs the cohort pointer.
func TestNamedSubjectRankingNeverDeclaresInputsTheFactReaderDoesNotReceive(t *testing.T) {
	t.Parallel()

	frame := rankingFrameOverANamedSubject()
	result, observed, _ := rankingInvestigation(t, nil, frame, QuestionFamilySubjectInvestigation, ShapeSingleSubject)

	// THE FIXTURE MUST BE NON-COHORT, asserted rather than assumed. If this
	// frame ever became a cohort variant the engine would resolve a cohort,
	// forward the plan kinds and rank -- and both assertions below would pass
	// while proving nothing about the branch under test.
	if frame.SubjectExpression.IsCohortVariant() {
		t.Fatalf("fixture expression %q is a COHORT variant; this test cannot attribute anything to the non-cohort path", frame.SubjectExpression.Kind)
	}
	definition, found := LookupQuestionFamily(result.AnswerPlan.Family)
	if !found {
		t.Fatalf("the served plan names family %q, which the registry does not carry", result.AnswerPlan.Family)
	}
	if isCohortSubjectAxis(definition.SubjectAxis) {
		t.Fatalf("this fixture resolved to COHORT family %q (axis %q); the unconditional ranking injection would supply these kinds and this test could not attribute them to the requirement rows",
			result.AnswerPlan.Family, definition.SubjectAxis)
	}

	// The frame must actually DERIVE a ranking cell, or (2) quantifies over
	// nothing.
	rankingRow, published := planRequirementForObligation(result, ObligationRanking)
	if !published {
		t.Fatalf("the served plan publishes no `ranking` requirement over %d rows -- this fixture does not reach the cell under test", len(result.AnswerPlan.Requirements))
	}

	// (1) PLAN <-> READER.
	if len(result.AnswerPlan.FactKinds) == 0 {
		t.Fatal("the served plan publishes NO fact kinds, so the plan/reader agreement below quantifies over nothing")
	}
	for _, planned := range result.AnswerPlan.FactKinds {
		if !observed[planned] {
			t.Errorf("the served plan publishes fact kind %q but the fact reader received %v -- the plan declares an input the reader never got, so anything computed from it was computed from nothing (plan kinds: %v)",
				planned, sortedObservedKinds(observed), result.AnswerPlan.FactKinds)
		}
	}

	// (2) NO SILENT `satisfied`.
	rows := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning)
	if len(rows) == 0 {
		t.Fatalf("the served document carries NO planning outcome row for `ranking`, yet the plan publishes the requirement (%q) -- the cell has no account at all", rankingRow.Requirement)
	}
	for _, row := range rows {
		if row.Outcome == contractsv1.ContextFabricRequirementSatisfied {
			t.Errorf("the `ranking` requirement %q reads %q on a frame that resolves no member set: RankCohort is invoked only when a cohort exists, so nothing ordered anything and the cell claims it did (plan row: step=%q unavailable=%q input_kinds=%v)",
				row.Requirement, row.Outcome, rankingRow.Step, rankingRow.Unavailable, rankingRow.InputFactKinds)
		}
	}
	t.Logf("non-cohort ranking: plan kinds %v, reader received %v, plan row step=%q unavailable=%q, outcome rows %d",
		result.AnswerPlan.FactKinds, sortedObservedKinds(observed), rankingRow.Step, rankingRow.Unavailable, len(rows))
}

// TestNamedSubjectRankingCellNamesItsCauseRatherThanVanishing pins the SHAPE
// of the correction, separately from the harm above.
//
// (2) above forbids a lie; it does not require an honest answer in its place.
// A cell that simply disappeared from the account would satisfy it and would
// be the other failure D10 names -- a requirement that neither serves nor says
// why. This asserts the row is present, `unavailable`, carries a coverage
// cause, and states that the derivation OBSERVED that cause rather than
// defaulting it.
func TestNamedSubjectRankingCellNamesItsCauseRatherThanVanishing(t *testing.T) {
	t.Parallel()

	frame := rankingFrameOverANamedSubject()
	result, _, _ := rankingInvestigation(t, nil, frame, QuestionFamilySubjectInvestigation, ShapeSingleSubject)

	rows := outcomeRowsForObligation(result, ObligationRanking, contractsv1.ContextFabricOutcomeStagePlanning)
	if len(rows) != 1 {
		t.Fatalf("the served document carries %d planning `ranking` rows, want exactly 1 -- two rows for one requirement give a reader two answers to one question", len(rows))
	}
	row := rows[0]
	if row.Outcome != contractsv1.ContextFabricRequirementUnavailable {
		t.Errorf("`ranking` outcome = %q, want %q", row.Outcome, contractsv1.ContextFabricRequirementUnavailable)
	}
	if row.Impact == contractsv1.ContextFabricAnswerImpactNone {
		t.Error("`ranking` is unavailable yet declares impact `none` -- an obligation the answer cannot discharge is not lossless")
	}
	if row.CauseCoverage == "" {
		t.Error("`ranking` is unavailable and names NO coverage cause -- a cell that neither serves nor says why is the account this layer exists to prevent")
	}
	if !row.CauseObserved {
		t.Error("`ranking`'s cause reads DEFAULTED; the derivation reported this reason for this cell, so claiming otherwise understates what is known")
	}

	// The PLAN row must agree with the outcome row: the derivation's own
	// token, and no step. A row naming both a step and a reason it cannot run
	// gives two answers to what became of the cell.
	planRow, published := planRequirementForObligation(result, ObligationRanking)
	if !published {
		t.Fatal("the served plan publishes no `ranking` requirement")
	}
	if planRow.Unavailable != string(RequirementReasonComputedPopulationAbsent) {
		t.Errorf("plan `ranking` unavailable = %q, want %q", planRow.Unavailable, RequirementReasonComputedPopulationAbsent)
	}
	if planRow.Step != "" {
		t.Errorf("plan `ranking` names step %q while also naming an unavailable reason -- exactly one of Step and Unavailable is meaningful", planRow.Step)
	}
	if len(planRow.InputFactKinds) != 0 {
		t.Errorf("plan `ranking` declares computed inputs %v on a cell whose step cannot run -- those are the reads nobody needs", planRow.InputFactKinds)
	}
}

// TestCohortRankingStillPlansAndReadsItsDeclaredInputs is the COMPLEMENT, and
// it is the control that stops the fix from being a deletion.
//
// A change that made every ranking cell unavailable would turn both tests
// above green and remove a real capability. On a cohort fixture -- the branch
// where RankCohort genuinely runs -- the row must stay SERVED, its declared
// inputs must reach the plan, and the plan must reach the reader. GREEN at the
// parent as well as at the tip: this behaviour is not what the ticket changes.
func TestCohortRankingStillPlansAndReadsItsDeclaredInputs(t *testing.T) {
	t.Parallel()

	frame := rankingFrameOverACohort()
	if !frame.SubjectExpression.IsCohortVariant() {
		t.Fatalf("the complement fixture's expression %q is NOT a cohort variant, so it controls nothing", frame.SubjectExpression.Kind)
	}
	cohort := &Cohort{
		Kind:      SubjectTeam,
		Rationale: "fixture",
		Members: []CohortMember{{
			Subject:          SubjectRef{Kind: SubjectTeam, CanonicalID: "team:COHORT", Label: "Cohort"},
			Rank:             1,
			InclusionReasons: []string{"matched"},
		}},
		Complete: true,
	}
	result, observed, _ := rankingInvestigation(t, cohort, frame, QuestionFamilyDiscoveredCohortRanking, ShapeDiscoveredCohort)

	planRow, published := planRequirementForObligation(result, ObligationRanking)
	if !published {
		t.Fatal("the cohort fixture publishes no `ranking` requirement, so this control asserts nothing")
	}
	if planRow.Unavailable != "" {
		t.Fatalf("the cohort fixture's `ranking` row reads unavailable %q -- the fix removed a capability instead of correcting a claim", planRow.Unavailable)
	}
	if planRow.Step != string(ComputedStepRankCohort) {
		t.Errorf("the cohort fixture's `ranking` row names step %q, want %q", planRow.Step, ComputedStepRankCohort)
	}
	if len(planRow.InputFactKinds) == 0 {
		t.Fatal("the cohort fixture's `ranking` row declares no computed inputs, so the plan/reader assertion below quantifies over nothing")
	}
	for _, declared := range planRow.InputFactKinds {
		if !observed[declared] {
			t.Errorf("the cohort fixture's plan declares computed input %q, which the fact reader (%v) never received", declared, sortedObservedKinds(observed))
		}
	}
	// The model's own kind must survive alongside them: a forwarding that
	// REPLACED the requirement set rather than widening it would satisfy the
	// loop above.
	if !observed[FactMetrics] {
		t.Errorf("the fact reader received %v, missing the model's own %q -- the plan may only WIDEN what is read", sortedObservedKinds(observed), FactMetrics)
	}
}

// TestTheUnrunnableRankingArmIsNAMEDOnTheEmittedLine is the decision-basis
// telemetry pin, and it reads the EMITTED RECORD rather than the summary
// struct.
//
// WHY A DEDICATED KEY AND NOT THE AGGREGATE ONE. Both arms of
// `computed_population_absent` report "the step had nothing to run over", and
// they send an operator to opposite ends of the pipeline: `not_a_population`
// means the INTERPRETER emitted a coordinate that cannot be served (rank the
// organization; rank a grouping axis), while `unresolvable_member_set` means
// the coordinate was legitimate and the FRAME resolves no member set for the
// step's executor. Reading only the aggregate key, an operator investigating
// this ticket's own defect would have been looking at the wrong layer.
//
// The zeroes are asserted as hard as the count, on this event's standing
// rule: an omitted zero is indistinguishable from an arm the classifier never
// reached.
func TestTheUnrunnableRankingArmIsNAMEDOnTheEmittedLine(t *testing.T) {
	t.Parallel()

	// Rows from the PRODUCTION derivation over the ticket's own frame, never
	// hand-built: a counter fed by a constructed row proves the counter, not
	// the branch.
	frame := rankingFrameOverANamedSubject()
	rows := DeriveRequirements(*frame, GenerateObligationSeed(nil), nil)
	summary := RequirementDerivationSummaryFrom(rows)

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFrameValidation(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			FrameValidationEvent{Outcome: FrameValidationOutcomeValid, RequirementDerivation: summary},
		)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]

	const armKey = "requirement_computed_population_absent_unresolvable_member_set"
	const otherArmKey = "requirement_computed_population_absent_not_a_population"
	const aggregateKey = "requirement_unavailable_computed_population_absent"

	for _, key := range []string{armKey, otherArmKey, aggregateKey} {
		if _, ok := record[key]; !ok {
			t.Fatalf("the emitted record is missing %q -- an absent key cannot be told apart from an arm that never ran", key)
		}
	}
	if record[armKey] != float64(1) {
		t.Errorf("%s = %v, want 1: this frame's `ranking` cell names a step whose executor needs a member set the frame resolves none of, and that decision must be readable from the run's own artifacts",
			armKey, record[armKey])
	}
	if record[otherArmKey] != float64(0) {
		t.Errorf("%s = %v, want an OBSERVED zero -- this frame's coordinate DOES name a population (a named subject is a population of one), so the other arm must not have fired",
			otherArmKey, record[otherArmKey])
	}
	// The split must ACCOUNT for the aggregate bucket, checkable without
	// leaving the line. A drift here would mean one arm is silently eating
	// rows the other should own.
	sum, _ := record[armKey].(float64)
	other, _ := record[otherArmKey].(float64)
	if aggregate, ok := record[aggregateKey].(float64); !ok || sum+other != aggregate {
		t.Errorf("%s = %v, but the two arms sum to %v -- the split does not account for its own bucket", aggregateKey, record[aggregateKey], sum+other)
	}
}

// TestTheNotAPopulationArmStillFires is the positive fixture for the OTHER
// arm, and it is not optional: an arm with no positive fixture can be dead
// for its whole life and read as a healthy zero. The organization as a
// single subject is not a population, so `ranking` over an
// organization-scope frame with no member kind lands there.
func TestTheNotAPopulationArmStillFires(t *testing.T) {
	t.Parallel()

	frame := frameWith(
		[]InvestigationGoal{GoalRankOrSurvey},
		orgExpression(nil),
		TemporalIntentCurrent,
		nil,
	)
	summary := RequirementDerivationSummaryFrom(DeriveRequirements(frame, GenerateObligationSeed(nil), nil))
	if summary.ComputedPopulationAbsentNotAPopulation == 0 {
		t.Fatalf("the `not_a_population` arm counted zero on a frame asking to rank the ORGANIZATION itself -- the arm is unreachable and its zero says nothing (rows: %d, unserved: %d)",
			summary.Derived, summary.Unserved)
	}
	if summary.ComputedPopulationAbsentUnresolvableMemberSet != 0 {
		t.Errorf("the `unresolvable_member_set` arm counted %d on a coordinate that names no population -- the two arms must partition, not overlap",
			summary.ComputedPopulationAbsentUnresolvableMemberSet)
	}
}
