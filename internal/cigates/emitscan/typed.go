package emitscan

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// refusalDetail is the R10 detail of a package whose types the scan cannot read. It is never pinnable.
const refusalDetail = "refusal:typecheck"

// typedPackage is the type-check of one package directory. err is set when the package does not type-check.
type typedPackage struct {
	info *types.Info
	err  error
}

// typedInfo type-checks the package once, on the first benign key it meets: the files that match the default build
// context are checked together against the export data of the module's dependencies.
func (p *pkgInfo) typedInfo() *typedPackage {
	if p.typed == nil {
		info, err := typeCheck(p.budget.ctx, p.root, p.group)
		p.typed = &typedPackage{info: info, err: err}
	}
	return p.typed
}

type exportEntry struct {
	files map[string]string
}

var exportCache = struct {
	sync.Mutex
	byDir map[string]*exportEntry
}{byDir: map[string]*exportEntry{}}

// moduleDirOf is the nearest directory with a go.mod at or above root, else at or above the working directory (a scan
// root made of fixture files has no module of its own).
func moduleDirOf(root string) (string, error) {
	starts := []string{root}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, start := range starts {
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", errors.New("no go.mod above the scan root or the working directory")
}

// exportFiles maps import path to export data file for every package of the module and its dependencies.
func exportFiles(ctx context.Context, moduleDir string) (map[string]string, error) {
	exportCache.Lock()
	defer exportCache.Unlock()
	if entry, ok := exportCache.byDir[moduleDir]; ok {
		return entry.files, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-export", "-deps", "-f", "{{if .Export}}{{.ImportPath}}\t{{.Export}}{{end}}", "./...")
	cmd.Dir = moduleDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -export: %w", err)
	}
	files := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if path, file, ok := strings.Cut(line, "\t"); ok {
			files[path] = file
		}
	}
	exportCache.byDir[moduleDir] = &exportEntry{files: files}
	return files, nil
}

func typeCheck(ctx context.Context, root string, group []parsedFile) (*types.Info, error) {
	if len(group) == 0 {
		return nil, errors.New("no files")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, parsed := range group {
		match, err := build.Default.MatchFile(filepath.Join(absRoot, filepath.Dir(filepath.FromSlash(parsed.rel))), filepath.Base(parsed.rel))
		if err != nil {
			return nil, err
		}
		if match {
			files = append(files, parsed.file)
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no file matches the build context")
	}
	moduleDir, err := moduleDirOf(root)
	if err != nil {
		return nil, err
	}
	exports, err := exportFiles(ctx, moduleDir)
	if err != nil {
		return nil, err
	}
	fset := group[0].fset
	lookup := func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(file)
	}
	var first error
	cfg := &types.Config{
		Importer:    importer.ForCompiler(fset, "gc", lookup),
		FakeImportC: true,
		Error: func(err error) {
			if first == nil {
				first = err
			}
		},
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	_, checkErr := cfg.Check(files[0].Name.Name, fset, files, info)
	if first != nil {
		return nil, first
	}
	if checkErr != nil {
		return nil, checkErr
	}
	return info, nil
}

// typeOf is the type the checker recorded for an expression of a file that was checked.
func (s *fileScan) typeOf(expr ast.Expr) (types.Type, bool) {
	typed := s.pkg.typedInfo()
	if typed.err != nil {
		return nil, false
	}
	tv, ok := typed.info.Types[expr]
	if !ok || tv.Type == nil || tv.IsType() {
		return nil, false
	}
	return tv.Type, true
}

// kindOf is the slog value kind an expression boxes to when it is passed as a loose slog argument, read from the type
// the checker gave it: "int" (int..int64), "bool", "string", "uint", "float", or "other" for a defined type, pointer or
// anything else (slog.Any keeps those as KindAny). A type parameter gives the comma-joined kinds its constraint allows.
// "" means the static type does not fix the kind (an interface); ok is false when there is no valid type, which is a refusal.
func (s *fileScan) kindOf(expr ast.Expr) (kind string, ok bool) {
	t, ok := s.typeOf(expr)
	if !ok {
		return "", false
	}
	return kindOfType(t, 0)
}

func kindOfType(t types.Type, depth int) (string, bool) {
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		switch types.Default(u).(*types.Basic).Kind() {
		case types.Int, types.Int8, types.Int16, types.Int32, types.Int64:
			return "int", true
		case types.Bool:
			return "bool", true
		case types.String:
			return "string", true
		case types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr:
			return "uint", true
		case types.Float32, types.Float64:
			return "float", true
		case types.Invalid, types.UntypedNil:
			return "", false
		}
		return "other", true
	case *types.Interface:
		return "", true
	case *types.TypeParam:
		return strings.Join(constraintKinds(u.Constraint(), depth+1), ","), true
	case *types.Tuple:
		return "", false
	case *types.Named:
		if _, isInterface := u.Underlying().(*types.Interface); isInterface {
			return "", true
		}
	}
	return "other", true
}

// constraintKinds lists the kinds a type parameter can be instantiated to under its constraint (a superset: an
// intersection is read as a union). A tilde term can be a defined type, which boxes as other. nil means the constraint
// does not bound the kind.
func constraintKinds(constraint types.Type, depth int) []string {
	if depth > 8 {
		return nil
	}
	set := map[string]bool{}
	var walk func(t types.Type, depth int) bool
	walk = func(t types.Type, depth int) bool {
		if depth > 8 {
			return false
		}
		switch u := types.Unalias(t).(type) {
		case *types.Union:
			for i := 0; i < u.Len(); i++ {
				term := u.Term(i)
				kind, ok := kindOfType(term.Type().Underlying(), depth+1)
				if !ok || kind == "" {
					return false
				}
				if term.Tilde() {
					set["other"] = true
					set[kind] = true
				} else if named, isNamed := types.Unalias(term.Type()).(*types.Named); isNamed && named != nil {
					set["other"] = true
				} else {
					set[kind] = true
				}
			}
			return true
		case *types.Interface:
			bounded := false
			for i := 0; i < u.NumEmbeddeds(); i++ {
				if walk(u.EmbeddedType(i), depth+1) {
					bounded = true
				}
			}
			return bounded
		case *types.Named:
			return walk(u.Underlying(), depth+1)
		}
		kind, ok := kindOfType(t, depth+1)
		if !ok || kind == "" {
			return false
		}
		set[kind] = true
		return true
	}
	if !walk(constraint, depth) {
		return nil
	}
	out := make([]string, 0, len(set))
	for kind := range set {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// attrValueKind is the kind of the slog.Value an Attr literal holds when it is built by a typed slog constructor;
// any other value expression (a variable, slog.AnyValue, a missing value) is "" (the kind is not fixed by the source).
func (s *fileScan) attrValueKind(value ast.Expr) (string, bool) {
	typed := s.pkg.typedInfo()
	if typed.err != nil {
		return "", false
	}
	if value == nil {
		return "", true
	}
	call, isCall := ast.Unparen(value).(*ast.CallExpr)
	if !isCall {
		return "", true
	}
	var name *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		name = fun
	case *ast.SelectorExpr:
		name = fun.Sel
	default:
		return "", true
	}
	fn, isFunc := typed.info.Uses[name].(*types.Func)
	if !isFunc || fn.Pkg() == nil || fn.Pkg().Path() != "log/slog" {
		return "", true
	}
	switch fn.Name() {
	case "StringValue":
		return "string", true
	case "IntValue", "Int64Value":
		return "int", true
	case "BoolValue":
		return "bool", true
	case "Uint64Value":
		return "uint", true
	case "Float64Value":
		return "float", true
	case "GroupValue", "TimeValue", "DurationValue":
		return "other", true
	}
	return "", true
}

// pairSource checks what a key/value pair list is read from: a composite literal must be a []any and an append callee
// must be the builtin. ok is the answer, valid is false when the types are unavailable (a refusal).
func (s *fileScan) pairSource(shape ast.Expr) (ok, valid bool) {
	typed := s.pkg.typedInfo()
	if typed.err != nil {
		return false, false
	}
	switch n := shape.(type) {
	case *ast.Ident:
		builtin, isBuiltin := typed.info.Uses[n].(*types.Builtin)
		return isBuiltin && builtin.Name() == "append", true
	case *ast.CompositeLit:
		t, found := s.typeOf(n)
		if !found {
			return false, false
		}
		slice, isSlice := types.Unalias(t).(*types.Slice)
		if !isSlice {
			return false, true
		}
		iface, isIface := types.Unalias(slice.Elem()).Underlying().(*types.Interface)
		return isIface && iface.Empty(), true
	}
	return false, false
}
