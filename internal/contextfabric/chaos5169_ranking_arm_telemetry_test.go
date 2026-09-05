package contextfabric

// THE DECISION-BASIS TELEMETRY for the two arms of
// `computed_population_absent`, asserted on the EMITTED RECORD.
//
// A SEPARATE FILE from the harm tests beside it, deliberately: this file
// names summary fields that do not exist on the parent commit, so it cannot
// compile there. The harm file compiles at the parent and FAILS at runtime,
// which is what makes its red a statement about behaviour rather than about
// a missing identifier -- and keeping the two apart is what lets the
// red-at-parent proof copy that file VERBATIM off this branch instead of
// hand-trimming a variant of it that nobody ships.

import (
	"context"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

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
			// ProposedKind carries the topology the arms belong to. Taken
			// from the fixture frame, never typed in beside it: a hand-typed
			// discriminator would keep agreeing with the assertion after the
			// fixture moved.
			FrameValidationEvent{
				Outcome:               FrameValidationOutcomeValid,
				ProposedKind:          frame.SubjectExpression.Kind,
				RequirementDerivation: summary,
			},
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

	// WHAT THE DECISION COST. The arm counter says a cell went unavailable;
	// these say which reads the answer therefore did not get. Without them
	// this arm is indistinguishable on the line from any other unavailable
	// cell, which is the diagnosis the ticket itself needed.
	//
	// Asserted per KIND against rank_cohort's own declaration rather than a
	// hand-typed list, so a change to the formula's inputs moves this with
	// it. The zeroes are asserted too: a kind no unplanned step consumes
	// must still carry its key.
	inputs, declared := InputsForComputedStep(ComputedStepRankCohort)
	if !declared || len(inputs.FactKinds) == 0 {
		t.Fatal("rank_cohort declares no inputs, so the cost assertion below quantifies over nothing")
	}
	dropped := map[FactKind]bool{}
	for _, kind := range inputs.FactKinds {
		dropped[kind] = true
	}
	seenNonZero := 0
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		key := "requirement_computed_input_kind_unplanned_" + string(kind)
		value, ok := record[key]
		if !ok {
			t.Fatalf("the emitted record is missing %q -- an omitted zero is indistinguishable from a kind the accounting never reached", key)
		}
		want := float64(0)
		if dropped[FactKind(kind)] {
			want = 1
			seenNonZero++
		}
		if value != want {
			t.Errorf("%s = %v, want %v -- this frame's ranking cell went unavailable, so exactly rank_cohort's %d declared inputs are unplanned",
				key, value, want, len(inputs.FactKinds))
		}
	}
	if seenNonZero != len(inputs.FactKinds) {
		t.Errorf("asserted %d non-zero unplanned kinds, want %d -- the vocabulary sweep did not reach every declared input", seenNonZero, len(inputs.FactKinds))
	}

	// THE FRAME KIND the arms belong to is already on this line, and this
	// asserts it rather than assuming it: without a topology an operator
	// cannot tell which shape produced the unrunnable cell, and a second
	// copy of the discriminator would be a field to keep in sync for no gain.
	if record["proposed_kind"] != string(SubjectExpressionNamed) {
		t.Errorf("proposed_kind = %v, want %q -- the arm counts are not attributable to a topology without it",
			record["proposed_kind"], SubjectExpressionNamed)
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
