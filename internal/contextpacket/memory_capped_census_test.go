package contextpacket

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

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The only files allowed to open a ClickHouse connection. Each must hand the
// raw client onward only through NewMemoryCappedQueryClient (or use it for
// Ping/Close), so no statement reaches ClickHouse without max_memory_usage.
var clickHouseConstructorAllowlist = map[string]bool{
	"internal/runtime/hosted/clickhouse.go": true,
	"cmd/acr-projector/runtime.go":          true,
}

func isCall(n ast.Node, name string) (*ast.CallExpr, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return call, fn.Sel.Name == name
	case *ast.Ident:
		return call, fn.Name == name
	}
	return nil, false
}

func TestEveryClickHouseConnectionIsMemoryCapped(t *testing.T) {
	t.Parallel()
	root := repopath.Root(t)
	var violations []string
	opened := map[string]bool{}
	querySites := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "tests" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "clickhouse") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		clientVars := map[*ast.Object]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok && len(assign.Rhs) == 1 {
				if _, ok := isCall(assign.Rhs[0], "NewClickHouseQueryClientWithOptions"); ok {
					if id, ok := assign.Lhs[0].(*ast.Ident); ok && id.Obj != nil {
						clientVars[id.Obj] = true
					}
				}
			}
			if call, ok := isCall(n, "Query"); ok && len(call.Args) == 3 {
				querySites++
			}
			if _, ok := isCall(n, "NewClickHouseQueryClientWithOptions"); ok {
				opened[rel] = true
				if !clickHouseConstructorAllowlist[rel] {
					violations = append(violations, rel+": opens a ClickHouse client outside the capped constructors")
				}
			}
			return true
		})
		if len(clientVars) == 0 {
			return nil
		}
		allowedUse := map[ast.Node]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && clientVars[id.Obj] && (sel.Sel.Name == "Ping" || sel.Sel.Name == "Close") {
					allowedUse[id] = true
				}
			}
			if call, ok := isCall(n, "NewMemoryCappedQueryClient"); ok && len(call.Args) > 0 {
				if id, ok := call.Args[0].(*ast.Ident); ok && clientVars[id.Obj] {
					allowedUse[id] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Obj != nil && clientVars[id.Obj] && !allowedUse[id] {
				if assign, ok := id.Obj.Decl.(*ast.AssignStmt); ok && len(assign.Lhs) > 0 && assign.Lhs[0] == id {
					return true
				}
				violations = append(violations, rel+": raw ClickHouse client "+id.Name+" used outside Ping/Close/NewMemoryCappedQueryClient")
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ClickHouse Query call sites reached through the capped constructors: %d", querySites)
	for rel := range clickHouseConstructorAllowlist {
		if !opened[rel] {
			violations = append(violations, rel+": allowlisted constructor no longer opens a client; census is stale")
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%d uncapped ClickHouse paths:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}
