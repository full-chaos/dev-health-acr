package pginvestigation_test

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The fixtures in this package must be valid WITHOUT a database.
//
// Every other test here needs a live Postgres, so until now the only thing
// that could tell you a fixture had gone invalid was CI's container job --
// and it told you by failing thirty tests at once with an error about a
// contract field, which reads like a code regression rather than a fixture
// gap. `Store.Save` calls `ValidateResult` before it persists anything, so
// fixture validity is a property of the fixture alone; it does not need the
// database it was only ever checked behind.
//
// This is the guard for a real incident. The completeness block's `state` is
// a REQUIRED closed vocabulary whose Go ZERO VALUE is not a member, so a
// hand-built block that simply omits it is invalid in a way no reader can
// see, and both fixture builders here were hand-built. Thirty container-job
// failures, all reading `completeness state "" is not a vocabulary member`.
// Both builders now call the producer, and this test fails on the Mac the
// moment either drifts again.
func TestEveryStoredFixtureIsValidWithoutADatabase(t *testing.T) {
	t.Parallel()
	fixtures := map[string]contextfabric.InvestigationResult{
		"validResult":    validResult("result_fixture_guard"),
		"reusableResult": reusableResult("result_fixture_guard_reuse", "org_fixture_guard", "why is throughput down?"),
	}
	// Every result the shared parity table expects to save SUCCESSFULLY.
	//
	// Steps with WantErr are excluded on purpose: one parity case exists
	// precisely to prove the store rejects a malformed result, so its
	// fixture is invalid BY DESIGN. That case is not skipped silently -- the
	// control below asserts it is still there and still invalid, so the
	// exclusion cannot quietly grow to cover a fixture that broke by
	// accident.
	deliberatelyInvalid := 0
	for _, testCase := range paritytest.Cases() {
		for index, step := range testCase.Save {
			if step.WantErr {
				if err := contextfabric.ValidateResult(step.Result); err != nil {
					deliberatelyInvalid++
				}
				continue
			}
			fixtures[fmt.Sprintf("paritytest/%s/save[%d]", testCase.Name, index)] = step.Result
		}
	}
	if deliberatelyInvalid != 1 {
		t.Fatalf("%d parity save steps carry a deliberately invalid result, want exactly 1; if that case was removed this test's exclusion now hides real breakage", deliberatelyInvalid)
	}
	fixtures["paritytest.ValidResult"] = paritytest.ValidResult("result_parity_guard", "what shipped this week?")
	if len(fixtures) < 3 {
		t.Fatalf("only %d fixtures under guard; the parity table produced nothing and this test would pass vacuously", len(fixtures))
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The SAME call Store.Save makes, so a fixture that passes here
			// cannot fail there.
			if err := contextfabric.ValidateResult(fixture); err != nil {
				t.Fatalf("fixture does not satisfy the contract Store.Save enforces: %v", err)
			}
			// And the specific trap: a required closed vocabulary whose zero
			// value is not a member.
			if !contractsv1.ValidContextFabricAnswerCompletenessState(fixture.Completeness.State) {
				t.Fatalf("Completeness.State = %q is not a vocabulary member; build the block from ComputeAnswerCompleteness rather than by hand", fixture.Completeness.State)
			}
		})
	}
}

// The producer must never emit a state outside its own vocabulary, on any
// result -- including a zero-valued one, which is exactly the shape a
// fixture builder starts from.
func TestTheCompletenessProducerAlwaysStampsAVocabularyMember(t *testing.T) {
	t.Parallel()
	for name, result := range map[string]contextfabric.InvestigationResult{
		"the zero result":     {},
		"a complete result":   validResult("result_producer_guard"),
		"a result with a row": resultCarryingOneOutcomeRow(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			block := contextfabric.ComputeAnswerCompleteness(result)
			if !contractsv1.ValidContextFabricAnswerCompletenessState(block.State) {
				t.Fatalf("the producer stamped %q, which is not a vocabulary member", block.State)
			}
		})
	}
}

// resultCarryingOneOutcomeRow is the non-vacuous half: a result whose
// outcome set is NOT empty, so the producer's derivation has to do real work
// rather than returning the empty-set answer three times.
func resultCarryingOneOutcomeRow() contextfabric.InvestigationResult {
	result := validResult("result_producer_guard_row")
	result.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:   "state/subject/team",
		Obligation:    "state",
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        contractsv1.ContextFabricAnswerImpactScope,
		CauseOverrun:  contractsv1.ContextFabricBudgetOverrunItems,
		CauseObserved: true,
		Served:        1,
		Declared:      2,
	}}
	return result
}
