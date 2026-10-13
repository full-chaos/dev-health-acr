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
	budgetMeasurers    = []string{"marshalContextFabricResponse", "encodeBounded", "packetWithinRequestLimits", "CompleteUsageWithBudget"}
)

type budgetedBuilders struct {
	scanned, unmeasured []string
}

// scanBudgetedBuilders finds the functions that serve a body and read a
// declared byte budget, and which of them call none of the measurers.
func scanBudgetedBuilders(files []*ast.File) budgetedBuilders {
	var found budgetedBuilders
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			serves, reads, measures := false, false, false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.CallExpr:
					name := ""
					switch f := n.Fun.(type) {
					case *ast.Ident:
						name = f.Name
					case *ast.SelectorExpr:
						name = f.Sel.Name
					}
					serves = serves || slices.Contains(servedBodyWriters, name)
					measures = measures || slices.Contains(budgetMeasurers, name)
				case *ast.SelectorExpr:
					reads = reads || slices.Contains(declaredBudgetKeys, n.Sel.Name)
				}
				return true
			})
			if serves && reads {
				found.scanned = append(found.scanned, fn.Name.Name)
				if !measures {
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
// never measures, and green for the same builder once it measures.
func TestBudgetMeasurementCensusFlagsAnUnmeasuredBuilder(t *testing.T) {
	const unmeasured = `package api
func plantedHandler(request Request) {
	limit := request.Options.MaxSerializedBytes
	_ = limit
	writeEncodedJSON(nil, 200, nil)
}`
	if got := scanBudgetedBuilders(parseSnippet(t, unmeasured)).unmeasured; !slices.Equal(got, []string{"plantedHandler"}) {
		t.Fatalf("unmeasured = %v, want [plantedHandler]", got)
	}
	measured := strings.Replace(unmeasured, "_ = limit", "_, _, _ = marshalContextFabricResponse(limit)", 1)
	if got := scanBudgetedBuilders(parseSnippet(t, measured)).unmeasured; len(got) != 0 {
		t.Fatalf("unmeasured = %v, want none for a builder that measures", got)
	}
}
