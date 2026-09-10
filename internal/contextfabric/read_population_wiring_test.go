package contextfabric

import (
	"os"
	"strings"
	"testing"
)

// SOURCE-LEVEL WIRING ASSERTIONS.
//
// WHY A SOURCE ASSERTION AND NOT A BEHAVIOURAL ONE, stated because the choice
// is a real limitation and not a convenience. The two defects below are about
// WHICH bundle a call site passes, and both call sites sit inside the stage-3
// retry, downstream of a synthesis retry that a unit fixture cannot reach
// without standing up the whole assembly path. The mutation battery found both
// surviving every behavioural arm in this package.
//
// A source assertion kills them deterministically and claims only what it
// proves: that the call site passes the retry's own bundle. It does NOT prove
// the retry produces the right answer -- no test here does, and saying so is
// the point. If the stage-3 path later grows a fixture that can drive a real
// retry, these become redundant and should be replaced rather than kept beside
// it.

func sourceOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// TestTheRetryEvaluatesReadsOverTheBundleItSynthesizedFrom pins both stage-3
// call sites to the RETRY's bundle.
//
// A retry finalized against the FIRST pass's facts reports coverage for a
// document nobody served: the narrowed answer is smaller, so its population
// would be measured against evidence for subjects the retry dropped.
func TestTheRetryEvaluatesReadsOverTheBundleItSynthesizedFrom(t *testing.T) {
	t.Parallel()
	source := sourceOf(t, "chaos4636_budget_stage3.go")

	for _, call := range []struct {
		name string
		want string
		bad  string
	}{
		{
			name: "the retry finalization",
			want: "retried = e.finalizeResult(ctx, principal, retried, *plan, params.Frame, retryParams.Facts)",
			bad:  "retried = e.finalizeResult(ctx, principal, retried, *plan, params.Frame, params.Facts)",
		},
		{
			name: "the SECOND planCandidateNarrowing, on the retried result",
			want: "e.planCandidateNarrowing(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, retryParams.Facts)",
			bad:  "e.planCandidateNarrowing(ctx, principal, plan, params.Frame, retried, budget, retryMeasured, params.Facts)",
		},
	} {
		if strings.Count(source, call.want) != 1 {
			t.Errorf("%s: expected exactly one call passing the RETRY bundle:\n  %s", call.name, call.want)
		}
		if strings.Contains(source, call.bad) {
			t.Errorf("%s: passes the FIRST PASS's bundle. The retried result was synthesized from the "+
				"narrowed bundle, so evaluating it against the first pass's facts reports coverage for "+
				"a document nobody served.\n  %s", call.name, call.bad)
		}
	}

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

	// The FIRST planCandidateNarrowing runs on the first-pass result and must
	// take the first-pass bundle -- the mirror image, asserted so a lane
	// "fixing" the above does not make both sites pass the retry bundle.
	first := "e.planCandidateNarrowing(ctx, principal, plan, params.Frame, result, budget, measured, params.Facts)"
	if strings.Count(source, first) != 1 {
		t.Errorf("the FIRST planCandidateNarrowing must take the first-pass bundle; it runs on the "+
			"first-pass result:\n  %s", first)
	}
}

// TestTheDenominatorSourceNeverReachesForTheReturnedOrInvokedSet is T-DENOM's
// second half: the population file must never NAME the sets that are not the
// population.
//
// The behavioural arms prove the numbers are right today. This proves the
// wrong input is not even reachable, which is what stops a later edit from
// quietly making the denominator a function of what came back.
func TestTheDenominatorSourceNeverReachesForTheReturnedOrInvokedSet(t *testing.T) {
	t.Parallel()
	// COMMENTS STRIPPED FIRST. The file's own header NAMES these symbols in
	// order to say why they are excluded, and a sweep over raw text therefore
	// fails on the very prose that documents the rule. Scanning code only is
	// what makes this an assertion about behaviour rather than about wording.
	source := codeOnly(sourceOf(t, "read_population.go"))
	for symbol, why := range map[string]string{
		"investigationScopeSubjectSet": "the INVOKED set is what a capability was asked about, not what the requirement completes over",
		"ClaimedFacts":                 "claimed facts are MODEL OUTPUT; a population derived from them is a population the model chose",
		"facts.Scope":                  "the bundle's read scope is the invoked set, not the population",
	} {
		if strings.Contains(source, symbol) {
			t.Errorf("read_population.go references %q -- %s", symbol, why)
		}
	}
	// NON-VACUITY: the file must actually contain the symbols it SHOULD use,
	// or this sweep would pass against an empty file.
	for _, required := range []string{"frameRoleSlots", "ComputeMembershipCardinality", "cohort.Groups"} {
		if !strings.Contains(source, required) {
			t.Errorf("read_population.go does not reference %q; the sweep above is vacuous if the file "+
				"does not use the owners it is supposed to", required)
		}
	}
}

// codeOnly strips // and /* */ comments so a source sweep asserts what the file
// DOES, never what it says about itself.
func codeOnly(source string) string {
	var out strings.Builder
	inBlock := false
	for _, line := range strings.Split(source, "\n") {
		for len(line) > 0 {
			if inBlock {
				end := strings.Index(line, "*/")
				if end < 0 {
					line = ""
					break
				}
				line = line[end+2:]
				inBlock = false
				continue
			}
			lineComment := strings.Index(line, "//")
			blockOpen := strings.Index(line, "/*")
			switch {
			case lineComment >= 0 && (blockOpen < 0 || lineComment < blockOpen):
				out.WriteString(line[:lineComment])
				line = ""
			case blockOpen >= 0:
				out.WriteString(line[:blockOpen])
				line = line[blockOpen+2:]
				inBlock = true
			default:
				out.WriteString(line)
				line = ""
			}
		}
		out.WriteString("\n")
	}
	return out.String()
}
