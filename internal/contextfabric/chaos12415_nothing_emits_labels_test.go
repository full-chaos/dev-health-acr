package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// CHAOS-12415: the result root is additionalProperties:false and ask-dev and
// web validate every answer, so a build that emits vocabulary_labels before
// their pins carry the field fails EVERY answer. Nothing may emit it until
// CHAOS-12416, which lifts this test in the change that composes the map.
//
// This reads all production Go in the module, because the producer could land
// anywhere that holds a result: the failure it guards is one assignment. It
// counts what it read, so a walk that finds nothing fails instead of passing.
func TestNoProductionCodeSetsVocabularyLabels(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	const declaringFields = 2 // the result and the projection
	files, declared := 0, 0
	var writes []string
	fileSet := token.NewFileSet()
	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			ast.Inspect(parsed, func(node ast.Node) bool {
				switch current := node.(type) {
				case *ast.Field:
					for _, name := range current.Names {
						if name.Name == "VocabularyLabels" {
							declared++
						}
					}
				case *ast.KeyValueExpr:
					if key, isIdent := current.Key.(*ast.Ident); isIdent && key.Name == "VocabularyLabels" {
						writes = append(writes, fileSet.Position(key.Pos()).String())
					}
				case *ast.AssignStmt:
					for _, left := range current.Lhs {
						if writesVocabularyLabels(left) {
							writes = append(writes, fileSet.Position(left.Pos()).String())
						}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 500 {
		t.Fatalf("read only %d production files: this measured almost nothing", files)
	}
	if declared != declaringFields {
		t.Fatalf("found %d VocabularyLabels declarations, want %d: the field moved, so this scan no longer proves anything", declared, declaringFields)
	}
	if len(writes) != 0 {
		t.Fatalf("production code sets vocabulary_labels before the pin train:\n  %s", strings.Join(writes, "\n  "))
	}
}

// writesVocabularyLabels sees through an index, so r.VocabularyLabels[key] = v
// counts as a write too.
func writesVocabularyLabels(expression ast.Expr) bool {
	if index, isIndex := expression.(*ast.IndexExpr); isIndex {
		expression = index.X
	}
	selector, isSelector := expression.(*ast.SelectorExpr)
	return isSelector && selector.Sel.Name == "VocabularyLabels"
}
