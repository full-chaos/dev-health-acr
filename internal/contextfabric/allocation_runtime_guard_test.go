package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
