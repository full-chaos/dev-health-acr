package contextfabric

import (
	"go/ast"
	"testing"
)

// The STRUCTURAL half of the final-recheck guard.
//
// The behavioural half cannot exist. `assertFitsBudget` refuses a finished
// document whose account does not reconcile, and no test can hand it one: the
// reconciler is three walks over the same document, so a disagreement is a
// defect inside the contracts package, never something a caller constructs. A
// mutation battery proved the consequence -- the whole refusal could be removed
// and every test in the package still passed.
//
// The decision itself is now a pure constructor and IS tested directly
// (TestAnAccountingDisagreementIsAnInternalErrorAndNeverA413). What remains
// untestable by execution is the WIRING: that the finalizer re-derives the
// ledger on the document in hand and routes it through that constructor. So the
// wiring is pinned structurally instead, which is the same answer this package
// already gives for the one-stamp path.
//
// This is deliberately NOT a substitute for a behavioural test where one is
// possible. It is the honest instrument for a guard whose trigger is another
// package's defect.

// callsWithin reports the callee names invoked inside the named function.
func callsWithin(t *testing.T, files []*ast.File, function string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	seen := false
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name != function {
				continue
			}
			seen = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, isCall := node.(*ast.CallExpr); isCall {
					if name := calleeName(call); name != "" {
						found[name] = true
					}
				}
				return true
			})
		}
	}
	if !seen {
		t.Fatalf("no function named %q was parsed: the pin is looking at the wrong package and would "+
			"pass vacuously once the call it guards is deleted", function)
	}
	return found
}

// TestTheFinalAssertionReDerivesTheLedgerAndRoutesItThroughTheOneConstructor is
// the pin.
func TestTheFinalAssertionReDerivesTheLedgerAndRoutesItThroughTheOneConstructor(t *testing.T) {
	t.Parallel()
	_, files := parsePackageForQuantifier(t)
	calls := callsWithin(t, files, "assertFitsBudget")

	// (1) The ledger is re-derived HERE, on the finished document. Stage
	//     three measured an earlier shape -- before the plan stamp, the
	//     requirement gap-fill and the display labels -- so an account
	//     reconciled there says nothing about what the route serializes.
	if !calls["ReconcileContextFabricResultItems"] {
		t.Error("assertFitsBudget does not call ReconcileContextFabricResultItems: the finished " +
			"document is served on an account taken before the late writers ran")
	}
	// (2) And the refusal goes through the ONE constructor, so the raised
	//     error and the emitted line cannot describe different documents.
	if !calls["itemAccountingErrorForLedger"] {
		t.Error("assertFitsBudget does not call itemAccountingErrorForLedger: a document whose account " +
			"does not add up would be served")
	}
	if !calls["itemAccountingEventFor"] {
		t.Error("assertFitsBudget does not call itemAccountingEventFor: the refusal would be raised " +
			"with nothing written down for an operator")
	}
}

// TestTheAccountingPinCanActuallyFail is the pin's own negative control.
//
// A structural pin that cannot fail is worse than none: it reports a guarantee
// nobody holds. This asserts the detector discriminates, by asking it about a
// function that legitimately makes none of those calls.
func TestTheAccountingPinCanActuallyFail(t *testing.T) {
	t.Parallel()
	_, files := parsePackageForQuantifier(t)
	// classifyItemQuota is a pure switch. If the detector reported the
	// accounting calls inside IT, it would be reporting them everywhere, and
	// the pin above would pass no matter what assertFitsBudget did.
	calls := callsWithin(t, files, "classifyItemQuota")
	for _, name := range []string{
		"ReconcileContextFabricResultItems",
		"itemAccountingErrorForLedger",
		"itemAccountingEventFor",
	} {
		if calls[name] {
			t.Errorf("the detector reports %q inside classifyItemQuota, which does not call it: the "+
				"pin is not discriminating and would pass vacuously", name)
		}
	}
	// And it does find calls that ARE there, so an empty result is not what
	// makes the loop above pass.
	if len(calls) == 0 {
		t.Error("the detector found no calls at all inside classifyItemQuota; it may be finding " +
			"nothing anywhere, which would make the pin above vacuous in the other direction")
	}
}
