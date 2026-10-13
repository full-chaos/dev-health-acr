package contextpacket

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// Files that open the raw driver (clickhouse-go Open) directly. Each must put
// APIStatementMaxMemoryUsage on its statements as max_memory_usage.
var rawDriverOpenAllowlist = map[string]bool{
	"internal/contextfabric/factoracle/capture.go": true,
}

func importsClickHouseDriver(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != "github.com/ClickHouse/clickhouse-go/v2" {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, true
		}
		return "clickhouse", true
	}
	return "", false
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
	scanned := 0
	rawOpened := map[string]bool{}
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
		scanned++
		if !strings.Contains(string(src), "clickhouse") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		if driverName, ok := importsClickHouseDriver(file); ok && !strings.HasPrefix(rel, "internal/testsupport/") {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := isCall(n, "Open")
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == driverName {
						rawOpened[rel] = true
						if !rawDriverOpenAllowlist[rel] {
							violations = append(violations, rel+": opens the raw ClickHouse driver outside the capped constructors")
						} else if !strings.Contains(string(src), "APIStatementMaxMemoryUsage") || !strings.Contains(string(src), `"max_memory_usage"`) {
							violations = append(violations, rel+": opens the raw ClickHouse driver without sending max_memory_usage = APIStatementMaxMemoryUsage")
						}
					}
				}
				return true
			})
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
	if scanned == 0 {
		violations = append(violations, "the census scanned no non-test Go file; the repository root is wrong")
	}
	for rel := range rawDriverOpenAllowlist {
		if !rawOpened[rel] {
			violations = append(violations, rel+": allowlisted raw-driver file no longer opens the driver; census is stale")
		}
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

// Every memory limit that is a literal (not an alias of another limit) must
// cite the measurement it was set from, so a limit cannot grow without one.
func TestEveryMemoryLimitConstantCitesAMeasurement(t *testing.T) {
	t.Parallel()
	root := repopath.Root(t)
	tag := regexp.MustCompile(`memory-limit-measured: [0-9.]+ (MiB|MB) 20[0-9]{2}-[0-9]{2}-[0-9]{2} \S+`)
	var violations []string
	found := 0
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
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "MaxMemoryUsage") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ParseComments)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.GenDecl:
				if node.Tok != token.CONST {
					return true
				}
				for _, spec := range node.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if !strings.HasSuffix(name.Name, "MaxMemoryUsage") || i >= len(vs.Values) || !literalLimit(vs.Values[i]) {
							continue
						}
						found++
						text := ""
						for _, group := range []*ast.CommentGroup{node.Doc, vs.Doc, vs.Comment} {
							if group != nil {
								text += group.Text()
							}
						}
						if !tag.MatchString(text) {
							violations = append(violations, filepath.ToSlash(rel)+": "+name.Name+" has no `memory-limit-measured: <n> MiB|MB <date> <source>` comment")
						}
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := node.Key.(*ast.Ident); ok && id.Name == "MaxMemoryUsage" && literalLimit(node.Value) {
					violations = append(violations, filepath.ToSlash(rel)+": MaxMemoryUsage set from a literal; name it as a constant with a measurement")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 4 {
		t.Fatalf("census found %d literal memory limits, want at least 4 (stale scan?)", found)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("%d unmeasured memory limits:\n%s", len(violations), strings.Join(violations, "\n"))
	}
}

func literalLimit(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr:
		return literalLimit(e.X) && literalLimit(e.Y)
	case *ast.ParenExpr:
		return literalLimit(e.X)
	case *ast.CallExpr:
		return len(e.Args) == 1 && literalLimit(e.Args[0])
	}
	return false
}
