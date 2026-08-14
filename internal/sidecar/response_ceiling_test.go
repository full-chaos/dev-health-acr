package sidecar

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// requiredCeilingMargin is how many times the sidecar's hard ceiling must
// clear the largest single response the hosted API will serve.
//
// It is a margin, not a tolerance. Equality would make the two values one
// change apart from a truncation that shows up only under maximum load,
// and the failure mode is silent: a truncated JSON body is a decode error
// at the client, blamed on the network rather than on a limit.
const requiredCeilingMargin = 8

// TestSidecarCeilingClearsTheServingBudget is CHAOS-3795, option (a).
//
// The sidecar refuses to read more than maxResponseBytesCeil bytes of any
// hosted response body. That is only safe if no legitimate response can be
// larger, and the largest one the service will serve is bounded by
// ContextFabricInvestigationOptions.MaxSerializedBytes, whose own maximum
// is ContextFabricSerializedBytesMax.
//
// Both sides are read from constants here rather than restated, which is
// the whole point: this asserts a RELATION, and it stays true only while
// both numbers are named. Raising the serving budget past an eighth of the
// ceiling fails this test at the moment of the change, not in production.
//
// Scope: one response, deliberately. The contract's aggregate maximum
// across a full expansion runs to hundreds of MiB, and no ceiling that
// cleared THAT would bound anything useful. The sidecar reads one response
// at a time (see TestEveryHostedResponseBodyIsReadThroughTheCeiling), so
// one response is the right quantity to compare.
func TestSidecarCeilingClearsTheServingBudget(t *testing.T) {
	serving := int64(contractsv1.ContextFabricSerializedBytesMax)

	if got := int64(maxResponseBytesCeil); got < serving*requiredCeilingMargin {
		t.Errorf("the sidecar ceiling is %d bytes; the serving budget is %d, so the ceiling must be at least %d (%dx)",
			got, serving, serving*requiredCeilingMargin, requiredCeilingMargin)
	}
	// The DEFAULT matters too, and separately: an operator who never sets
	// ACR_API_MAX_RESPONSE_BYTES runs on it, so a default below the
	// serving budget would truncate a legitimate maximal answer on a
	// stock deployment even though the ceiling above is generous.
	if got := int64(defaultMaxResponseBytes); got < serving {
		t.Errorf("the default response limit is %d bytes, below the %d-byte serving budget: a stock sidecar truncates a maximal answer", got, serving)
	}
	// And the configurable FLOOR is deliberately below the serving budget
	// -- an operator may choose a tight limit -- so this states that as an
	// intended asymmetry rather than leaving a reader to wonder whether it
	// is the same defect.
	if int64(minResponseBytes) >= serving {
		t.Errorf("minResponseBytes (%d) is no longer below the serving budget; the operator-tightening case this comment describes has changed", minResponseBytes)
	}
}

// TestEveryHostedResponseBodyIsReadThroughTheCeiling asserts the PREMISE
// the test above rests on, at the source level.
//
// The margin proves the ceiling is big enough. It proves nothing about
// whether the ceiling is applied. A third read path -- one io.ReadAll on a
// response body -- would leave the arithmetic above true and correct while
// the guarantee it describes no longer holds anywhere.
//
// So this pins the shape rather than the behaviour: readLimited is called
// at exactly the two call sites that exist (the transport read and the
// lifecycle read), and every one passes the configured ceiling rather than
// a local number. Adding a hosted read path is then a deliberate act that
// updates this test, not an omission nobody notices.
func TestEveryHostedResponseBodyIsReadThroughTheCeiling(t *testing.T) {
	const expectedCallSites = 2

	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	type callSite struct {
		file     string
		argument string
	}
	var sites []callSite
	var bodyReads []string

	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch function := call.Fun.(type) {
				case *ast.Ident:
					if function.Name == "readLimited" && len(call.Args) == 2 {
						sites = append(sites, callSite{file: fileName, argument: exprText(fileSet, call.Args[1])})
					}
				case *ast.SelectorExpr:
					// An io.ReadAll whose reader is NOT an io.LimitReader
					// is the shape that bypasses every ceiling in this
					// package, so it is named explicitly: the failure then
					// says what went wrong rather than only that a count
					// moved. Every existing read here is already wrapped,
					// including readLimited's own, so the wrapped form is
					// the convention this pins rather than an exemption.
					if identifier, ok := function.X.(*ast.Ident); ok && identifier.Name == "io" && function.Sel.Name == "ReadAll" {
						if len(call.Args) != 1 || !isLimitReaderCall(call.Args[0]) {
							bodyReads = append(bodyReads, fileSet.Position(call.Pos()).String())
						}
					}
				}
				return true
			})
		}
	}

	if len(bodyReads) > 0 {
		t.Errorf("unbounded io.ReadAll at %v; a read that is not wrapped in io.LimitReader ignores every ceiling this package configures", bodyReads)
	}
	if len(sites) != expectedCallSites {
		t.Fatalf("readLimited has %d call sites, pinned %d: %+v.\nA new hosted read path must pass the configured ceiling and update this pin; a vanished one means the ceiling stopped being applied somewhere.",
			len(sites), expectedCallSites, sites)
	}
	for _, site := range sites {
		if site.argument != "c.cfg.MaxResponseBytes" {
			t.Errorf("%s reads with limit %q, not the configured ceiling: a local number cannot be tuned by an operator and is invisible to the margin check above", site.file, site.argument)
		}
	}
}

// exprText renders an expression back to source text for the assertions
// above. Only the selector shapes this test cares about need to render
// exactly; anything else renders well enough to name in a failure.
func exprText(fileSet *token.FileSet, expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.SelectorExpr:
		return exprText(fileSet, value.X) + "." + value.Sel.Name
	case *ast.Ident:
		return value.Name
	default:
		return fileSet.Position(expression.Pos()).String()
	}
}

// isLimitReaderCall reports whether expression is io.LimitReader(...). The
// wrapper is what makes a read bounded; ReadAll over anything else is not.
func isLimitReaderCall(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == "io" && selector.Sel.Name == "LimitReader"
}
