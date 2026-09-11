package contextfabric

import (
	"os"
	"strings"
	"testing"
)

// SOURCE-LEVEL WIRING ASSERTIONS -- NOW ONLY FOR THE RETRY ALLOCATION.
//
// This file used to pin, by source text, which fact bundle the stage-3 retry
// passes to its finalization and to its candidate narrowing, and that the
// population file never names the returned or invoked sets. An adversarial
// round named those as guards that check text instead of behaviour, and both
// are now EXECUTED instead: see read_population_retry_behaviour_test.go.
//
// What remains below is the retry ALLOCATION wiring (retryAllocation /
// forRetry / consumedRetryAllocation). It is another lane's class, this change
// does not touch those lines, and replacing it behaviourally needs a fault
// injected inside synthesizeAndAssemble. It stays source-level, by an explicit
// decision, and is recorded as an owed follow-up -- a source assertion claims
// only that the call site binds the producer's returned allocation, never that
// the retry measures correctly.

func sourceOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// TestTheRetryAllocationIsBoundToWhatTheProducerConsumed pins, at source, the
// retry allocation wiring described in this file's header.
func TestTheRetryAllocationIsBoundToWhatTheProducerConsumed(t *testing.T) {
	t.Parallel()
	source := sourceOf(t, "chaos4636_budget_stage3.go")

	// THE OTHER LANE'S HALF OF THE SAME SITE, pinned here because the two
	// fixes now live together and a resolution that kept only one would still
	// compile and still pass every arm that pins the other.
	//
	// The retry's ALLOCATION must be computed over the NARROWED cohort, just
	// as its EVIDENCE must come from the narrowed bundle. They are the same
	// "stale document at the retry" class on different axes, found
	// independently by two lanes; taking one without the other re-opens the
	// half it did not fix.
	if !strings.Contains(source, "retryAllocation := AllocateItems(*plan, groupCountOf(narrowed.Graph.Cohort), cohortMemberCount(narrowed.Graph.Cohort))") {
		t.Error("the retry does not compute its OWN allocation over the narrowed cohort; measuring the " +
			"retried document against the first pass's grants is the sibling of evaluating it against " +
			"the first pass's facts")
	}
	// THE ALLOCATION MUST BE HANDED TO THE PRODUCER, AND READ BACK FROM IT.
	//
	// This assertion used to require `..., retryAllocation, retried, budget)`
	// -- measuring the LOCAL variable. The other lane's own keystone review
	// showed that is the weaker binding and superseded it: `ItemAllocation` is
	// a VALUE type, so passing it to `forRetry` hands the producer a COPY, and
	// measuring the local then measures a different object that merely happens
	// to be equal. Equal on every honest input, separable only under a fault
	// -- which is exactly the defect class this whole site exists to close.
	//
	// SO THIS PIN IS UPDATED, NOT BUMPED, AND THIS IS ITS THIRD REVISION --
	// each one strictly stronger than the last, each one driven by the other
	// lane finding a weaker binding than the one this test was demanding:
	//
	//   1. `retryAllocation`          the LOCAL variable
	//   2. `retryParams.Allocation`   the params the producer was HANDED
	//   3. `consumedRetryAllocation`  what the producer RETURNED as spent
	//
	// Revision 2 was not enough, and the reason is worth keeping: `params` is
	// passed BY VALUE, so `retryParams.Allocation` is still the CALLER's copy.
	// A fault applied inside the producer to its own allocation can never
	// reach a guard that reads the caller's object -- which is exactly how
	// narration came to spend a re-copied local that nothing validated. Only
	// the value the producer HANDS BACK can witness what was actually spent.
	//
	// A pin that keeps asserting a superseded form forces a merge to re-open
	// the other lane's defect in order to go green -- a test dictating a
	// regression. So both weaker forms are REFUSED below, not merely unasked.
	if !strings.Contains(source, "params.forRetry(narrowed.Graph, narrowed.Facts, retryAllocation)") {
		t.Error("the retry's allocation is not handed to the producer; a document must be PRODUCED " +
			"under the grants it is later MEASURED against, or the measurement describes a shape " +
			"nobody synthesized")
	}
	if !strings.Contains(source, "retried, consumedRetryAllocation, retryPending, retryErr := e.synthesizeAndAssemble(ctx, principal, retryParams)") {
		t.Error("the retry producer does not RETURN what it consumed; without that return value the " +
			"guard below has nothing to read but the caller's own copy")
	}
	if !strings.Contains(source, "e.measureAssembledAttempt(ctx, principal, \"re_synthesized_result\", consumedRetryAllocation, retried, budget)") {
		t.Error("the retry is not measured against the allocation the PRODUCER RETURNED as consumed; " +
			"any caller-side object is a copy that agrees on every honest input and diverges only " +
			"under a fault")
	}
	// Both superseded forms, refused by name so neither can return.
	if strings.Contains(source, "e.measureAssembledAttempt(ctx, principal, \"re_synthesized_result\", retryAllocation, retried, budget)") {
		t.Error("the retry is measured against the LOCAL allocation (revision-1 shape); measure " +
			"`consumedRetryAllocation`, what the producer returned as spent")
	}
	if strings.Contains(source, "e.measureAssembledAttempt(ctx, principal, \"re_synthesized_result\", retryParams.Allocation, retried, budget)") {
		t.Error("the retry is measured against the params the producer was HANDED (revision-2 shape). " +
			"`params` is by value, so that is still the caller's copy and a producer-local fault " +
			"cannot reach it; measure `consumedRetryAllocation`")
	}
}
