package outboundtransport

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"sort"

	"golang.org/x/tools/go/packages"
)

// loadSources lists the packages matching patterns for one GOOS/GOARCH with `go list -deps` and type-checks them from
// source. It never compiles: packages.Load with NeedTypes runs `go list -export`, which builds the whole dependency
// closure for every release GOOS. Root packages are checked with function bodies and full type info. A dependency is
// checked with IgnoreFuncBodies: the walk only needs the declared types and signatures of what roots use.
func loadSources(root string, patterns []string, goos, goarch string) (*sourceLoader, error) {
	cfg := &packages.Config{
		Dir:  root,
		Env:  append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	loader := &sourceLoader{
		fset:  token.NewFileSet(),
		sizes: types.SizesFor("gc", goarch),
		units: map[string]*sourceUnit{},
		roots: pkgs,
	}
	for _, pkg := range pkgs {
		loader.isRoot(pkg.ID, true)
	}
	for _, pkg := range pkgs {
		loader.check(pkg)
	}
	return loader, nil
}

type sourceUnit struct {
	pkg   *packages.Package
	files []*ast.File
	types *types.Package
	info  *types.Info
	errs  []error
}

type sourceLoader struct {
	fset    *token.FileSet
	sizes   types.Sizes
	units   map[string]*sourceUnit
	roots   []*packages.Package
	rootIDs map[string]bool
}

func (l *sourceLoader) isRoot(id string, set bool) bool {
	if l.rootIDs == nil {
		l.rootIDs = map[string]bool{}
	}
	if set {
		l.rootIDs[id] = true
	}
	return l.rootIDs[id]
}

func (l *sourceLoader) check(pkg *packages.Package) *sourceUnit {
	if unit, ok := l.units[pkg.ID]; ok {
		return unit
	}
	unit := &sourceUnit{pkg: pkg}
	l.units[pkg.ID] = unit
	root := l.isRoot(pkg.ID, false)
	for _, name := range pkg.GoFiles {
		file, err := parser.ParseFile(l.fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			unit.errs = append(unit.errs, err)
		}
		if file != nil {
			unit.files = append(unit.files, file)
		}
	}
	conf := types.Config{
		IgnoreFuncBodies: !root,
		FakeImportC:      true,
		Sizes:            l.sizes,
		Error:            func(err error) { unit.errs = append(unit.errs, err) },
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if path == "unsafe" {
				return types.Unsafe, nil
			}
			dep := pkg.Imports[path]
			if dep == nil {
				return nil, fmt.Errorf("package %s imports %q, which go list did not return", pkg.PkgPath, path)
			}
			return l.check(dep).types, nil
		}),
	}
	if root {
		unit.info = &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Implicits:  map[ast.Node]types.Object{},
			Instances:  map[*ast.Ident]types.Instance{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
	}
	unit.types, _ = conf.Check(pkg.PkgPath, l.fset, unit.files, unit.info)
	return unit
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// problems is every type or parse error of a root package, and of each dependency package whose objects a root uses:
// a dependency that did not check cleanly can hand the walk an invalid type, which the walk would read as "not a
// net/http type" and so miss a site.
func (l *sourceLoader) problems(goos, goarch string) []string {
	var out []string
	used := map[string]bool{}
	for _, pkg := range l.roots {
		unit := l.units[pkg.ID]
		for _, listErr := range pkg.Errors {
			out = append(out, fmt.Sprintf("LOAD error in %s (%s/%s): %v", pkg.PkgPath, goos, goarch, listErr))
		}
		for _, err := range unit.errs {
			out = append(out, fmt.Sprintf("LOAD error in %s (%s/%s): %v", pkg.PkgPath, goos, goarch, err))
		}
		for _, obj := range unit.info.Uses {
			if obj.Pkg() != nil && obj.Pkg() != unit.types {
				used[obj.Pkg().Path()] = true
			}
		}
	}
	for _, unit := range l.units {
		if l.isRoot(unit.pkg.ID, false) || !used[unit.pkg.PkgPath] || len(unit.errs) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("LOAD error in dependency %s (%s/%s), whose objects the walk uses: %v", unit.pkg.PkgPath, goos, goarch, unit.errs[0]))
	}
	sort.Strings(out)
	return out
}
