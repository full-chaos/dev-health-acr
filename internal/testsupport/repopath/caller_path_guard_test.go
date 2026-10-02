package repopath

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Files allowed to call runtime.Caller or runtime.Callers: they read a
// function name from the stack, never a file path.
var callerAllowList = map[string]string{
	"internal/contextfabric/engine_test.go": "reads stack function names only",
}

func TestNoCallerDerivedPathsInGoSources(t *testing.T) {
	root := Root(t)
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "third_party", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := callerAllowList[rel]; ok {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", rel, err)
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "runtime" && (sel.Sel.Name == "Caller" || sel.Sel.Name == "Callers") {
				found = append(found, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	for _, rel := range found {
		t.Errorf("%s uses runtime.Caller/Callers; under -trimpath it yields module-relative paths. Use repopath.Path(t, ...) or add a reasoned entry to callerAllowList", rel)
	}
}
