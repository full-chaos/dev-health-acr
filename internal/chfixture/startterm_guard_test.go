package chfixture

import (
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

// directStartFiles may name the testcontainers start APIs: the one helper that terminates a failed start, and
// the FalkorDB fixture that hands testcontainers.GenericContainer to its own terminate wrapper.
var directStartFiles = map[string]bool{
	starttermFile: true,
	"internal/chfixture/falkorfixture/falkorfixture.go":           true,
	"internal/chfixture/falkorfixture/falkorfixture_live_test.go": true,
}

// directStartRefs returns the position of every reference (call or function value) to a testcontainers start
// API that leaks the container of a failed start, and how many startterm.Generic / startterm.PostgresRun uses
// the file has.
func directStartRefs(fset *token.FileSet, filename string, src []byte) (direct []string, helper int, err error) {
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, 0, err
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
		names[local] = path
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		p := names[id.Name]
		pos := filename + ":" + strconv.Itoa(fset.Position(sel.Pos()).Line)
		switch {
		case p == starttermPath && (sel.Sel.Name == "Generic" || sel.Sel.Name == "PostgresRun"):
			helper++
		case p == tcRoot && (sel.Sel.Name == "GenericContainer" || sel.Sel.Name == "Run"),
			strings.HasPrefix(p, tcModulePrefix) && (sel.Sel.Name == "Run" || sel.Sel.Name == "RunContainer"):
			if !directStartFiles[filename] {
				direct = append(direct, pos+" "+filepath.Base(p)+"."+sel.Sel.Name)
			}
		}
		return true
	})
	return direct, helper, nil
}

func TestEveryContainerStartTerminatesAFailedStart(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var bad []string
	files, helper := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "node_modules", "worktrees":
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
		b, h, err := directStartRefs(fset, filepath.ToSlash(rel), src)
		if err != nil {
			return err
		}
		files++
		bad, helper = append(bad, b...), helper+h
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("%d test container start(s) bypass startterm, so a container whose start fails is never terminated; use startterm.Generic or startterm.PostgresRun:\n%s", len(bad), strings.Join(bad, "\n"))
	}
	if files < 100 || helper < 46 {
		t.Fatalf("the scan did not measure what it claims: files=%d startterm uses=%d", files, helper)
	}
}

func TestDirectStartScanFlagsEveryBypassShape(t *testing.T) {
	src := []byte(`package x
import (
	"github.com/testcontainers/testcontainers-go"
	pg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
)
var a = testcontainers.GenericContainer
func f() {
	_, _ = testcontainers.GenericContainer(nil, testcontainers.GenericContainerRequest{})
	_, _ = testcontainers.Run(nil, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44")
	_, _ = pg.Run(nil, "img")
	_, _ = pg.RunContainer(nil)
	_, _ = startterm.Generic(nil, testcontainers.GenericContainerRequest{})
	_, _ = startterm.PostgresRun(nil, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44")
}
`)
	bad, helper, err := directStartRefs(token.NewFileSet(), "x_test.go", src)
	if err != nil || len(bad) != 5 || helper != 2 {
		t.Fatalf("scan must flag the five bypasses and count two helper uses: bad=%v helper=%d err=%v", bad, helper, err)
	}
	bad, _, _ = directStartRefs(token.NewFileSet(), starttermFile, src)
	if len(bad) != 0 {
		t.Fatalf("the helper file is exempt: %v", bad)
	}
}
