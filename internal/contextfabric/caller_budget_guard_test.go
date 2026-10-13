package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The client-synthesis cut and fit sites read the caller's budget through
// effectiveCallerBudget. A direct read of Options.MaxSerializedBytes sees the
// widened body bound a tool sends, not the caller's budget.
func TestNoClientSynthesisSiteReadsTheBodyBoundDirectly(t *testing.T) {
	fset := token.NewFileSet()
	var seen int
	for _, file := range []string{"client_synthesis.go", "chaos4636_budget_stage3.go", "chaos4636_plan_telemetry.go"} {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "effectiveCallerBudget" || fn.Name.Name == "effectiveResponseBudget" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "MaxSerializedBytes" {
					return true
				}
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Options" {
					t.Errorf("%s: %s reads Options.MaxSerializedBytes directly; use effectiveCallerBudget", fset.Position(sel.Pos()), fn.Name.Name)
				}
				seen++
				return true
			})
		}
	}
	if seen == 0 {
		t.Fatal("the guard saw no MaxSerializedBytes selector; it measured nothing")
	}
}
