package emitscan

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

// refusalDetail is the R10 detail of a package whose types the scan cannot read. It is never pinnable.
const refusalDetail = "refusal:typecheck"

// shadowDetail is the R10 detail of a parameter or local variable that hides a constant holding a key the redactor
// knows (protected, network or benign): the name reads as the constant to a reader and as something else to the compiler.
// It is never pinnable.
const shadowDetail = "refusal:shadowed-key"

const attributePkg = "go.opentelemetry.io/otel/attribute"

// typedPackage is the type-check of one package directory. err is set when the package does not type-check.
type typedPackage struct {
	info  *types.Info
	files []*ast.File
	err   error
	flow  *flowInfo
}

// typedInfo type-checks the package once, on the first benign key it meets: the files that match the default build
// context are checked together against the export data of the module's dependencies.
func (p *pkgInfo) typedInfo() *typedPackage {
	if p.typed == nil {
		info, files, err := typeCheck(p.budget.ctx, p.root, p.group)
		p.typed = &typedPackage{info: info, files: files, err: err}
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

func typeCheck(ctx context.Context, root string, group []parsedFile) (*types.Info, []*ast.File, error) {
	if len(group) == 0 {
		return nil, nil, errors.New("no files")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	var files []*ast.File
	for _, parsed := range group {
		match, err := build.Default.MatchFile(filepath.Join(absRoot, filepath.Dir(filepath.FromSlash(parsed.rel))), filepath.Base(parsed.rel))
		if err != nil {
			return nil, nil, err
		}
		if match {
			files = append(files, parsed.file)
		}
	}
	if len(files) == 0 {
		return nil, nil, errors.New("no file matches the build context")
	}
	moduleDir, err := moduleDirOf(root)
	if err != nil {
		return nil, nil, err
	}
	exports, err := exportFiles(ctx, moduleDir)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, first
	}
	if checkErr != nil {
		return nil, nil, checkErr
	}
	return info, files, nil
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
	if _, found := s.typeOf(value); !found {
		return "", false
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

// typedFold resolves the names of a key expression through the objects the checker bound them to, not through their
// spelling: a constant is its exact value, a parameter or local variable is never a package constant of the same name, and
// a local variable that is initialised once and never written again is the value of its initialiser.
type typedFold struct{ s *fileScan }

const maxLocalDepth = 8

func (f *typedFold) ready(ctx *foldCtx) (*typedPackage, bool) {
	typed := f.s.pkg.typedInfo()
	if typed.err != nil {
		if ctx.refusal == "" {
			ctx.refusal = refusalDetail
		}
		return nil, false
	}
	return typed, true
}

func objectOf(info *types.Info, ident *ast.Ident) types.Object {
	if obj := info.Uses[ident]; obj != nil {
		return obj
	}
	return info.Defs[ident]
}

func (f *typedFold) ident(ident *ast.Ident, ctx *foldCtx) []string {
	typed, ok := f.ready(ctx)
	if !ok {
		return nil
	}
	obj := objectOf(typed.info, ident)
	if obj == nil {
		if ctx.refusal == "" {
			ctx.refusal = refusalDetail
		}
		return nil
	}
	switch o := obj.(type) {
	case *types.Const:
		if o.Val().Kind() == constant.String {
			return []string{constant.StringVal(o.Val())}
		}
	case *types.Var:
		if o.IsField() {
			return nil
		}
		if shadowsKnownKey(o) {
			if ctx.refusal == "" {
				ctx.refusal = shadowDetail
			}
			return nil
		}
		return f.local(typed, o, ctx)
	}
	return nil
}

// shadowsKnownKey reports a variable that hides a string constant, visible at the variable's declaration, whose text is a
// key the redactor treats specially.
func shadowsKnownKey(v *types.Var) bool {
	scope := v.Parent()
	if scope == nil || scope.Parent() == nil {
		return false
	}
	_, hidden := scope.Parent().LookupParent(v.Name(), v.Pos())
	c, isConst := hidden.(*types.Const)
	if !isConst || c.Val().Kind() != constant.String {
		return false
	}
	text := constant.StringVal(c.Val())
	protected, network := telemetryscrub.KeyClass(text)
	return protected || network || telemetryscrub.BenignKind(text) != ""
}

// local folds a function-local variable that is initialised exactly once and never written again, never has its address
// taken and is never a range target. Any other variable (parameter, result, package variable, reassigned local) is unprovable.
func (f *typedFold) local(typed *typedPackage, v *types.Var, ctx *foldCtx) []string {
	if v.Pkg() == nil || v.Parent() == v.Pkg().Scope() {
		return nil
	}
	flow := typed.flowInfo()
	init, ok := flow.inits[v]
	if !ok || flow.written[v] {
		return nil
	}
	if ctx.depth++; ctx.depth > maxLocalDepth {
		ctx.over = true
		return nil
	}
	defer func() { ctx.depth-- }()
	return foldString(init, ctx, 0)
}

// callKind says what a one-argument call in a key expression is, from the object its callee resolves to: "conv" for a
// conversion to a string type or attribute.Key, "text" for strconv.Itoa and fmt.Sprint, "" for anything else (a function
// that shadows a builtin or a type name is a call, not a conversion).
func (f *typedFold) callKind(call *ast.CallExpr, ctx *foldCtx) string {
	typed, ok := f.ready(ctx)
	if !ok {
		return ""
	}
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if name, isType := typed.info.Uses[fun].(*types.TypeName); isType {
			if basic, isBasic := name.Type().Underlying().(*types.Basic); isBasic && basic.Info()&types.IsString != 0 {
				return "conv"
			}
		}
	case *ast.SelectorExpr:
		switch o := typed.info.Uses[fun.Sel].(type) {
		case *types.TypeName:
			if o.Pkg() != nil && o.Pkg().Path() == attributePkg && o.Name() == "Key" {
				return "conv"
			}
		case *types.Func:
			if o.Pkg() != nil && (o.Pkg().Path() == "strconv" && o.Name() == "Itoa" || o.Pkg().Path() == "fmt" && o.Name() == "Sprint") {
				return "text"
			}
		}
	}
	return ""
}

// flowInfo records, for the variables of one package, the single initialiser of each and every variable that is written
// anywhere else (assignment, op-assignment, inc/dec, range target, address taken, redeclared by :=).
type flowInfo struct {
	inits   map[*types.Var]ast.Expr
	written map[types.Object]bool
}

func (t *typedPackage) flowInfo() *flowInfo {
	if t.flow != nil {
		return t.flow
	}
	flow := &flowInfo{inits: map[*types.Var]ast.Expr{}, written: map[types.Object]bool{}}
	mark := func(expr ast.Expr) {
		if expr == nil {
			return
		}
		if ident, ok := ast.Unparen(expr).(*ast.Ident); ok {
			if obj := objectOf(t.info, ident); obj != nil {
				flow.written[obj] = true
			}
		}
	}
	for _, file := range t.files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.AssignStmt:
				if n.Tok != token.DEFINE {
					for _, lhs := range n.Lhs {
						mark(lhs)
					}
					return true
				}
				for i, lhs := range n.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if !ok {
						continue
					}
					def, isDef := t.info.Defs[ident].(*types.Var)
					if !isDef {
						mark(lhs)
						continue
					}
					if len(n.Lhs) == len(n.Rhs) {
						flow.inits[def] = n.Rhs[i]
					} else {
						flow.written[def] = true
					}
				}
			case *ast.IncDecStmt:
				mark(n.X)
			case *ast.RangeStmt:
				if n.Tok == token.DEFINE {
					for _, expr := range []ast.Expr{n.Key, n.Value} {
						if ident, ok := expr.(*ast.Ident); ok {
							if def := t.info.Defs[ident]; def != nil {
								flow.written[def] = true
							}
						}
					}
				} else {
					mark(n.Key)
					mark(n.Value)
				}
			case *ast.ValueSpec:
				for i, name := range n.Names {
					def, isVar := t.info.Defs[name].(*types.Var)
					if !isVar {
						continue
					}
					if len(n.Values) == len(n.Names) {
						flow.inits[def] = n.Values[i]
					} else {
						flow.written[def] = true
					}
				}
			case *ast.UnaryExpr:
				if n.Op == token.AND {
					mark(n.X)
				}
			}
			return true
		})
	}
	t.flow = flow
	return flow
}

// isAttributeKey reports an identifier whose static type is attribute.Key. known is false when the package has no types.
func (s *fileScan) isAttributeKey(ident *ast.Ident) (isKey, known bool) {
	typed := s.pkg.typedInfo()
	if typed.err != nil {
		return false, false
	}
	obj := objectOf(typed.info, ident)
	if obj == nil {
		return false, false
	}
	named, ok := types.Unalias(obj.Type()).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == attributePkg && named.Obj().Name() == "Key", true
}
