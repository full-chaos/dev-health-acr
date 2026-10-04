package devhealthfacts_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// queryDeadlineReads are the wall-clock reads that only size a query timeout
// from the context deadline; their value reaches no fact.
var queryDeadlineReads = map[string]string{
	"work_item_membership.go": "Until",
	"workitem_scope.go":       "Until",
}

func TestProducersReadTheWallClockOnlyThroughTheirClock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	set := token.NewFileSet()
	parsed := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(set, name, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed++
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "time" {
				return true
			}
			switch selector.Sel.Name {
			case "Now", "Since", "Until":
			default:
				return true
			}
			if name == "clock.go" && selector.Sel.Name == "Now" {
				return true
			}
			if queryDeadlineReads[name] == selector.Sel.Name {
				return true
			}
			t.Errorf("%s: time.%s reads the wall clock outside clock(); a fact built from it moves the client synthesis input on every call", set.Position(selector.Pos()), selector.Sel.Name)
			return true
		})
	}
	if parsed < 10 {
		t.Fatalf("parsed %d production files, want the whole package", parsed)
	}
}
