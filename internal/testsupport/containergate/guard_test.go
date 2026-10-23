package containergate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	gatePath       = "github.com/full-chaos/dev-health-acr/internal/testsupport/containergate"
	tcRoot         = "github.com/testcontainers/testcontainers-go"
	tcModulePrefix = tcRoot + "/modules/"
	tcNetwork      = tcRoot + "/network"
)

var knownTestcontainersImports = map[string]bool{
	tcRoot:                       true,
	tcRoot + "/wait":             true,
	tcRoot + "/modules/postgres": true,
	tcNetwork:                    true,
}

var rootStartNames = map[string]bool{
	"GenericContainer": true, "Run": true, "NewDockerProvider": true,
	"NewDockerClientWithOpts": true, "NewDockerClient": true,
}

var dockerBinaries = map[string]bool{"docker": true, "podman": true, "nerdctl": true, "docker-compose": true}

var dockerVerbsThatStartNothing = map[string]bool{
	"stats": true, "exec": true, "inspect": true, "logs": true, "ps": true, "port": true,
	"cp": true, "kill": true, "stop": true, "rm": true, "top": true, "wait": true,
}

type violation struct{ rule, pos, msg string }

func (v violation) String() string { return fmt.Sprintf("%s %s %s", v.rule, v.pos, v.msg) }

type startRef struct {
	node ast.Node
	what string
}

// checkSource applies rules G1-G5 to one Go source file.
func checkSource(fset *token.FileSet, filename string, src []byte) ([]violation, int, int, error) {
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, 0, 0, err
	}
	var out []violation
	at := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return fmt.Sprintf("%s:%d", filename, p.Line)
	}
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		local := filepath.Base(path)
		if path == tcRoot {
			local = "testcontainers"
		}
		if spec.Name != nil {
			local = spec.Name.Name
		}
		if strings.HasPrefix(path, "github.com/testcontainers/") {
			if spec.Name != nil && spec.Name.Name == "." {
				out = append(out, violation{"G4", at(spec), "dot-import of " + path})
			}
			if !knownTestcontainersImports[path] {
				out = append(out, violation{"G4", at(spec), "unknown testcontainers import " + path + "; extend the guard before using it"})
			}
		}
		names[local] = path
	}
	pathOf := func(x ast.Expr) string {
		id, ok := x.(*ast.Ident)
		if !ok {
			return ""
		}
		return names[id.Name]
	}
	isRequire := func(stmt ast.Stmt) bool {
		es, ok := stmt.(*ast.ExprStmt)
		if !ok {
			return false
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Require" && pathOf(sel.X) == gatePath
	}
	strLit := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	startOf := func(n ast.Node) (string, bool) {
		sel, ok := n.(*ast.SelectorExpr)
		if ok {
			p := pathOf(sel.X)
			switch {
			case p == tcRoot && rootStartNames[sel.Sel.Name]:
				return "testcontainers." + sel.Sel.Name, true
			case strings.HasPrefix(p, tcModulePrefix) && (sel.Sel.Name == "Run" || sel.Sel.Name == "RunContainer"):
				return path0(p) + "." + sel.Sel.Name, true
			case p == tcNetwork && (sel.Sel.Name == "New" || sel.Sel.Name == "NewNetwork"):
				return "network." + sel.Sel.Name, true
			}
			return "", false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return "", false
		}
		fn, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || pathOf(fn.X) != "os/exec" || (fn.Sel.Name != "Command" && fn.Sel.Name != "CommandContext") {
			return "", false
		}
		args := call.Args
		if fn.Sel.Name == "CommandContext" && len(args) > 0 {
			args = args[1:]
		}
		if len(args) == 0 {
			return "", false
		}
		bin, ok := strLit(args[0])
		if !ok || !dockerBinaries[bin] {
			return "", false
		}
		for _, arg := range args[1:] {
			verb, lit := strLit(arg)
			if !lit {
				return "exec " + bin + " <non-literal argument>", true
			}
			if strings.HasPrefix(verb, "-") {
				continue
			}
			if dockerVerbsThatStartNothing[verb] {
				return "", false
			}
			return "exec " + bin + " " + verb, true
		}
		return "exec " + bin, true
	}

	refs, requires := 0, 0
	inFunc := map[ast.Node]bool{}
	var funcs []*ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
			funcs = append(funcs, fd)
			ast.Inspect(fd, func(n ast.Node) bool { inFunc[n] = true; return true })
		}
	}
	for _, fd := range funcs {
		requireIdx := -1
		for i, stmt := range fd.Body.List {
			if isRequire(stmt) {
				requireIdx = i
				requires++
				break
			}
		}
		for j, stmt := range fd.Body.List {
			ast.Inspect(stmt, func(n ast.Node) bool {
				what, ok := startOf(n)
				if !ok {
					return true
				}
				refs++
				if requireIdx < 0 || requireIdx >= j {
					out = append(out, violation{"G1", at(n), what + " in " + fd.Name.Name + " has no top-level containergate.Require before it"})
				}
				return true
			})
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil || inFunc[n] {
			return true
		}
		if what, ok := startOf(n); ok {
			refs++
			out = append(out, violation{"G2", at(n), what + " outside any function body"})
		}
		return true
	})
	gatedStarters := map[string]bool{}
	for _, fd := range funcs {
		for _, stmt := range fd.Body.List {
			if isRequire(stmt) {
				gatedStarters[fd.Name.Name] = true
			}
		}
	}
	for _, fd := range funcs {
		requireIdx := -1
		for i, stmt := range fd.Body.List {
			if isRequire(stmt) {
				requireIdx = i
				break
			}
		}
		for j, stmt := range fd.Body.List {
			ast.Inspect(stmt, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Do" || (requireIdx >= 0 && requireIdx < j) {
					return true
				}
				for _, arg := range call.Args {
					lit, ok := arg.(*ast.FuncLit)
					if !ok {
						continue
					}
					ast.Inspect(lit, func(m ast.Node) bool {
						inner, ok := m.(*ast.CallExpr)
						if !ok {
							return true
						}
						if id, ok := inner.Fun.(*ast.Ident); ok && gatedStarters[id.Name] {
							out = append(out, violation{"G3", at(n), ".Do in " + fd.Name.Name + " runs gated " + id.Name + " without a top-level containergate.Require before the .Do"})
						}
						return true
					})
				}
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, refs, requires, nil
}

func path0(importPath string) string { return filepath.Base(importPath) }

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func TestEveryContainerStartInTheModuleIsBehindRequire(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var violations []violation
	files, refs, requires := 0, 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		found, r, q, err := checkSource(fset, filepath.ToSlash(rel), src)
		if err != nil {
			return fmt.Errorf("G0 %s: cannot parse: %w", rel, err)
		}
		files++
		refs += r
		requires += q
		violations = append(violations, found...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 100 || refs == 0 || requires == 0 {
		t.Errorf("G0 the walk saw %d files, %d container start references, %d Require calls; a vacuous walk proves nothing", files, refs, requires)
	}
	for _, v := range violations {
		t.Error(v.String())
	}
}

const gatedOK = `package p
import (
	"testing"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/containergate"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)
func start(t *testing.T) {
	t.Helper()
	containergate.Require(t)
	_, _ = testcontainers.GenericContainer(nil, testcontainers.GenericContainerRequest{})
	_, _ = tcpostgres.Run(nil, "img")
}
`

func TestGuardRulesFireOnSyntheticSources(t *testing.T) {
	cases := []struct {
		name, rule, src string
	}{
		{"gated source is clean", "", gatedOK},
		{"the startScopeClickHouse shape without Require", "G1", strings.Replace(gatedOK, "\tcontainergate.Require(t)\n", "", 1)},
		{"Require after the start", "G1", strings.Replace(strings.Replace(gatedOK, "\tcontainergate.Require(t)\n", "", 1), "\t_, _ = tcpostgres.Run(nil, \"img\")\n", "\t_, _ = tcpostgres.Run(nil, \"img\")\n\tcontainergate.Require(t)\n", 1)},
		{"Require inside an if", "G1", strings.Replace(gatedOK, "\tcontainergate.Require(t)\n", "\tif t != nil {\n\t\tcontainergate.Require(t)\n\t}\n", 1)},
		{"start taken as a value", "G1", strings.Replace(gatedOK, "\tcontainergate.Require(t)\n", "\tf := testcontainers.GenericContainer\n\t_ = f\n\tcontainergate.Require(t)\n", 1)},
		{"aliased module import", "G1", "package p\nimport pg \"github.com/testcontainers/testcontainers-go/modules/postgres\"\nfunc s() { _, _ = pg.Run(nil, \"img\") }\n"},
		{"network start", "G1", "package p\nimport \"github.com/testcontainers/testcontainers-go/network\"\nfunc s() { _, _ = network.New(nil) }\n"},
		{"package var initialiser", "G2", "package p\nimport \"github.com/testcontainers/testcontainers-go\"\nvar x = testcontainers.GenericContainer\n"},
		{"Once accessor with Require first is clean", "", gatedOK + "func accessor(t *testing.T, o interface{ Do(func()) }) {\n\tcontainergate.Require(t)\n\to.Do(func() { start(t) })\n}\n"},
		{"Once accessor without Require", "G3", gatedOK + "func accessor(t *testing.T, o interface{ Do(func()) }) {\n\to.Do(func() { start(t) })\n}\n"},
		{"unknown testcontainers module", "G4", "package p\nimport _ \"github.com/testcontainers/testcontainers-go/modules/compose\"\n"},
		{"dot import", "G4", "package p\nimport . \"github.com/testcontainers/testcontainers-go\"\n"},
		{"docker run", "G1", "package p\nimport \"os/exec\"\nfunc s() { _ = exec.Command(\"docker\", \"run\", \"img\") }\n"},
		{"docker compose via CommandContext", "G1", "package p\nimport \"os/exec\"\nfunc s() { _ = exec.CommandContext(nil, \"docker\", \"compose\", \"up\") }\n"},
		{"docker with a non-literal verb", "G1", "package p\nimport \"os/exec\"\nfunc s(v string) { _ = exec.Command(\"docker\", v) }\n"},
		{"docker stats starts nothing", "", "package p\nimport \"os/exec\"\nfunc s() { _ = exec.Command(\"docker\", \"stats\", \"x\") }\n"},
		{"docker exec starts nothing", "", "package p\nimport \"os/exec\"\nfunc s() { _ = exec.Command(\"docker\", \"exec\", \"-i\", \"x\") }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found, _, _, err := checkSource(token.NewFileSet(), "synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			if tc.rule == "" {
				if len(found) != 0 {
					t.Fatalf("expected no violation, got %v", found)
				}
				return
			}
			for _, v := range found {
				if v.rule == tc.rule {
					return
				}
			}
			t.Fatalf("expected a %s violation, got %v", tc.rule, found)
		})
	}
}
