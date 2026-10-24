package sourceload

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const viewBase = "github.com/full-chaos/dev-health-acr/internal/cigates/sourceload/testdata/packagesview/"

func loadView(t *testing.T, pattern string) []*packages.Package {
	t.Helper()
	loader, err := Load(repopath.Root(t), []string{viewBase + pattern}, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	return loader.Packages()
}

func TestPackagesCarryTheFieldsAnAnalysisReads(t *testing.T) {
	pkgs := loadView(t, "good")
	if len(pkgs) != 1 {
		t.Fatalf("loaded %d packages, want 1", len(pkgs))
	}
	root := pkgs[0]
	if root.Fset == nil || len(root.Syntax) != 1 || root.Types == nil || root.TypesInfo == nil || root.TypesSizes == nil {
		t.Fatalf("root is missing a field: fset=%v syntax=%d types=%v info=%v sizes=%v",
			root.Fset != nil, len(root.Syntax), root.Types != nil, root.TypesInfo != nil, root.TypesSizes != nil)
	}
	if len(root.Errors) != 0 || root.IllTyped {
		t.Fatalf("a clean root reported errors=%v illTyped=%v", root.Errors, root.IllTyped)
	}
	if len(root.TypesInfo.Uses) == 0 || len(root.TypesInfo.Defs) == 0 {
		t.Fatal("the root's type info is empty: an analysis would read nothing")
	}
	dep := root.Imports[viewBase+"gooddep"]
	if dep == nil || dep.Types == nil {
		t.Fatalf("the dependency has no types: %v", dep)
	}
	if dep.Types.Scope().Lookup("Box") == nil {
		t.Fatal("the dependency's declared type Box is not in its scope")
	}
	if dep.TypesInfo != nil || len(dep.Syntax) != 0 {
		t.Fatal("a dependency carries syntax or type info, so function bodies are being checked and the load is paying for them")
	}
	if unsafeDep := dep.Imports["unsafe"]; unsafeDep == nil || unsafeDep.Types == nil {
		t.Fatalf("the unsafe package has no types, so a program built over the closure would skip it: %v", unsafeDep)
	}
}

func TestPackagesReportARootsOwnTypeError(t *testing.T) {
	pkgs := loadView(t, "badroot")
	root := pkgs[0]
	if len(root.Errors) == 0 || !root.IllTyped {
		t.Fatalf("a root with a type error reported errors=%v illTyped=%v", root.Errors, root.IllTyped)
	}
	if root.Errors[0].Kind != packages.TypeError {
		t.Errorf("error kind = %v, want TypeError", root.Errors[0].Kind)
	}
}

func TestPackagesReportABrokenDependencyDeclaration(t *testing.T) {
	pkgs := loadView(t, "use")
	dep := pkgs[0].Imports[viewBase+"brokendep"]
	if dep == nil {
		t.Fatal("dependency missing from Imports")
	}
	var msgs []string
	for _, e := range dep.Errors {
		msgs = append(msgs, e.Msg)
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "not an int") && !strings.Contains(strings.Join(msgs, "\n"), "cannot use") {
		t.Fatalf("the dependency's declaration error is not on the dependency: %v", msgs)
	}
	if len(dep.Errors) != 1 {
		t.Errorf("dependency function bodies are checked: got %d errors, want only the declaration error: %v", len(dep.Errors), msgs)
	}
}

func TestPackagesIsStableAcrossCalls(t *testing.T) {
	loader, err := Load(repopath.Root(t), []string{viewBase + "badroot"}, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	first := len(loader.Packages()[0].Errors)
	second := len(loader.Packages()[0].Errors)
	if first == 0 || first != second {
		t.Fatalf("errors after first call = %d, after second = %d", first, second)
	}
}

func TestPackagesIllTypedFollowsImports(t *testing.T) {
	root := loadView(t, "use")[0]
	if len(root.Errors) != 0 {
		t.Fatalf("the root has no error of its own, got %v", root.Errors)
	}
	if !root.IllTyped {
		t.Fatal("a root that imports an ill-typed package must be ill-typed, as in go/packages: SSA would otherwise build it")
	}
	if good := loadView(t, "good")[0]; good.IllTyped {
		t.Fatal("a root over well-typed imports must not be ill-typed")
	}
}

func TestPackagesParseErrorsKeepTheirPositions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module parsebadmod\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte("package p\n\nfunc Broken( {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loader, err := Load(dir, []string{"parsebadmod"}, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	root := loader.Packages()[0]
	if len(root.Errors) < 2 {
		t.Fatalf("a root with two syntax errors reported %d: the error list was collapsed", len(root.Errors))
	}
	for _, e := range root.Errors {
		if e.Kind != packages.ParseError {
			t.Errorf("kind = %v, want ParseError", e.Kind)
		}
		if !strings.HasSuffix(e.Pos, "p.go:3:14") && !strings.HasSuffix(e.Pos, "p.go:4:1") {
			t.Errorf("position = %q, want the file, line and column of a syntax error", e.Pos)
		}
		if strings.Contains(e.Msg, "p.go") {
			t.Errorf("message %q repeats the position, so PrintErrors would print it twice", e.Msg)
		}
	}
}

func TestPackagesFeedSSAExactlyWhenWellTyped(t *testing.T) {
	good := loadView(t, "good")
	prog, ssaPkgs := ssautil.Packages(good, 0)
	if len(ssaPkgs) != 1 || ssaPkgs[0] == nil {
		t.Fatalf("a well-typed root was not admitted to SSA: %v", ssaPkgs)
	}
	prog.Build()
	if ssaPkgs[0].Func("Read") == nil || len(ssaPkgs[0].Func("Read").Blocks) == 0 {
		t.Fatal("the root's function body was not built")
	}
	_, broken := ssautil.Packages(loadView(t, "use"), 0)
	if len(broken) != 1 || broken[0] != nil {
		t.Fatalf("a root over an ill-typed import was admitted to SSA: %v", broken)
	}
}
