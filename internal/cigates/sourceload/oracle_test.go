package sourceload

import (
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const modulePath = "github.com/full-chaos/dev-health-acr/"

// oldMode is the union of the modes the five consumer analyses load with before they move to Packages().
const oldMode = packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
	packages.NeedTypesInfo | packages.NeedTypesSizes | packages.NeedImports | packages.NeedDeps | packages.NeedModule

// compared is every exported packages.Package field the loader claims to fill, with the comparison that proves it.
// roots says the field is compared on root packages only.
type fieldCheck struct {
	roots   bool
	compare func(old, got *packages.Package) []string
}

var compared = map[string]fieldCheck{
	"ID":      {compare: func(o, g *packages.Package) []string { return diffString("ID", o.ID, g.ID) }},
	"Name":    {compare: func(o, g *packages.Package) []string { return diffString("Name", o.Name, g.Name) }},
	"PkgPath": {compare: func(o, g *packages.Package) []string { return diffString("PkgPath", o.PkgPath, g.PkgPath) }},
	"GoFiles": {compare: func(o, g *packages.Package) []string {
		return diffString("GoFiles", strings.Join(o.GoFiles, ","), strings.Join(g.GoFiles, ","))
	}},
	"Imports": {compare: func(o, g *packages.Package) []string {
		return diffString("Imports", importSet(o), importSet(g))
	}},
	"Module": {compare: func(o, g *packages.Package) []string {
		return diffString("Module", moduleString(o), moduleString(g))
	}},
	"Errors": {compare: func(o, g *packages.Package) []string {
		return diffString("Errors", errorSet(o), errorSet(g))
	}},
	"IllTyped": {compare: func(o, g *packages.Package) []string {
		return diffString("IllTyped", fmt.Sprint(o.IllTyped), fmt.Sprint(g.IllTyped))
	}},
	"Types": {compare: func(o, g *packages.Package) []string {
		return diffString("Types", typesSummary(o.Types), typesSummary(g.Types))
	}},
	"TypesSizes": {compare: func(o, g *packages.Package) []string {
		return diffString("TypesSizes", sizesSummary(o.TypesSizes), sizesSummary(g.TypesSizes))
	}},
	"Fset": {compare: func(o, g *packages.Package) []string {
		return diffString("Fset", fmt.Sprint(o.Fset != nil), fmt.Sprint(g.Fset != nil))
	}},
	"Syntax": {roots: true, compare: func(o, g *packages.Package) []string {
		return diffString("Syntax", syntaxSummary(o), syntaxSummary(g))
	}},
	"TypesInfo": {roots: true, compare: func(o, g *packages.Package) []string {
		return diffString("TypesInfo", infoSummary(o.TypesInfo), infoSummary(g.TypesInfo))
	}},
}

// notFilled is every exported field of packages.Package the loader does not fill, with the reason no consumer needs it.
var notFilled = map[string]string{
	"Dir":             "not read by any consumer",
	"TypeErrors":      "not read by any consumer; type errors are in Errors",
	"Target":          "not read by any consumer",
	"ForTest":         "tests are never loaded here",
	"CompiledGoFiles": "not read by any consumer; cgo-generated files are never compiled here",
	"OtherFiles":      "not read by any consumer",
	"EmbedFiles":      "not read by any consumer",
	"EmbedPatterns":   "not read by any consumer",
	"IgnoredFiles":    "not read by any consumer",
	"ExportFile":      "the loader never compiles, so there is no export data",
}

func diffString(field, old, got string) []string {
	if old == got {
		return nil
	}
	oldLines, gotLines := strings.FieldsFunc(old, func(r rune) bool { return r == '\n' || r == ',' }), strings.FieldsFunc(got, func(r rune) bool { return r == '\n' || r == ',' })
	inOld, inGot := map[string]bool{}, map[string]bool{}
	for _, l := range oldLines {
		inOld[l] = true
	}
	for _, l := range gotLines {
		inGot[l] = true
	}
	var onlyOld, onlyGot []string
	for _, l := range oldLines {
		if !inGot[l] && len(onlyOld) < 4 {
			onlyOld = append(onlyOld, l)
		}
	}
	for _, l := range gotLines {
		if !inOld[l] && len(onlyGot) < 4 {
			onlyGot = append(onlyGot, l)
		}
	}
	return []string{fmt.Sprintf("%s differs\n    only in packages.Load: %q\n    only in Packages():    %q", field, onlyOld, onlyGot)}
}

// isCgo reports whether the package imports the pseudo package "C": the source checker sees it through FakeImportC,
// where go/packages type-checks the cgo-generated files. The difference is confined to the unexported objects (cgo
// generates _C* and __cgofn_* names, and an unexported declaration that mentions a C type is invalid here), the pseudo
// imports "C" and runtime/cgo, and the runtime/cgo package itself. Exported objects of a cgo package are still compared
// in full. No first-party package uses cgo; this only concerns the standard library (net, os/user).
func isCgo(pkg *types.Package) bool {
	if pkg == nil {
		return false
	}
	for _, imp := range pkg.Imports() {
		if imp.Path() == "C" || imp.Path() == "runtime/cgo" {
			return true
		}
	}
	return false
}

func cgoNoise(path string) bool { return path == "C" || path == "runtime/cgo" }

func importSet(p *packages.Package) string {
	cgo := isCgo(p.Types)
	var out []string
	for path, imp := range p.Imports {
		if cgo && cgoNoise(path) {
			continue
		}
		out = append(out, path+"=>"+imp.PkgPath)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func moduleString(p *packages.Package) string {
	if p.Module == nil {
		return "<none>"
	}
	return p.Module.Path + "@" + p.Module.Version + " go" + p.Module.GoVersion
}

func errorSet(p *packages.Package) string {
	var out []string
	for _, e := range p.Errors {
		out = append(out, fmt.Sprintf("%v|%s|%s", e.Kind, e.Pos, e.Msg))
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func typesSummary(pkg *types.Package) string {
	if pkg == nil {
		return "<nil>"
	}
	var out []string
	cgo := isCgo(pkg)
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if cgo && !scope.Lookup(name).Exported() {
			continue
		}
		obj := scope.Lookup(name)
		line := fmt.Sprintf("%s %T %s", name, obj, obj.Type())
		if tn, ok := obj.(*types.TypeName); ok && !tn.IsAlias() {
			if _, isInterface := tn.Type().Underlying().(*types.Interface); !isInterface {
				set := types.NewMethodSet(types.NewPointer(tn.Type()))
				var methods []string
				for i := 0; i < set.Len(); i++ {
					methods = append(methods, set.At(i).Obj().Name()+set.At(i).Type().String())
				}
				sort.Strings(methods)
				line += " methods=" + strings.Join(methods, ";")
			}
		}
		out = append(out, line)
	}
	var imports []string
	for _, imp := range pkg.Imports() {
		if cgo && cgoNoise(imp.Path()) {
			continue
		}
		imports = append(imports, imp.Path())
	}
	sort.Strings(imports)
	return fmt.Sprintf("path=%s name=%s\nimports=%s\n%s", pkg.Path(), pkg.Name(), strings.Join(imports, ","), strings.Join(out, "\n"))
}

func sizesSummary(sizes types.Sizes) string {
	if sizes == nil {
		return "<nil>"
	}
	var out []string
	for _, t := range []types.Type{types.Typ[types.Int], types.Typ[types.Uintptr], types.Typ[types.Complex128], types.Typ[types.UnsafePointer], types.Typ[types.String]} {
		out = append(out, fmt.Sprintf("%s:%d/%d", t, sizes.Sizeof(t), sizes.Alignof(t)))
	}
	return strings.Join(out, ",")
}

func syntaxSummary(p *packages.Package) string {
	var out []string
	for _, f := range p.Syntax {
		out = append(out, fmt.Sprintf("%s:%d", filepath.Base(p.Fset.Position(f.Pos()).Filename), len(f.Decls)))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func infoSummary(info *types.Info) string {
	if info == nil {
		return "<nil>"
	}
	return fmt.Sprintf("types=%d defs=%d uses=%d implicits=%d selections=%d instances=%d",
		len(info.Types), len(info.Defs), len(info.Uses), len(info.Implicits), len(info.Selections), len(info.Instances))
}

// diffClosures compares the whole import closure of old and got, root fields on roots only.
func diffClosures(oldRoots, gotRoots []*packages.Package) []string {
	index := func(roots []*packages.Package) (map[string]*packages.Package, map[string]bool) {
		all, isRoot := map[string]*packages.Package{}, map[string]bool{}
		for _, r := range roots {
			isRoot[r.ID] = true
		}
		packages.Visit(roots, nil, func(p *packages.Package) { all[p.ID] = p })
		return all, isRoot
	}
	oldAll, oldRoot := index(oldRoots)
	gotAll, gotRoot := index(gotRoots)
	var diffs []string
	for id, o := range oldAll {
		g, ok := gotAll[id]
		if !ok && o.PkgPath == "runtime/cgo" {
			continue
		}
		if !ok {
			diffs = append(diffs, "package missing from Packages(): "+id)
			continue
		}
		if oldRoot[id] != gotRoot[id] {
			diffs = append(diffs, "root-ness differs for "+id)
		}
		names := make([]string, 0, len(compared))
		for name := range compared {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			check := compared[name]
			if check.roots && !oldRoot[id] {
				continue
			}
			for _, d := range check.compare(o, g) {
				diffs = append(diffs, id+": "+d)
			}
		}
	}
	for id := range gotAll {
		if _, ok := oldAll[id]; !ok {
			diffs = append(diffs, "package only in Packages(): "+id)
		}
	}
	sort.Strings(diffs)
	return diffs
}

func loadBoth(t *testing.T, dir string, patterns []string) (oldRoots, gotRoots []*packages.Package) {
	t.Helper()
	oldRoots, err := packages.Load(&packages.Config{Dir: dir, Mode: oldMode}, patterns...)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	loader, err := Load(dir, patterns, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return oldRoots, loader.Packages()
}

func TestEveryPackageFieldIsComparedOrExplained(t *testing.T) {
	typ := reflect.TypeOf(packages.Package{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		_, isCompared := compared[field.Name]
		_, isExplained := notFilled[field.Name]
		if isCompared == isExplained {
			t.Errorf("packages.Package.%s must be in exactly one of compared or notFilled (compared=%v explained=%v)", field.Name, isCompared, isExplained)
		}
	}
	for name := range compared {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("compared names %s, which is not a packages.Package field", name)
		}
	}
}

// TestPackagesMatchPackagesLoadOnTheRealRoots is the differential oracle: the roots the five consumers load, loaded the
// old way (go list -export, compiles the closure) and through Packages(), field by field over the whole closure.
func TestPackagesMatchPackagesLoadOnTheRealRoots(t *testing.T) {
	roots := []string{
		"internal/contextfabric/...", "internal/api/...", "internal/contracts/v1/...", "internal/mcp/...",
		"internal/contextfabric/testdata/emissionprobe",
		"cmd/operationpolicy", "internal/runtime/hosted", "internal/contextfabric/directread",
		"internal/contextfabric/factoracle", "internal/contextfabric/falkorgraph", "internal/contextfabric/sourcerow",
		"internal/mcp/guide/guidegen",
	}
	var patterns []string
	for _, r := range roots {
		patterns = append(patterns, modulePath+r)
	}
	oldRoots, gotRoots := loadBoth(t, repopath.Root(t), patterns)
	if len(oldRoots) < 20 {
		t.Fatalf("the oracle loaded %d roots: it measured too little to mean anything", len(oldRoots))
	}
	if diffs := diffClosures(oldRoots, gotRoots); len(diffs) != 0 {
		t.Fatalf("Packages() differs from packages.Load in %d place(s):\n%s", len(diffs), strings.Join(diffs, "\n"))
	}
}

func TestPackagesMatchPackagesLoadOnThePlantedFixtures(t *testing.T) {
	t.Run("a root over a declaration that does not type-check", func(t *testing.T) {
		oldRoots, gotRoots := loadBoth(t, repopath.Root(t), []string{viewBase + "use", viewBase + "badroot", viewBase + "good"})
		// By design a dependency's function bodies are not checked, so packages.Load alone reports the body error of
		// brokendep.Body. Everything else must agree, including the declaration error and the root's IllTyped.
		diffs := diffClosures(oldRoots, gotRoots)
		for _, d := range diffs {
			if !strings.Contains(d, "brokendep: Errors differs") {
				t.Errorf("unexpected difference: %s", d)
			}
		}
		if len(diffs) != 1 {
			t.Errorf("want exactly the documented dependency-body difference, got %d: %v", len(diffs), diffs)
		}
	})
	t.Run("a dependency newer than its module go version", func(t *testing.T) {
		dir := t.TempDir()
		for rel, content := range map[string]string{
			"go.mod":     "module rootmod\n\ngo 1.22\n\nrequire oldmod v0.0.0\n\nreplace oldmod => ./old\n",
			"root.go":    "package rootmod\n\nimport \"oldmod\"\n\nvar V oldmod.T\n",
			"old/go.mod": "module oldmod\n\ngo 1.17\n",
			"old/old.go": "package oldmod\n\ntype T any\n",
		} {
			path := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		oldRoots, gotRoots := loadBoth(t, dir, []string{"rootmod"})
		if diffs := diffClosures(oldRoots, gotRoots); len(diffs) != 0 {
			t.Fatalf("differences:\n%s", strings.Join(diffs, "\n"))
		}
		if !oldRoots[0].IllTyped {
			t.Fatal("the control is broken: packages.Load did not reject the go 1.17 dependency")
		}
	})
}

// TestTheOracleSeesAPlantedParityDefect proves the comparison can fail: each defect is applied to the Packages() side
// of a clean load and must be reported.
func TestTheOracleSeesAPlantedParityDefect(t *testing.T) {
	defects := map[string]func(root *packages.Package){
		"a dropped import":       func(r *packages.Package) { delete(r.Imports, viewBase+"gooddep") },
		"a flipped IllTyped":     func(r *packages.Package) { r.IllTyped = true },
		"an invented error":      func(r *packages.Package) { r.Errors = append(r.Errors, packages.Error{Msg: "x"}) },
		"an emptied scope":       func(r *packages.Package) { r.Types = types.NewPackage(r.PkgPath, r.Name) },
		"missing sizes":          func(r *packages.Package) { r.TypesSizes = nil },
		"missing syntax":         func(r *packages.Package) { r.Syntax = nil },
		"missing type info":      func(r *packages.Package) { r.TypesInfo = &types.Info{} },
		"a wrong module version": func(r *packages.Package) { r.Module = &packages.Module{Path: "x", GoVersion: "1.1"} },
		"a dropped dependency type": func(r *packages.Package) {
			r.Imports[viewBase+"gooddep"].Types = types.NewPackage(viewBase+"gooddep", "gooddep")
		},
	}
	for name, plant := range defects {
		oldRoots, gotRoots := loadBoth(t, repopath.Root(t), []string{viewBase + "good"})
		if diffs := diffClosures(oldRoots, gotRoots); len(diffs) != 0 {
			t.Fatalf("%s: the clean control already differs: %v", name, diffs)
		}
		plant(gotRoots[0])
		if len(diffClosures(oldRoots, gotRoots)) == 0 {
			t.Errorf("%s: the oracle did not see it", name)
		}
	}
}
