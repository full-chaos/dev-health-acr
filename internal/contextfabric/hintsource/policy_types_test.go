package hintsource_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
)

// THE TWO POLICY TYPES ARE PINNED BY TYPE-CHECKING, not by a mutation arm, and
// the reason is worth stating because it is the only honest way to test this.
//
// The property is "a read site that names one policy field and the other
// policy's accessor DOES NOT COMPILE". A mutation battery cannot assert that: a
// mutant that fails to build is a harness error, not a kill, so the battery
// would report the guard as unmeasured rather than as working. Type-checking
// the swapped expression in process asserts exactly the property, and asserts it
// FAILS, which is the direction that matters.
//
// AND THE BOUNDARY IS PINNED TOO, in the same test. Swapping the field alone is
// rejected. Swapping the field AND the accessor together is NOT — it is a valid
// expression of a valid type, and no type system catches it. That case is a
// stated limit rather than a covered one, and the test records it by asserting
// that it type-checks, so a future change that accidentally closes it will fail
// here and force the limit's wording to be corrected rather than left stale.
// policyTypeCase is one expression and what the type checker must say about it.
type policyTypeCase struct {
	expr        string
	wantError   bool
	wantMessage string
}

// policyTypeCases is the ONE table both tests below read. Two copies of it —
// one to check and one to audit — would be the second-expected-list shape this
// package exists to remove.
//
// THERE IS NO CASE FOR THE FIELD-AND-ACCESSOR SWAP, and the reason is the limit
// itself rather than an omission.
//
// A case was written for it and it was WRONG: its expression was
// `_ = a.ContestExempt.Exempt()`, character for character the same as "the
// contest read, correct". It duplicated a passing case and asserted nothing,
// which review caught.
//
// It could not have been written correctly. Swapping both the field and its
// accessor produces an expression IDENTICAL to the correct read of the other
// fact — that is exactly what makes it a well-typed mistake. The difference
// lives in WHICH CALL SITE the expression appears at, and a type check over an
// expression in isolation has no call site to look at. No case here can express
// it, so the limit is recorded in the pull request's risk notes and carried in
// the follow-up rather than papered over with a green subtest.
func policyTypeCases() map[string]policyTypeCase {
	return map[string]policyTypeCase{
		"the contest read, correct": {
			expr: `_ = a.ContestExempt.Exempt()`,
		},
		"the short-circuit read, correct": {
			expr: `_ = a.ShortCircuitEligible.Eligible()`,
		},
		"contest field with the short-circuit accessor": {
			expr: `_ = a.ContestExempt.Eligible()`, wantError: true,
			wantMessage: "Eligible",
		},
		"short-circuit field with the contest accessor": {
			expr: `_ = a.ShortCircuitEligible.Exempt()`, wantError: true,
			wantMessage: "Exempt",
		},
		"a policy used directly as a condition": {
			expr: `if a.ContestExempt { _ = 1 }`, wantError: true,
			wantMessage: "non-boolean condition",
		},
	}
}

func TestSwappingOnePolicyFieldDoesNotTypeCheck(t *testing.T) {
	rejections := 0
	for name, testCase := range policyTypeCases() {
		if testCase.wantError {
			rejections++
		}
		t.Run(name, func(t *testing.T) {
			err := typeCheckAgainstHintsource(t, testCase.expr)
			switch {
			case testCase.wantError && err == nil:
				t.Errorf("%q type-checked, and it must not: the two policies are separate types so that a "+
					"read site cannot name one field and the other's accessor", testCase.expr)
			case testCase.wantError && !strings.Contains(err.Error(), testCase.wantMessage):
				t.Errorf("%q failed with %v, which does not mention %q — the rejection must be the one this "+
					"test is about, not some unrelated error", testCase.expr, err, testCase.wantMessage)
			case !testCase.wantError && err != nil:
				t.Errorf("%q must type-check and did not: %v", testCase.expr, err)
			}
		})
	}
	// A table of only-accepted cases would pass while the types did nothing.
	if rejections == 0 {
		t.Error("no case in this table expects a rejection, so it cannot tell the separate types from two bools")
	}
}

// NO TWO CASES MAY SHARE AN EXPRESSION. A duplicated expression is how the
// previous version of this table pretended to cover something it did not: one
// case restated a passing case under a name claiming it measured a limit.
// Asserting uniqueness turns that from a thing a reader has to notice into a
// thing the suite refuses.
func TestNoTwoPolicyTypeCasesShareAnExpression(t *testing.T) {
	seen := map[string]string{}
	for name, testCase := range policyTypeCases() {
		expr := testCase.expr
		if previous, duplicate := seen[expr]; duplicate {
			t.Errorf("cases %q and %q use the SAME expression %s — one of them measures nothing, and a case "+
				"that restates another under a different name reads as coverage it does not have",
				previous, name, expr)
		}
		seen[expr] = name
	}
}

// typeCheckAgainstHintsource type-checks one statement written against a real
// hintsource.Attributes, importing this package FROM SOURCE so the check is
// against the types as they are now rather than against an installed copy.
func typeCheckAgainstHintsource(t *testing.T, statement string) error {
	t.Helper()
	source := `package probe

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"

func probe() {
	var a hintsource.Attributes
	` + statement + `
	_ = a
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", source, 0)
	if err != nil {
		t.Fatalf("the probe does not parse, which is a fixture bug rather than a result: %v", err)
	}
	config := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	_, checkErr := config.Check("probe", fset, []*ast.File{file}, nil)
	return checkErr
}

// AND THE CONTROL: the accessors must actually report the stored values, or
// every rejection above would be pinning a pair of types that mean nothing.
func TestThePolicyAccessorsReportTheStoredValue(t *testing.T) {
	receipt := hintsource.Lookup(string(hintsource.PriorSubjectReceipt))
	recheck := hintsource.Lookup(string(hintsource.AnswerReuseAuthorizationRecheck))
	if receipt.ContestExempt.Exempt() || receipt.ShortCircuitEligible.Eligible() {
		t.Errorf("a prior-subject receipt is neither contest-exempt nor short-circuit eligible; got %+v", receipt)
	}
	if !recheck.ContestExempt.Exempt() || !recheck.ShortCircuitEligible.Eligible() {
		t.Errorf("the reuse recheck is both; got %+v", recheck)
	}
}
