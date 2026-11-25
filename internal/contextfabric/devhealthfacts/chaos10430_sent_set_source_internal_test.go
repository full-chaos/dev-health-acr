package devhealthfacts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// The set the team pull request reads join is built only from the link set
// statement's rows (phase A), never from caller input: in the package's own
// code a teamLinkedPullRequests value is made only by linkedPullRequestsOf
// (the empty value aside), linkedPullRequestsOf is called only by
// readTeamLinkedPullRequests over readTeamProjectLinkedSet's rows, and the
// set's fields are unexported, so no other package can make one.
func TestCHAOS10430TheSentSetIsBuiltOnlyFromLinkSetRows(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var literals, folds, reads []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					if id, ok := node.Type.(*ast.Ident); ok && id.Name == "teamLinkedPullRequests" && len(node.Elts) > 0 {
						literals = append(literals, fn.Name.Name)
					}
				case *ast.CallExpr:
					if id, ok := node.Fun.(*ast.Ident); ok {
						switch id.Name {
						case "linkedPullRequestsOf":
							folds = append(folds, fn.Name.Name)
						case "readTeamProjectLinkedSet":
							reads = append(reads, fn.Name.Name)
						}
					}
				}
				return true
			})
		}
	}
	if !slices.Equal(literals, []string{"linkedPullRequestsOf"}) {
		t.Errorf("a non-empty teamLinkedPullRequests is built in %v, want only linkedPullRequestsOf", literals)
	}
	if !slices.Equal(folds, []string{"readTeamLinkedPullRequests"}) {
		t.Errorf("linkedPullRequestsOf is called from %v, want only readTeamLinkedPullRequests", folds)
	}
	slices.Sort(reads)
	if !slices.Contains(reads, "readTeamLinkedPullRequests") {
		t.Errorf("readTeamLinkedPullRequests does not read readTeamProjectLinkedSet (callers %v)", reads)
	}
}
