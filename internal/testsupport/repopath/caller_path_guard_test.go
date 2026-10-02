package repopath

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
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
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if callerUsed(t, rel, src) {
			found = append(found, rel)
		}
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

func callerUsed(t *testing.T, name string, src []byte) bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Errorf("parse %s: %v", name, err)
		return false
	}
	local, dot := "", false
	for _, imp := range file.Imports {
		if imp.Path.Value != `"runtime"` {
			continue
		}
		switch {
		case imp.Name == nil:
			local = "runtime"
		case imp.Name.Name == ".":
			dot = true
		default:
			local = imp.Name.Name
		}
	}
	isCaller := func(name string) bool { return name == "Caller" || name == "Callers" }
	used := false
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := x.X.(*ast.Ident); ok && local != "" && pkg.Name == local && isCaller(x.Sel.Name) {
				used = true
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && dot && isCaller(id.Name) {
				used = true
			}
		}
		return !used
	})
	return used
}

func TestCallerUsedSeesAliasAndDotImports(t *testing.T) {
	cases := map[string]struct {
		src  string
		want bool
	}{
		"plain":   {"package p\nimport \"runtime\"\nvar _, f, _, _ = runtime.Caller(0)\n", true},
		"callers": {"package p\nimport \"runtime\"\nvar _ = runtime.Callers(0, nil)\n", true},
		"alias":   {"package p\nimport rt \"runtime\"\nvar _, f, _, _ = rt.Caller(0)\n", true},
		"dot":     {"package p\nimport . \"runtime\"\nvar _, f, _, _ = Caller(0)\n", true},
		"other":   {"package p\nimport \"runtime\"\nvar _ = runtime.NumCPU()\n", false},
		"shadow":  {"package p\nimport \"os\"\nvar runtime struct{ Caller func() }\nvar _ = os.Args\nfunc f() { runtime.Caller() }\n", false},
	}
	for name, c := range cases {
		if got := callerUsed(t, name, []byte(c.src)); got != c.want {
			t.Errorf("%s: callerUsed = %v, want %v", name, got, c.want)
		}
	}
}
