package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

var (
	servedBodyWriters  = []string{"writeEncodedJSON", "writeJSON"}
	declaredBudgetKeys = []string{"MaxSerializedBytes", "MaxBytes", "AnswerBudgetBytes"}
)

type budgetedBuilders struct {
	scanned, unmeasured []string
}

// A comparing measurer takes the budget itself (as an argument or in its own
// body) and reports whether the body fits; its result must be used. A plain
// measurer returns a size; its result must be compared with a value read from
// the declared budget.
var (
	comparingMeasurers = []string{"encodeBounded", "packetWithinRequestLimits", "CompleteUsageWithBudget"}
	sizeMeasurers      = []string{"marshalContextFabricResponse"}
	relationalOps      = []token.Token{token.GTR, token.GEQ, token.LSS, token.LEQ}
)

func calledName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func readsDeclaredBudget(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && slices.Contains(declaredBudgetKeys, sel.Sel.Name) {
			found = true
		}
		return !found
	})
	return found
}

// identsOf lists the identifiers an expression mentions.
func identsOf(node ast.Node) []string {
	var names []string
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name != "_" {
			names = append(names, id.Name)
		}
		return true
	})
	return names
}

// measuresAgainstBudget says whether a function that reads a declared byte
// budget measures what it serves against it: a comparing measurer whose result
// is used, or a size measurer whose result is compared with a value that came
// from the budget.
func measuresAgainstBudget(body *ast.BlockStmt) bool {
	budgetValues := map[string]bool{}
	sizeValues := map[string]bool{}
	comparing := false
	ast.Inspect(body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, rhs := range assign.Rhs {
			if readsDeclaredBudget(rhs) {
				for _, lhs := range assign.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
						budgetValues[id.Name] = true
					}
				}
			}
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			name := calledName(call)
			var kept []string
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					kept = append(kept, id.Name)
				}
			}
			if slices.Contains(sizeMeasurers, name) {
				for _, id := range kept {
					sizeValues[id] = true
				}
			}
			if slices.Contains(comparingMeasurers, name) && len(kept) > 0 {
				comparing = true
			}
		}
		return true
	})
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.IfStmt:
			if call, ok := ast.Unparen(unaryOperand(n.Cond)).(*ast.CallExpr); ok && slices.Contains(comparingMeasurers, calledName(call)) {
				comparing = true
			}
			ast.Inspect(n.Cond, func(c ast.Node) bool {
				if call, ok := c.(*ast.CallExpr); ok && slices.Contains(comparingMeasurers, calledName(call)) {
					comparing = true
				}
				return true
			})
		case *ast.BinaryExpr:
			if !slices.Contains(relationalOps, n.Op) {
				return true
			}
			left, right := identsOf(n.X), identsOf(n.Y)
			if (anyIn(left, sizeValues) && (anyIn(right, budgetValues) || readsDeclaredBudget(n.Y))) ||
				(anyIn(right, sizeValues) && (anyIn(left, budgetValues) || readsDeclaredBudget(n.X))) {
				comparing = true
			}
		}
		return true
	})
	return comparing
}

func unaryOperand(expr ast.Expr) ast.Expr {
	if unary, ok := expr.(*ast.UnaryExpr); ok {
		return unary.X
	}
	return expr
}

func anyIn(names []string, set map[string]bool) bool {
	for _, name := range names {
		if set[name] {
			return true
		}
	}
	return false
}

// scanBudgetedBuilders finds the functions that serve a body and read a
// declared byte budget, and which of them never measure that body against it.
func scanBudgetedBuilders(files []*ast.File) budgetedBuilders {
	var found budgetedBuilders
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			serves, reads := false, readsDeclaredBudget(fn.Body)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok && slices.Contains(servedBodyWriters, calledName(call)) {
					serves = true
				}
				return true
			})
			if serves && reads {
				found.scanned = append(found.scanned, fn.Name.Name)
				if !measuresAgainstBudget(fn.Body) {
					found.unmeasured = append(found.unmeasured, fn.Name.Name)
				}
			}
		}
	}
	slices.Sort(found.scanned)
	slices.Sort(found.unmeasured)
	return found
}

func parsePackageSources(t *testing.T) []*ast.File {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(info fs.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			files = append(files, file)
		}
	}
	return files
}

func parseSnippet(t *testing.T, source string) []*ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "snippet.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	return []*ast.File{file}
}

// Every handler that serves a body and reads a declared byte budget measures
// the body against it. The scan must see the handlers known to carry a
// budget, or it measured nothing.
func TestEveryBudgetedBodyIsMeasuredAgainstItsBudget(t *testing.T) {
	found := scanBudgetedBuilders(parsePackageSources(t))
	for _, known := range []string{"ContextFabricInvestigationHandler", "ContextFabricInvestigationResultHandler", "handleContextPacket"} {
		if !slices.Contains(found.scanned, known) {
			t.Fatalf("the census did not see %s among %v: it measured nothing", known, found.scanned)
		}
	}
	if len(found.unmeasured) > 0 {
		t.Fatalf("served bodies that read a byte budget and never measure against it: %v", found.unmeasured)
	}
}

// The census is red for a builder that reads the budget and serves a body it
// never measures, for one that measures and drops the result, and for one that
// measures but never compares with the budget; it is green once the size is
// compared with the budget.
func TestBudgetMeasurementCensusFlagsABuilderThatDoesNotCompare(t *testing.T) {
	const template = `package api
func plantedHandler(request Request) {
	limit := request.Options.MaxSerializedBytes
	%s
	writeEncodedJSON(nil, 200, nil)
}`
	for name, body := range map[string]string{
		"unmeasured":             `_ = limit`,
		"result discarded":       `_, _, _ = marshalContextFabricResponse(limit)`,
		"measured not compared":  `_, size, _ := marshalContextFabricResponse(limit); _ = size`,
		"compared to a constant": `_, size, _ := marshalContextFabricResponse(limit); if size > 10 { _ = limit }`,
	} {
		source := strings.Replace(template, "%s", body, 1)
		if got := scanBudgetedBuilders(parseSnippet(t, source)).unmeasured; !slices.Equal(got, []string{"plantedHandler"}) {
			t.Errorf("%s: unmeasured = %v, want [plantedHandler]", name, got)
		}
	}
	green := strings.Replace(template, "%s", `_, size, _ := marshalContextFabricResponse(nil); if size > limit { return }`, 1)
	if got := scanBudgetedBuilders(parseSnippet(t, green)).unmeasured; len(got) != 0 {
		t.Fatalf("unmeasured = %v, want none for a builder that compares the size with the budget", got)
	}
}
