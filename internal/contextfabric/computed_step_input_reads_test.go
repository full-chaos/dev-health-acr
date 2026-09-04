package contextfabric_test

// THE CONSUMER OF A COMPUTED STEP'S DECLARED INPUTS.
//
// The §13.2.3 amendment gave a computed requirement row a place to say what
// its server step CONSUMES, and stated in the same breath that declaring an
// input is not planning a read. Nothing read the declaration, so the
// six-authority parity proof recorded the gap as a blocking cell:
// `operational_deficiencies` is a declared `rank_cohort` input that no derived
// read row served, and retiring the authority that injects it would have
// dropped a real read.
//
// These tests pin the consumer. The parity proof's own assertion
// (TestEachAuthorityBlockingLossCountIsPinned) says the blocking cell is
// gone; it cannot say WHY, because it never calls the plan. That is this
// file's job, and the two together are what make the parity claim non
// circular: the proof counts a declared input as planned only because
// planFactKinds is measured here actually planning it.

import (
	"testing"

	contextfabric "github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// traceFrameByID finds one corpus frame, failing closed.
//
// A frame id that has been renamed away would otherwise leave the caller
// driving a ZERO QuestionFrame -- which derives no coordinates, no rows and
// no inputs, and every assertion below would pass over nothing.
func traceFrameByID(t *testing.T, id string) contextfabric.QuestionFrame {
	t.Helper()
	for _, testCase := range traceFrames() {
		if testCase.id == id {
			return testCase.frame
		}
	}
	t.Fatalf("corpus frame %q is gone; this test drove nothing", id)
	return contextfabric.QuestionFrame{}
}

// TestPlanFactKindsPlansEveryDeclaredComputedStepInput drives the PRODUCTION
// plan stage and reads what it planned.
//
// THE FAMILY IS DELIBERATELY NOT A COHORT ONE, and that is the whole design
// of this test. planFactKinds has three sources: the model's widening, the
// family's unconditional cohort-ranking injection, and -- new -- the declared
// inputs of the turn's computed rows. On a cohort family the second and third
// name the SAME five kinds (rank_cohort's declared inputs are
// cohortRankingFormulaKinds itself), so a test driven there would pass with
// the third source deleted. Driving a single-subject family switches the
// cohort injection off, proven rather than assumed by asking the production
// predicate, so the only remaining explanation for the kinds appearing is the
// one under test.
func TestPlanFactKindsPlansEveryDeclaredComputedStepInput(t *testing.T) {
	capabilities := liveCapabilityList(t)
	seed := contextfabric.GenerateObligationSeed(capabilities)
	frame := traceFrameByID(t, "C4")
	rows := contextfabric.DeriveRequirements(frame, seed, capabilities)

	planned := contextfabric.ComputedStepInputReads(rows)
	if len(planned) == 0 {
		t.Fatal("the corpus frame declares no computed-step input at all, so this test asserted nothing -- either the frame stopped deriving a computed obligation or the step's declaration was emptied")
	}

	// The kinds that can ONLY come from the input declaration. A kind a read
	// row already serves would appear in the plan whatever this change did,
	// so asserting on it would be a pin that survives its own deletion. This
	// is `operational_deficiencies` today and it is DERIVED rather than
	// named, so the test keeps working when the registry moves.
	servedByARead := map[contextfabric.FactKind]bool{}
	for _, row := range rows {
		for _, kind := range row.FactKinds {
			servedByARead[kind] = true
		}
	}
	var onlyFromInputs []contextfabric.FactKind
	for _, kind := range planned {
		if !servedByARead[kind] {
			onlyFromInputs = append(onlyFromInputs, kind)
		}
	}
	if len(onlyFromInputs) == 0 {
		t.Fatal("every declared input is already served by a read row on this frame, so nothing here discriminates the input consumer from the read rows -- the corpus moved and this test needs a frame that still shows the gap")
	}

	// The family whose axis is NOT a cohort, proven by the production
	// predicate rather than by reading the registry table here.
	family := contextfabric.QuestionFamilySubjectInvestigation
	definition, found := contextfabric.LookupQuestionFamily(family)
	if !found {
		t.Fatalf("family %q is not in the registry", family)
	}
	if contextfabric.IsCohortSubjectAxisForTest(definition.SubjectAxis) {
		t.Fatalf("family %q has a COHORT subject axis (%q), so the unconditional ranking injection would supply these kinds and this test could not attribute them", family, definition.SubjectAxis)
	}

	input := contextfabric.PlanAnswerInput{
		Family:           contextfabric.QuestionFamilyOutcome{Family: family, Source: contextfabric.QuestionFamilySourceModel},
		Budget:           contextfabric.ResponseBudget{MaxItems: 30, MaxSerializedBytes: 1 << 20},
		MaxCohortMembers: 50,
	}

	// THE CONTROL RUNS FIRST, so a plan that named these kinds for some third
	// reason is caught before the positive case can be read as evidence.
	withoutRows := contextfabric.PlanAnswer(input)
	for _, kind := range onlyFromInputs {
		if containsFactKind(withoutRows.FactKinds, kind) {
			t.Fatalf("control plan (no requirement rows) already names %q, so this family is not the isolated setting this test claims", kind)
		}
	}

	input.Requirements = rows
	withRows := contextfabric.PlanAnswer(input)
	for _, kind := range planned {
		if !containsFactKind(withRows.FactKinds, kind) {
			t.Errorf("plan.FactKinds does not name %q, which a served, server-executed computed row on this frame declares its step consumes -- a declared input nothing plans is the blocking cell this change exists to close", kind)
		}
	}

	// EVERY kind of the control's plan survives. The new source is a
	// WIDENING, and a union that dropped an earlier source while adding its
	// own would satisfy every assertion above.
	for _, kind := range withoutRows.FactKinds {
		if !containsFactKind(withRows.FactKinds, kind) {
			t.Errorf("plan.FactKinds LOST %q once requirement rows were supplied; the row source may only widen", kind)
		}
	}
	t.Logf("family %s: control plans %d kinds, with rows %d, attributed only-from-inputs %v", family, len(withoutRows.FactKinds), len(withRows.FactKinds), onlyFromInputs)
}

// TestComputedStepInputReadsHonoursBothGuards drives each guard on its own.
//
// Two conditions decide whether a declared input becomes a planned read --
// the row is SERVED, and the step is SERVER-EXECUTED -- and neither follows
// from the other. A mutation dropping either one is invisible to a fixture
// that fails both, so every case below differs from the planned case in
// exactly ONE field.
func TestComputedStepInputReadsHonoursBothGuards(t *testing.T) {
	deficiencies := contextfabric.FactOperationalDeficiencies
	health := contextfabric.FactHealth

	served := contextfabric.DerivedRequirement{
		Kind:           contextfabric.ObligationKindComputed,
		Step:           contextfabric.ComputedStepRankCohort,
		InputClass:     contextfabric.ComputedInputFactKinds,
		InputFactKinds: []contextfabric.FactKind{deficiencies},
		StepExecution:  contextfabric.ComputedStepServerExecuted,
	}

	declaredOnly := served
	declaredOnly.StepExecution = contextfabric.ComputedStepDeclaredOnly

	// An unavailable row names NO step -- that is the derivation's own
	// invariant, not a convenience here: "a row that named both a step and a
	// reason it cannot run would be two answers to what became of the cell".
	unavailable := served
	unavailable.Step = ""
	unavailable.StepExecution = ""
	unavailable.Unavailable = contextfabric.RequirementReasonComputedPopulationAbsent

	readRow := contextfabric.DerivedRequirement{
		Kind:      contextfabric.ObligationKindRead,
		FactKinds: []contextfabric.FactKind{health},
	}

	cases := []struct {
		name string
		rows []contextfabric.DerivedRequirement
		want []contextfabric.FactKind
	}{
		{
			name: "served and server-executed: the declared input is planned",
			rows: []contextfabric.DerivedRequirement{readRow, served},
			want: []contextfabric.FactKind{deficiencies},
		},
		{
			name: "the SAME row declared_only: nothing runs the step, so nothing plans its input",
			rows: []contextfabric.DerivedRequirement{readRow, declaredOnly},
			want: nil,
		},
		{
			name: "the SAME row unavailable: the computation cannot happen, so its inputs are not fetched",
			rows: []contextfabric.DerivedRequirement{readRow, unavailable},
			want: nil,
		},
		{
			name: "a read row's own serving kinds are NOT input reads",
			rows: []contextfabric.DerivedRequirement{readRow},
			want: nil,
		},
		{
			name: "no rows at all",
			rows: nil,
			want: nil,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := contextfabric.ComputedStepInputReads(testCase.rows)
			if len(got) != len(testCase.want) {
				t.Fatalf("ComputedStepInputReads = %v, want %v", got, testCase.want)
			}
			for index, kind := range testCase.want {
				if got[index] != kind {
					t.Fatalf("ComputedStepInputReads = %v, want %v", got, testCase.want)
				}
			}
			if len(testCase.want) == 0 && got != nil {
				t.Fatalf("ComputedStepInputReads returned an EMPTY slice, want nil -- the derivation's kind lists are nil when empty and a projected row must equal itself across a store round trip")
			}
		})
	}

	// DEDUPLICATION AND ORDER, driven on rows that both declare the same
	// kinds in different orders. Two runs of one turn must produce the same
	// plan bytes, and a caller that saw a repeat would read one declaration
	// as two independent needs.
	first := served
	first.InputFactKinds = []contextfabric.FactKind{deficiencies, health}
	secondRow := served
	secondRow.InputFactKinds = []contextfabric.FactKind{health, deficiencies}
	got := contextfabric.ComputedStepInputReads([]contextfabric.DerivedRequirement{first, secondRow})
	if len(got) != 2 {
		t.Fatalf("two rows declaring the same two kinds planned %v, want exactly two", got)
	}
	// The two candidate orders must actually DIFFER, or an order assertion
	// over them proves nothing.
	if first.InputFactKinds[0] == secondRow.InputFactKinds[0] {
		t.Fatal("the two fixtures declare their kinds in the same order, so this cannot detect an order-preserving union")
	}
	again := contextfabric.ComputedStepInputReads([]contextfabric.DerivedRequirement{secondRow, first})
	for index := range got {
		if got[index] != again[index] {
			t.Fatalf("ComputedStepInputReads is order-dependent: %v vs %v", got, again)
		}
	}
}

func containsFactKind(kinds []contextfabric.FactKind, want contextfabric.FactKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
