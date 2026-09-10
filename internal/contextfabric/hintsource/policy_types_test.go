package hintsource_test

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
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
// The boundary of that property — swapping the field AND the accessor together
// is well-typed and therefore uncovered — is explained above policyTypeCases,
// where it says why it cannot be written as a case at all, and is recorded in
// the pull request's risk notes.

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

// duplicateExpressions reports every expression two cases share, as
// "case A / case B: expr".
//
// SEPARATED FROM THE TABLE IT CHECKS, deliberately. A guard written inline over
// the real table can only ever be exercised by a table that already has the
// defect — so the real table being clean makes the guard unexecuted code, and a
// mutation disabling it survives while reporting nothing. That is the same
// shape as a scan whose only corpus is a codebase containing none of the cases
// it governs, which review already found once in this change set. As a function
// it can be run against a table built to contain the defect.
func duplicateExpressions(cases map[string]policyTypeCase) []string {
	seen := map[string]string{}
	duplicates := make([]string, 0)
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		expr := cases[name].expr
		if previous, ok := seen[expr]; ok {
			duplicates = append(duplicates, previous+" / "+name+": "+expr)
			continue
		}
		seen[expr] = name
	}
	return duplicates
}

// NO TWO CASES MAY SHARE AN EXPRESSION. A duplicated expression is how the
// previous version of this table pretended to cover something it did not: one
// case restated a passing case under a name claiming it measured a limit.
func TestNoTwoPolicyTypeCasesShareAnExpression(t *testing.T) {
	if found := duplicateExpressions(policyTypeCases()); len(found) != 0 {
		t.Errorf("cases share expressions: %v — a case that restates another under a different name reads as "+
			"coverage it does not have", found)
	}
}

// AND THE DETECTOR IS ITSELF EXERCISED, on a table built to contain exactly the
// defect the real one must not. Without this the check above is a guard nothing
// runs: the real table is clean, so disabling the detector changes nothing any
// test can see.
func TestTheDuplicateDetectorFindsADuplicate(t *testing.T) {
	planted := map[string]policyTypeCase{
		"the contest read, correct":                   {expr: `_ = a.ContestExempt.Exempt()`},
		"BOTH field and accessor swapped — the shape": {expr: `_ = a.ContestExempt.Exempt()`},
		"an unrelated case":                           {expr: `_ = a.ShortCircuitEligible.Eligible()`},
	}
	found := duplicateExpressions(planted)
	if len(found) != 1 {
		t.Fatalf("duplicateExpressions found %d duplicates in a table with exactly one, %v", len(found), found)
	}
	if !strings.Contains(found[0], "a.ContestExempt.Exempt()") {
		t.Errorf("the report %q does not name the shared expression", found[0])
	}
	// AND it must not cry duplicate over a clean table.
	if extra := duplicateExpressions(map[string]policyTypeCase{
		"one": {expr: `_ = a.ContestExempt.Exempt()`},
		"two": {expr: `_ = a.ShortCircuitEligible.Eligible()`},
	}); len(extra) != 0 {
		t.Errorf("duplicateExpressions reported %v on a table with no duplicates", extra)
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
