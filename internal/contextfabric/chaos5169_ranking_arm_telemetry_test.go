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

// TestTheArmCountersRefuseANonComputedRow makes the computed-kind conjunct
// DECIDABLE, and it exists because an adversarial round predicted it would
// survive — and it did.
//
// The arm counters gate on `row.Kind == ObligationKindComputed` as well as on
// the reason token. Removing that conjunct changed nothing any fixture could
// see: `classifyUnavailable` never returns `computed_population_absent` for a
// READ row today, so no frame in the corpus can produce the shape the conjunct
// exists to refuse, and the mutant `if row.Unavailable == ...` survived a
// thirteen-test suite.
//
// A conjunct no fixture can isolate is not kept with a comment — it is pinned
// where it IS decidable. `RequirementDerivationSummaryFrom` is a PURE function,
// total over its input, so a constructed row is legitimate input to it rather
// than a test building the decision it asserts on: the unit under test is the
// FOLD, and the row is its argument. That is the one place this property can be
// stated at all.
//
// What it protects: the day a read-side classifier gains this token — or a row
// is built wrong — those cells must not be counted in COMPUTED-only telemetry,
// silently inflating an arm an operator reads as a computed-step decision.
func TestTheArmCountersRefuseANonComputedRow(t *testing.T) {
	t.Parallel()

	// A READ row carrying the computed reason. The derivation does not build
	// this today; the conjunct is what keeps it out if anything ever does.
	readRow := DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationState, Role: SubjectRoleSubject, Subject: SubjectTeam,
		},
		Kind:        ObligationKindRead,
		Scope:       CompletionScopeSingleSubject,
		Quantifier:  CompletionQuantifierNone,
		Unavailable: RequirementReasonComputedPopulationAbsent,
	}
	summary := RequirementDerivationSummaryFrom([]DerivedRequirement{readRow})

	// REACH: the row must have landed in the aggregate bucket, or this test
	// asserts zeroes over a row the fold never saw.
	index, ok := unavailableReasonIndex(RequirementReasonComputedPopulationAbsent)
	if !ok {
		t.Fatal("computed_population_absent is not in the reason vocabulary")
	}
	if summary.UnavailableCells[index] != 1 {
		t.Fatalf("the aggregate bucket counted %d, want 1 -- the fold never saw this row, so the assertions below are vacuous", summary.UnavailableCells[index])
	}

	if summary.ComputedPopulationAbsentUnresolvableMemberSet != 0 {
		t.Errorf("a READ row was counted in the `unresolvable_member_set` arm (%d) -- the arms are COMPUTED-only telemetry",
			summary.ComputedPopulationAbsentUnresolvableMemberSet)
	}
	if summary.ComputedPopulationAbsentNotAPopulation != 0 {
		t.Errorf("a READ row was counted in the `not_a_population` arm (%d) -- the arms are COMPUTED-only telemetry",
			summary.ComputedPopulationAbsentNotAPopulation)
	}
	var unplanned int
	for _, count := range summary.ComputedInputKindsUnplanned {
		unplanned += count
	}
	if unplanned != 0 {
		t.Errorf("a READ row contributed %d unplanned computed-step input kinds -- a read obligation has no computed step and no declared step inputs", unplanned)
	}
	// THE RESIDUAL MUST ACCOUNT FOR IT. An adversarial round found that this
	// very fixture left the aggregate bucket at one with both arms at zero --
	// an emitted line claiming a refusal no arm explains. The split is now a
	// TOTAL partition, and this is the row that would otherwise fall out of it.
	if summary.ComputedPopulationAbsentNonComputedRow != 1 {
		t.Errorf("the non-computed residual counted %d, want 1 -- the read row is in the aggregate bucket, so the split must account for it",
			summary.ComputedPopulationAbsentNonComputedRow)
	}
	if got := summary.ComputedPopulationAbsentNotAPopulation +
		summary.ComputedPopulationAbsentUnresolvableMemberSet +
		summary.ComputedPopulationAbsentNonComputedRow; got != summary.UnavailableCells[index] {
		t.Errorf("the three-way split sums to %d but the aggregate bucket is %d -- the partition is not total", got, summary.UnavailableCells[index])
	}

	// The COMPLEMENT, in the same run: an equivalent COMPUTED row must be
	// counted. Without it this test passes on a fold that counts nothing.
	computedRow := DerivedRequirement{
		RequirementCoordinate: RequirementCoordinate{
			Obligation: ObligationRanking, Role: SubjectRoleSubject, Subject: SubjectTeam,
		},
		Kind:        ObligationKindComputed,
		Scope:       CompletionScopeSingleSubject,
		Quantifier:  CompletionQuantifierNone,
		Unavailable: RequirementReasonComputedPopulationAbsent,
	}
	both := RequirementDerivationSummaryFrom([]DerivedRequirement{readRow, computedRow})
	if both.ComputedPopulationAbsentUnresolvableMemberSet != 1 {
		t.Errorf("the computed row was counted %d times in the `unresolvable_member_set` arm, want exactly 1 -- the read row must be refused and the computed row must not be",
			both.ComputedPopulationAbsentUnresolvableMemberSet)
	}
	// And the partition stays total with BOTH rows present, which is the case
	// a residual counter is easiest to get wrong on.
	if got := both.ComputedPopulationAbsentNotAPopulation +
		both.ComputedPopulationAbsentUnresolvableMemberSet +
		both.ComputedPopulationAbsentNonComputedRow; got != both.UnavailableCells[index] {
		t.Errorf("with a read row AND a computed row the split sums to %d but the aggregate is %d -- the partition is not total", got, both.UnavailableCells[index])
	}
	var bothUnplanned int
	for _, count := range both.ComputedInputKindsUnplanned {
		bothUnplanned += count
	}
	if bothUnplanned == 0 {
		t.Error("neither row contributed an unplanned input kind, so the zero asserted above cannot discriminate")
	}
}

// TestTheArmCountersCoverEveryRoleThatCanRefuseAPopulation closes the round-2
// mutant `&& row.Role == SubjectRoleSubject`, which survived.
//
// It survived because every earlier fixture here refuses a population at the
// SUBJECT role — a named subject, and the organization as a subject. But the
// derivation refuses populations at two more roles, and both are reachable:
// `organization_scope` WITH a member kind refuses at the MEMBER role, and an
// explicit set refuses at the OPERAND role. A role restriction on the counters
// would silently drop those, and an operator would read zero for a decision
// that fired.
//
// The roles are read off the DERIVED ROWS rather than asserted from a list, so
// this cannot pass by agreeing with a hand-typed expectation that has drifted
// from what the derivation actually produces.
func TestTheArmCountersCoverEveryRoleThatCanRefuseAPopulation(t *testing.T) {
	t.Parallel()

	kind := SubjectTeam
	cases := []struct {
		name     string
		frame    QuestionFrame
		wantRole SubjectRole
	}{
		{
			name:     "organization scope with a member kind refuses at the MEMBER role",
			frame:    frameWith([]InvestigationGoal{GoalRankOrSurvey}, orgExpression(&kind), TemporalIntentCurrent, nil),
			wantRole: SubjectRoleMember,
		},
		{
			name:     "an explicit set refuses at the OPERAND role",
			frame:    *explicitSetRankingFrame(),
			wantRole: SubjectRoleOperand,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := DeriveRequirements(tc.frame, GenerateObligationSeed(nil), nil)

			// REACH, before any count is read: the fixture must actually
			// produce an unavailable ranking row AT THE ROLE this case is
			// about, or the counter assertion below is vacuous.
			found := false
			for _, row := range rows {
				if row.Obligation != ObligationRanking || row.Served() {
					continue
				}
				if row.Role == tc.wantRole && row.Unavailable == RequirementReasonComputedPopulationAbsent {
					found = true
				}
			}
			if !found {
				t.Fatalf("no unavailable `ranking` row at role %q for this frame -- the fixture does not exhibit the case, so the counter assertion would prove nothing", tc.wantRole)
			}

			summary := RequirementDerivationSummaryFrom(rows)
			if summary.ComputedPopulationAbsentUnresolvableMemberSet == 0 {
				t.Errorf("the `unresolvable_member_set` arm counted ZERO for a population refused at role %q -- a role restriction on the counters would read as a decision that never fired", tc.wantRole)
			}
			var unplanned int
			for _, count := range summary.ComputedInputKindsUnplanned {
				unplanned += count
			}
			if unplanned == 0 {
				t.Errorf("no unplanned computed-step input kinds counted at role %q -- the cost of the refusal is invisible on the emitted line", tc.wantRole)
			}
			t.Logf("role %s: arm=%d unplanned_kinds=%d", tc.wantRole, summary.ComputedPopulationAbsentUnresolvableMemberSet, unplanned)
		})
	}
}
