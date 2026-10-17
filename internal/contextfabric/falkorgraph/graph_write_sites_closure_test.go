package falkorgraph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// graphWriteFunctions is every function that sends a write query
// (GRAPH.QUERY) to the graph. All of them run only under the projector:
// ApplyProjectionBatch, the index bootstrap it calls, and the vector index
// maintenance. No request path of acr-api reaches one, which is why a purged
// organization's graph cannot be recreated by a request. A new write site
// fails this test until it is classified here.
var graphWriteFunctions = []string{
	"applyTombstone", "clearNodeVectors", "createFulltextIndex", "createIndex", "createVectorIndexWithOptions", "deleteOutrankedLinks",
	"dropVectorIndex", "projectContent", "projectEntity", "projectEpisode", "projectRelationship",
	"retractOpenEdges", "writeNodeVector", "writeWatermark",
}

func TestEveryGraphWriteSiteIsAProjectorFunction(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]bool{}
	queries := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "query" || len(call.Args) != 5 {
					return true
				}
				queries++
				switch mode := call.Args[4].(type) {
				case *ast.Ident:
					switch mode.Name {
					case "true":
					case "false":
						writers[function.Name.Name] = true
					default:
						if function.Name.Name != "query" {
							t.Errorf("%s: %s passes a computed read-only flag (%s); only a pass-through wrapper named query may", name, function.Name.Name, mode.Name)
						}
					}
				default:
					t.Errorf("%s: %s passes a read-only flag this test cannot read", name, function.Name.Name)
				}
				return true
			})
		}
	}
	if queries < 20 {
		t.Fatalf("found only %d graph query calls; the scan did not read the package", queries)
	}
	found := make([]string, 0, len(writers))
	for name := range writers {
		found = append(found, name)
	}
	sort.Strings(found)
	if strings.Join(found, ",") != strings.Join(graphWriteFunctions, ",") {
		t.Fatalf("graph write functions = %v, want %v", found, graphWriteFunctions)
	}
}
