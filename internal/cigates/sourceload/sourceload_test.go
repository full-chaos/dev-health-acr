package sourceload

import (
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

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
