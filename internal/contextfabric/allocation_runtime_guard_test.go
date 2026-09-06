package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The THIRD CHECK, as a runtime guard rather than a test-time property.
//
// A keystone review found that `Agreement()` had NO production caller. The
// grants were re-derived over a sweep in tests and never once on a serving
// path, so a corrupted allocation still produced `attempt_valid=true
// capacity=certified_fit`: the certificate proved measured CAPACITY, not
// allocation INTEGRITY, while the design claimed three checks. That is this
// change's own standard turned on itself — it spends its time removing guards
// that cannot fire, and shipped one that was never called.
//
// It now raises the SAME typed error an unreconciled ledger raises, because it
// is the same kind of failure: the server cannot account for its own answer.
// No new error kind, no new vocabulary token, no wire field.

// corruptedAllocation is an allocation whose published grants do not match the
// formula they claim to follow.
//
// The residue is moved into a bucket WITHOUT changing the residue field, so the
// parts still sum to the ceiling — a check that merely added the terms up would
// pass this. `Agreement()` re-derives, so it does not.
func corruptedAllocation(t *testing.T) ItemAllocation {
	t.Helper()
	honest := AllocateItems(allocationPlan(30), 2, 1)
	if got := honest.Agreement(); got != AllocationAgrees {
		t.Fatalf("the honest allocation disagrees (%q); the fixture cannot show a corruption against it", got)
	}
	corrupted := honest
	for index, member := range contractsv1.ContextFabricItemBucketVocabulary() {
		if member == contractsv1.ContextFabricItemBucketGlobal {
			corrupted.Grants[index]++
		}
		if member == contractsv1.ContextFabricItemBucketMember {
			corrupted.Grants[index]--
		}
	}
	if corrupted.TotalGranted() != honest.TotalGranted() {
		t.Fatalf("the corruption changed the TOTAL (%d -> %d); it must not, or a summing check would "+
			"catch it and this fixture would not be testing re-derivation",
			honest.TotalGranted(), corrupted.TotalGranted())
	}
	if got := corrupted.Agreement(); got == AllocationAgrees {
		t.Fatal("the corrupted allocation still agrees; the fixture does not corrupt anything")
	}
	return corrupted
}

// TestACorruptedAllocationIsRefusedOnTheServingPath is the guard, driven
// through the ONE function every terminal arm calls.
func TestACorruptedAllocationIsRefusedOnTheServingPath(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	engine := &Engine{telemetry: NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(&sink, nil)))}

	corrupted := corruptedAllocation(t)
	result := groupIncidenceResult(2)

	// The control: the HONEST allocation over the same document must pass, or
	// the assertion below would hold for a function that refuses everything.
	honest := AllocateItems(allocationPlan(30), 2, 1)
	if _, err := engine.measureAssembledAttempt(context.Background(), storage.Principal{OrgID: "org_1"},
		"assembled_result", honest, result, ResponseBudget{MaxItems: 30}); err != nil {
		t.Fatalf("the honest allocation was refused: %v", err)
	}

	_, err := engine.measureAssembledAttempt(context.Background(), storage.Principal{OrgID: "org_1"},
		"assembled_result", corrupted, result, ResponseBudget{MaxItems: 30})
	if err == nil {
		t.Fatal("a corrupted allocation was accepted on the serving path: the third check is not wired in")
	}

	// The SAME error an unreconciled ledger raises: an internal defect, and
	// never the budget refusal that blames the caller's question.
	if !errors.Is(err, ErrItemAccounting) {
		t.Errorf("errors.Is(err, ErrItemAccounting) = false for %v", err)
	}
	if !errors.Is(err, ErrInvalidResult) {
		t.Errorf("errors.Is(err, ErrInvalidResult) = false for %v", err)
	}
	if errors.Is(err, ErrAnswerExceedsBudget) {
		t.Errorf("errors.Is(err, ErrAnswerExceedsBudget) = true for %v: a server-side allocation defect "+
			"would reach the caller as \"your question was too big\"", err)
	}

	// The LINE, not the struct: a field populated and never logged is not
	// telemetry, and this is the artefact an operator alerts on.
	line := sink.String()
	if !strings.Contains(line, "context fabric item accounting disagreement") {
		t.Fatalf("no accounting line was emitted for a refused allocation.\nemitted: %s", line)
	}
	want := "allocation_disagreement=" + string(corrupted.Agreement())
	if !strings.Contains(line, want) {
		t.Errorf("the line carries no %s, so an operator cannot tell WHICH check failed.\nline: %s",
			want, line)
	}
	// And it must not claim the ledger disagreed, because it did not.
	if !strings.Contains(line, "ledger_status="+string(contractsv1.ContextFabricLedgerReconciled)) {
		t.Errorf("the line does not report the ledger as reconciled; the ledger is fine here and saying "+
			"otherwise would send an operator to the wrong half.\nline: %s", line)
	}
	if !strings.Contains(line, "level=ERROR") {
		t.Errorf("the accounting line is not at ERROR level.\nline: %s", line)
	}
}

// TestAnAgreeingAllocationEmitsNoAllocationDisagreement keeps the ordinary case
// honest: the empty value is a vocabulary MEMBER (`AllocationAgrees`), so it
// must pass through as empty rather than being reported as `unclassified`. An
// allocation that agrees has not failed to be classified.
func TestAnAgreeingAllocationEmitsNoAllocationDisagreement(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   AllocationDisagreement
		want string
	}{
		{"agrees passes through as empty", AllocationAgrees, ""},
		{"a real disagreement passes through", AllocationGrantFormula, string(AllocationGrantFormula)},
		{"a value outside the vocabulary fails closed", AllocationDisagreement("sideways"), "unclassified"},
	}
	for _, one := range cases {
		if got := string(validAllocationDisagreementOrUnclassified(one.in)); got != one.want {
			t.Errorf("%s: got %q, want %q", one.name, got, one.want)
		}
	}
}

// TestStageThreeDerivesNoAllocationOfItsOwn is the structural half, and it is
// the honest instrument for this defect.
//
// A keystone review injected `synthesisAllocation.Grants[0]++` at the producer
// and watched the guard pass the answer: stage three was re-deriving its own
// allocation from the same inputs and checking THAT. Because `AllocateItems` is
// pure the two agreed on every honest input, so the duplication was invisible.
//
// MY FIRST ATTEMPT AT PINNING THIS WAS WRONG and is recorded here so it is not
// tried again. I wrote a test that corrupted `input.Allocation` inside a
// synthesizer wrapper. That cannot work: `SynthesisInput` is passed by value, so
// the corruption lands on the synthesizer's own copy and reaches neither the
// engine's allocation nor the guard. It failed identically before and after the
// fix — a test that cannot distinguish them.
//
// The keystone's fault was a SOURCE mutant, not a test hook, so the behavioural
// half belongs in the mutation battery (`corrupt_consumed_allocation`), and what
// belongs here is the structural property the fix actually establishes: there is
// no second allocation for the guard to check the wrong one of.
func TestStageThreeDerivesNoAllocationOfItsOwn(t *testing.T) {
	t.Parallel()
	_, files := parsePackageForQuantifier(t)

	calls := callsWithin(t, files, "fitAssembledResult")
	if !calls["measureAssembledAttempt"] {
		t.Fatal("fitAssembledResult no longer measures anything; this pin is stale and asserts nothing")
	}

	// EXACTLY ONE AllocateItems call may remain in stage three: the retry's,
	// which allocates for a NARROWED cohort and is therefore a different budget
	// for a different document, not a second authority over the same one.
	source := stageThreeSource(t)
	derivations := strings.Count(source, "AllocateItems(")
	if derivations != 1 {
		t.Errorf("stage three contains %d AllocateItems calls, want exactly 1 (the retry's). The "+
			"first-pass allocation must be the one synthesis CONSUMED, carried on the assembly "+
			"params -- a second derivation from the same inputs is what let a corrupted producer "+
			"copy pass a guard checking a clean replacement.", derivations)
	}
	if !strings.Contains(source, "retryAllocation := AllocateItems(") {
		t.Error("the one remaining derivation is not the retry's; stage three has reintroduced a " +
			"first-pass allocation of its own")
	}
	if !strings.Contains(source, "allocation := params.Allocation") {
		t.Error("stage three no longer reads the carried allocation from the params: the guard would " +
			"be checking an object other than the one synthesis consumed")
	}
}

// stageThreeSource reads the stage-three file from disk. The AST helpers above
// answer "which functions call what"; this question is "how many times is one
// constructor called in this file", which is a text property and is read as one.
func stageThreeSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("chaos4636_budget_stage3.go")
	if err != nil {
		t.Fatalf("read stage three source: %v", err)
	}
	return string(body)
}
