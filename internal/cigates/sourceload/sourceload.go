package sourceload

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"sort"

	"golang.org/x/tools/go/packages"
)

// Load lists the packages matching patterns for one GOOS/GOARCH with `go list -deps` and type-checks them from
// source. It never compiles: packages.Load with NeedTypes runs `go list -export`, which builds the whole dependency
// closure for every release GOOS. Root packages are checked with function bodies and full type info. A dependency is
// checked with IgnoreFuncBodies: the walk only needs the declared types and signatures of what roots use.
func Load(root string, patterns []string, goos, goarch string) (*Loader, error) {
	cfg := &packages.Config{
		Dir:  root,
		Env:  append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch),
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	loader := &Loader{
		Fset:  token.NewFileSet(),
		sizes: types.SizesFor("gc", goarch),
		Units: map[string]*Unit{},
		Roots: pkgs,
	}
	for _, pkg := range pkgs {
		loader.isRoot(pkg.ID, true)
	}
	for _, pkg := range pkgs {
		loader.check(pkg)
	}
	return loader, nil
}

type Unit struct {
	Pkg   *packages.Package
	Files []*ast.File
	Types *types.Package
	Info  *types.Info
	Errs  []error
}

type Loader struct {
	Fset    *token.FileSet
	sizes   types.Sizes
	Units   map[string]*Unit
	Roots   []*packages.Package
	rootIDs map[string]bool
	shaped  bool
}

func (l *Loader) isRoot(id string, set bool) bool {
	if l.rootIDs == nil {
		l.rootIDs = map[string]bool{}
	}
	if set {
		l.rootIDs[id] = true
	}
	return l.rootIDs[id]
}

func (l *Loader) check(pkg *packages.Package) *Unit {
	if unit, ok := l.Units[pkg.ID]; ok {
		return unit
	}
	unit := &Unit{Pkg: pkg}
	l.Units[pkg.ID] = unit
	root := l.isRoot(pkg.ID, false)
	for _, name := range pkg.GoFiles {
		file, err := parser.ParseFile(l.Fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			unit.Errs = append(unit.Errs, err)
		}
		if file != nil {
			unit.Files = append(unit.Files, file)
		}
	}
	conf := types.Config{
		IgnoreFuncBodies: !root,
		FakeImportC:      true,
		Sizes:            l.sizes,
		Error:            func(err error) { unit.Errs = append(unit.Errs, err) },
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if path == "unsafe" {
				return types.Unsafe, nil
			}
			dep := pkg.Imports[path]
			if dep == nil {
				return nil, fmt.Errorf("package %s imports %q, which go list did not return", pkg.PkgPath, path)
			}
			return l.check(dep).Types, nil
		}),
	}
	if root {
		unit.Info = &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Implicits:  map[ast.Node]types.Object{},
			Instances:  map[*ast.Ident]types.Instance{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
	}
	unit.Types, _ = conf.Check(pkg.PkgPath, l.Fset, unit.Files, unit.Info)
	return unit
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// problems is every type or parse error of a root package, and of each dependency package whose objects a root uses:
// a dependency that did not check cleanly can hand the walk an invalid type, which the walk would read as "not a
// net/http type" and so miss a site.
func (l *Loader) Problems(goos, goarch string) []string {
	var out []string
	used := map[string]bool{}
	for _, pkg := range l.Roots {
		unit := l.Units[pkg.ID]
		for _, listErr := range pkg.Errors {
			out = append(out, fmt.Sprintf("LOAD error in %s (%s/%s): %v", pkg.PkgPath, goos, goarch, listErr))
		}
		for _, err := range unit.Errs {
			out = append(out, fmt.Sprintf("LOAD error in %s (%s/%s): %v", pkg.PkgPath, goos, goarch, err))
		}
		for _, obj := range unit.Info.Uses {
			if obj.Pkg() != nil && obj.Pkg() != unit.Types {
				used[obj.Pkg().Path()] = true
			}
		}
	}
	for _, unit := range l.Units {
		if l.isRoot(unit.Pkg.ID, false) || !used[unit.Pkg.PkgPath] || len(unit.Errs) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("LOAD error in dependency %s (%s/%s), whose objects the walk uses: %v", unit.Pkg.PkgPath, goos, goarch, unit.Errs[0]))
	}
	sort.Strings(out)
	return out
}

// Packages returns the root packages as go/packages values filled from the source-checked units, so an analysis that
// reads Fset, Syntax, Types, TypesInfo, Imports and Errors needs no other change than the load call. Only roots carry
// Syntax and TypesInfo; a dependency carries its declared types and its own declaration errors. A dependency's
// function bodies are not checked, so a type error inside one is not reported.
func (l *Loader) Packages() []*packages.Package {
	if l.shaped {
		return l.Roots
	}
	l.shaped = true
	packages.Visit(l.Roots, nil, func(pkg *packages.Package) {
		pkg.Fset = l.Fset
		pkg.TypesSizes = l.sizes
		if pkg.PkgPath == "unsafe" {
			pkg.Types = types.Unsafe
			return
		}
		unit := l.Units[pkg.ID]
		if unit == nil {
			return
		}
		pkg.Types = unit.Types
		if l.isRoot(pkg.ID, false) {
			pkg.Syntax = unit.Files
			pkg.TypesInfo = unit.Info
		}
		for _, err := range unit.Errs {
			kind := packages.TypeError
			if _, parse := err.(scanner.ErrorList); parse {
				kind = packages.ParseError
			}
			e := packages.Error{Msg: err.Error(), Kind: kind}
			if typeErr, ok := err.(types.Error); ok {
				e.Pos = l.Fset.Position(typeErr.Pos).String()
				e.Msg = typeErr.Msg
			}
			pkg.Errors = append(pkg.Errors, e)
		}
		if len(unit.Errs) > 0 {
			pkg.IllTyped = true
		}
	})
	return l.Roots
}
