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

func repoRootFromHere(t *testing.T) string {
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
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// falkorWaitViolations returns the file:line of every bare ForListeningPort("6379/tcp") wait, every direct
// GenericContainer start and every FalkorReady use in a source that names a FalkorDB image, and how many calls
// go through falkorfixture.Start, which terminates a container whose start fails.
func falkorWaitViolations(fset *token.FileSet, filename string, src []byte) (bad []string, ready int, falkor bool, err error) {
	if !strings.Contains(strings.ToLower(string(src)), "falkordb") {
		return nil, 0, false, nil
	}
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, 0, true, err
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Start":
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "falkorfixture" {
				ready++
			}
		case "FalkorReady":
			bad = append(bad, filename+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" FalkorReady")
		case "GenericContainer":
			if namesRedisPort(call) {
				bad = append(bad, filename+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" GenericContainer")
			}
		case "ForListeningPort":
			if len(call.Args) == 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, _ := strconv.Unquote(lit.Value); s == "6379/tcp" {
						bad = append(bad, filename+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
					}
				}
			}
		}
		return true
	})
	return bad, ready, true, nil
}

func namesRedisPort(call *ast.CallExpr) bool {
	found := false
	for _, arg := range call.Args {
		ast.Inspect(arg, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && s == "6379/tcp" {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

func TestEveryFalkorDBTestContainerWaitsForAServingServer(t *testing.T) {
	root := repoRootFromHere(t)
	fset := token.NewFileSet()
	var bad []string
	scanned, falkorFiles, ready := 0, 0, 0
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "worktrees", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, filepath.Join(root, "internal", "chfixture")+string(filepath.Separator)) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		b, r, isFalkor, err := falkorWaitViolations(fset, rel, src)
		if err != nil {
			return err
		}
		if isFalkor {
			falkorFiles++
		}
		bad, ready = append(bad, b...), ready+r
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("%d FalkorDB test container(s) start without falkorfixture.Start (bare listening-port wait, direct GenericContainer or FalkorReady); use falkorfixture.Start, which also terminates a container whose start fails:\n%s", len(bad), strings.Join(bad, "\n"))
	}
	if scanned < 100 || falkorFiles < 10 || ready < 15 {
		t.Fatalf("the scan did not measure what it claims: scanned=%d falkorFiles=%d falkorfixture.Start uses=%d", scanned, falkorFiles, ready)
	}
}

func TestFalkorWaitScanFlagsABareListeningPortWait(t *testing.T) {
	src := []byte("package x\nvar _ = wait.ForListeningPort(\"6379/tcp\")\nvar _ = chfixture.FalkorReady()\nvar _, _ = testcontainers.GenericContainer(ctx, r{ExposedPorts: []string{\"6379/tcp\"}})\nvar _, _ = testcontainers.GenericContainer(ctx, r{ExposedPorts: []string{\"9000/tcp\"}})\nvar _ = falkorfixture.Start(ctx, t, image)\nconst image = \"falkordb/falkordb\"\n")
	bad, ready, falkor, err := falkorWaitViolations(token.NewFileSet(), "x_test.go", src)
	if err != nil || !falkor || ready != 1 || len(bad) != 3 {
		t.Fatalf("scan must flag the planted wait: bad=%v ready=%d falkor=%v err=%v", bad, ready, falkor, err)
	}
}
