package contextfabric

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The FIFTH site of this branch's recurring defect class -- one decision
// described by two documents -- and the second one in the allocator itself.
//
// Keystone review #3 found that stage three VALIDATED an allocation it had
// re-derived rather than the one synthesis CONSUMED. The fix (91408cc1)
// derived the first-pass allocation once, at params construction, and carried
// it on `synthesisAssemblyParams.Allocation`.
//
// That fix was INCOMPLETE, and keystone review #4 found the half it missed.
// `forRetry` copies the graph, the facts, the resolution and the cohort -- and
// says nothing about `Allocation`, so the first pass's allocation rides through
// unchanged on `retry := p`. The retry's synthesis therefore SPENDS the
// allocation written for the UN-NARROWED cohort, while stage three derives the
// narrowed cohort's allocation AFTER synthesis and MEASURES against that one.
// Consumed and validated are two different documents again, one level down.
//
// It needs no fault injection to matter: the retried answer is produced under
// grants for a member and group population it no longer has, so its narration
// budget is wrong and the served content changes.
//
// WHY THIS TEST TAKES THE SHAPE IT DOES. A test that corrupts `input.Allocation`
// inside a synthesizer wrapper cannot pin anything here -- `SynthesisInput` is
// passed BY VALUE, so the corruption lands on the synthesizer's own copy and
// fails identically before and after a fix. That dead end is already recorded
// on the first-pass pin. This test therefore only OBSERVES what each synthesis
// call was handed, which a by-value copy carries faithfully, and asserts a
// relation between the two calls.
//
// THE PROPERTY, and it needs no plan of its own: `AllocateItems` is a function
// of the member and group counts, so a cohort that genuinely narrowed MUST
// yield a different allocation. If the two synthesis calls were handed equal
// allocations across a narrowing, the second one was not derived for the cohort
// it was given.

// recordedSynthesis is what one synthesis call was handed. The Allocation and
// the cohort sizes are read off the input rather than reconstructed, because
// the question is what the producer actually spent, not what it should have.
type recordedSynthesis struct {
	allocation ItemAllocation
	members    int
	groups     int
}

// recordingSynthesizerCalls wraps a synthesizer and records every call's
// allocation and cohort shape, passing the call through untouched.
func recordingSynthesizerCalls(inner AnswerSynthesizer, into *[]recordedSynthesis) AnswerSynthesizer {
	return synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		*into = append(*into, recordedSynthesis{
			allocation: input.Allocation,
			members:    cohortMemberCount(input.Graph.Cohort),
			groups:     groupCountOf(input.Graph.Cohort),
		})
		return inner.Synthesize(ctx, principal, input)
	})
}

// TestTheRetryIsSynthesizedUnderItsOwnCohortsAllocation is the behavioural half.
func TestTheRetryIsSynthesizedUnderItsOwnCohortsAllocation(t *testing.T) {
	t.Parallel()
	calls := 0
	telemetry := &recordingTelemetry{}
	// The same fixture path 3 uses to reach a retry that actually RUNS: an
	// overlapping grouped cohort that overruns at maxItems=20, with a reserve
	// large enough that the retry is admitted rather than declined.
	engine := budgetStageEngine(t, chaos4809OverlappingGroupedCohort(), 20, budgetStageOptions(20, time.Second), &calls, telemetry)
	var seen []recordedSynthesis
	engine.synthesizer = recordingSynthesizerCalls(engine.synthesizer, &seen)

	// The error is not the subject here: this path may serve or refuse
	// depending on whether the narrowed answer fits. What matters is what the
	// SECOND synthesis was handed.
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())

	if len(seen) != 2 {
		t.Fatalf("synthesis ran %d time(s), want 2 -- the retry must actually RUN or this test asserts nothing", len(seen))
	}
	first, retry := seen[0], seen[1]

	// CONTROL. If the cohort did not narrow, the two allocations SHOULD be
	// equal and the assertion below would be vacuous -- it would pass on the
	// broken code for the wrong reason. Fail loudly instead of asserting
	// nothing.
	if retry.members == first.members && retry.groups == first.groups {
		t.Fatalf("the cohort did not narrow (members %d->%d, groups %d->%d): this fixture cannot "+
			"distinguish a retry allocated for its own cohort from one that inherited the first "+
			"pass's, so the assertion below would be vacuous",
			first.members, retry.members, first.groups, retry.groups)
	}

	// THE DEFECT. AllocateItems is a function of the group and member counts,
	// so across a genuine narrowing the retry's allocation cannot legitimately
	// equal the first pass's.
	if retry.allocation == first.allocation {
		t.Errorf("the retry was synthesized under the FIRST pass's allocation.\n"+
			"  first pass: %d member(s), %d group(s), grants %v\n"+
			"  retry     : %d member(s), %d group(s), grants %v\n"+
			"The retried document is produced under grants written for a member and group "+
			"population it no longer has, and stage three then measures it against a THIRD "+
			"allocation derived after synthesis. Consumed and validated must be one document.",
			first.members, first.groups, first.allocation.Grants,
			retry.members, retry.groups, retry.allocation.Grants)
	}

	// The retry's own allocation must still be internally coherent -- a
	// different allocation that does not agree with its own formula would be a
	// different defect wearing this one's clothes.
	if got := retry.allocation.Agreement(); got != AllocationAgrees {
		t.Errorf("the retry's allocation does not agree with its own formula: %q", got)
	}
}

// TestForRetryStatesTheAllocationExplicitly is the structural half, and it is
// the one that closes the CLASS rather than the instance.
//
// The first-pass fix left `forRetry` free to carry `Allocation` through by
// silent struct copy, so the defect was reintroduced by omission rather than by
// commission -- nobody wrote a second derivation, they just failed to write the
// first. A pin that counts derivations cannot see that. Requiring the
// allocation as a PARAMETER makes the omission a compile error instead: a
// future retry path must say which allocation its document is produced under.
func TestForRetryStatesTheAllocationExplicitly(t *testing.T) {
	t.Parallel()
	assembly := readPackageSource(t, "chaos4636_synthesis_assembly.go")
	if !strings.Contains(assembly, "forRetry(graph GraphContext, facts CanonicalFactBundle, allocation ItemAllocation)") {
		t.Error("forRetry does not take the allocation as a parameter: a retry can again inherit the " +
			"first pass's grants by silent struct copy, which is exactly how this defect was " +
			"reintroduced after the first-pass fix")
	}
	if !strings.Contains(assembly, "retry.Allocation = allocation") {
		t.Error("forRetry does not set Allocation from its parameter, so the parameter is decorative " +
			"and the first pass's allocation still rides through on the struct copy")
	}

	stage := stageThreeSource(t)
	if !strings.Contains(stage, "params.forRetry(narrowed.Graph, narrowed.Facts, retryAllocation)") {
		t.Error("stage three does not pass the retry allocation into forRetry: the retried document " +
			"would again be produced under one allocation and measured against another")
	}
	// The guard must read the OBJECT the producer was handed, not a local
	// variable that merely holds an equal value. ItemAllocation is a value
	// type, so `forRetry(..., retryAllocation)` gives the producer a copy:
	// measuring `retryAllocation` here would reproduce keystone #3's defect
	// exactly -- two objects, equal on every honest input, separated only by a
	// fault.
	if !strings.Contains(stage, `"re_synthesized_result", retryParams.Allocation,`) {
		t.Error("the retry measurement does not read retryParams.Allocation: the runtime guard would " +
			"be checking a copy rather than the allocation the retry synthesis actually consumed")
	}
	// Derived BEFORE synthesis, or the ordering that caused this defect is
	// still in place: a value computed after the producer has already spent
	// cannot be what the producer spent.
	derivedAt := strings.Index(stage, "retryAllocation := AllocateItems(")
	synthesizedAt := strings.Index(stage, "e.synthesizeAndAssemble(ctx, principal, retryParams)")
	if derivedAt < 0 {
		t.Fatal("stage three no longer derives retryAllocation; this pin is stale and asserts nothing")
	}
	if synthesizedAt < 0 {
		t.Fatal("stage three no longer calls synthesizeAndAssemble for the retry; this pin is stale")
	}
	if derivedAt > synthesizedAt {
		t.Error("the retry allocation is derived AFTER the retry synthesis: the producer cannot have " +
			"spent a value that did not exist when it ran, which is precisely this defect")
	}
	// Derived ONCE, like the first pass's. Two derivations here would be the
	// original defect in the retry's clothing.
	if got := strings.Count(stage, "AllocateItems("); got != 1 {
		t.Errorf("stage three contains %d AllocateItems calls, want exactly 1 (the retry's, derived "+
			"before synthesis and used for both producing and measuring the retried document)", got)
	}
}

// readPackageSource reads one file of this package from disk. The AST helpers
// elsewhere answer "which functions call what"; these questions are about a
// signature and an ORDER within one file, which are text properties.
func readPackageSource(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}
