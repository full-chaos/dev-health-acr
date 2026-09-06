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
			want: "retried = e.finalizeResult(retried, *plan, params.Frame, retryParams.Facts)",
			bad:  "retried = e.finalizeResult(retried, *plan, params.Frame, params.Facts)",
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
	if !strings.Contains(source, "e.measureAssembledAttempt(ctx, principal, \"re_synthesized_result\", retryAllocation, retried, budget)") {
		t.Error("the retry is not measured against its own allocation")
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
