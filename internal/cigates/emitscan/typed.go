package emitscan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan/tcheck"
	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

// refusalDetail is the R10 detail of a package whose types the scan cannot read. It is never pinnable.
const refusalDetail = "refusal:typecheck"

// helperUnavailableDetail is the R10 detail of a package whose type-check helper could not be built, found or trusted: the
// scan cannot tell what the package's keys are, and says so under its own reason. Never pinnable.
const helperUnavailableDetail = "refusal:typecheck_child_unavailable"

var errHelperUnavailable = errors.New("type-check helper unavailable")

// refusal is the R10 detail for a package whose types cannot be read.
func (p *pkgInfo) refusal() string {
	if p.typed != nil && errors.Is(p.typed.err, errHelperUnavailable) {
		return helperUnavailableDetail
	}
	return refusalDetail
}

// errOversize is the type-check error of a package that holds an expression the scan's fold bound refuses: it is not
// checked, because the checker's work on it is not bounded by the scan's budget; the scan reads its keys by name, fail closed.
var errOversize = errors.New("package holds an expression larger than the scan's fold bound")

// shadowDetail is the R10 detail of a parameter or local variable that hides a constant holding a key the redactor
// knows (protected, network or benign): the name reads as the constant to a reader and as something else to the compiler.
// It is never pinnable.
const shadowDetail = "refusal:shadowed-key"

// keyReceiverDetail is the R10 detail of an attribute.Key method call or method value whose receiver the scan cannot
// read: a pointer, a type parameter or interface that may hold a Key, or a method value that escapes. Never pinnable.
const keyReceiverDetail = "refusal:key-receiver"

// keyMethodValueDetail is the R10 detail of an attribute.Key method value that is not a plain local alias that is only called.
const keyMethodValueDetail = "refusal:key-method-value"

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
	if p.typed == nil && p.oversize {
		p.typed = &typedPackage{err: errOversize}
	}
	if p.typed == nil {
		info, files, err := p.boundedTypeCheck()
		p.typed = &typedPackage{info: info, files: files, err: err}
	}
	return p.typed
}

// sourceSnapshot is every file the scan parsed, as the bytes it parsed.
type sourceSnapshot struct {
	root  string
	files []parsedFile // sorted by rel
	key   string       // digest of the rels and bytes
}

func newSourceSnapshot(root string, byDir map[string][]parsedFile) *sourceSnapshot {
	snap := &sourceSnapshot{root: root}
	for _, group := range byDir {
		snap.files = append(snap.files, group...)
	}
	sort.Slice(snap.files, func(i, j int) bool { return snap.files[i].rel < snap.files[j].rel })
	snap.key = probeKey(snap.files)
	return snap
}

// inModule is the part of the snapshot that lies under moduleDir, with its paths relative to moduleDir. Only those files can
// be read by `go list ./...` of the module, so only they belong in its overlay and in the key of its export set; a scan root
// outside the module (a fixture) must neither change the key nor add overlay entries. Paths are compared as given (no symlink
// resolution).
func (snap *sourceSnapshot) inModule(moduleDir string) (*sourceSnapshot, error) {
	absRoot, err := filepath.Abs(snap.root)
	if err != nil {
		return nil, err
	}
	var inside []parsedFile
	for _, parsed := range snap.files {
		rel, err := filepath.Rel(moduleDir, filepath.Join(absRoot, filepath.FromSlash(parsed.rel)))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		inside = append(inside, parsedFile{rel: filepath.ToSlash(rel), src: parsed.src})
	}
	return newSourceSnapshot(moduleDir, map[string][]parsedFile{"": inside}), nil
}

// writeOverlay writes the parsed bytes to a private temp directory and the go overlay file that maps each scanned file to
// its copy. The directory is returned even with an error, so the caller removes it.
func (snap *sourceSnapshot) writeOverlay() (dir, overlayFile string, err error) {
	absRoot, err := filepath.Abs(snap.root)
	if err != nil {
		return "", "", err
	}
	dir, err = os.MkdirTemp("", "emitscan-overlay-")
	if err != nil {
		return "", "", err
	}
	replace := map[string]string{}
	for i, parsed := range snap.files {
		copyPath := filepath.Join(dir, fmt.Sprintf("%d.go", i))
		if err := os.WriteFile(copyPath, parsed.src, 0o600); err != nil {
			return dir, "", err
		}
		replace[filepath.Join(absRoot, filepath.FromSlash(parsed.rel))] = copyPath
	}
	payload, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return dir, "", err
	}
	overlayFile = filepath.Join(dir, "overlay.json")
	return dir, overlayFile, os.WriteFile(overlayFile, payload, 0o600)
}

// exportListExecs counts the `go list -export -deps` commands this process started.
var exportListExecs atomic.Int64

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
//
// With a snapshot, go list builds every scanned file from the bytes the scan parsed (an overlay of temp copies), so a
// change on disk while it runs cannot reach the export data. Files go list reads that the scan did not parse (go.mod,
// test files, directories outside the scanned ones) are still read from disk.
func exportFiles(ctx context.Context, moduleDir string, snap *sourceSnapshot) (map[string]string, error) {
	exportCache.Lock()
	defer exportCache.Unlock()
	if snap != nil {
		var snapErr error
		if snap, snapErr = snap.inModule(moduleDir); snapErr != nil {
			return nil, snapErr
		}
	}
	cacheKey := moduleDir
	if snap != nil && len(snap.files) > 0 {
		cacheKey += "\x00" + snap.key
	}
	if entry, ok := exportCache.byDir[cacheKey]; ok {
		return entry.files, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	goPath, err := goBinary()
	if err != nil {
		return nil, err
	}
	args := []string{"list", "-export", "-deps"}
	if snap != nil && len(snap.files) > 0 {
		overlayDir, overlayFile, err := snap.writeOverlay()
		if overlayDir != "" {
			defer os.RemoveAll(overlayDir)
		}
		if err != nil {
			return nil, fmt.Errorf("go list overlay: %w", err)
		}
		args = append(args, "-overlay", overlayFile)
	}
	args = append(args, "-f", "{{if .Export}}{{.ImportPath}}\t{{.Export}}{{end}}", "./...")
	exportListExecs.Add(1)
	cmd := exec.CommandContext(ctx, goPath, args...)
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
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
	exportCache.byDir[cacheKey] = &exportEntry{files: files}
	return files, nil
}

func typeCheck(root string, group []parsedFile, exports map[string]string) (*types.Info, []*ast.File, error) {
	if len(group) == 0 {
		return nil, nil, errors.New("no files")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	var files []*ast.File
	for _, parsed := range group {
		match, err := tcheck.Match(absRoot, parsed.rel, parsed.src)
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
	info, err := tcheck.Check(group[0].fset, files, exports)
	if err != nil {
		return nil, nil, err
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
			ctx.refusal = f.s.pkg.refusal()
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
			ctx.refusal = f.s.pkg.refusal()
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
	callees map[*ast.Ident]bool // identifiers that are the callee of a call
	uses    map[types.Object][]*ast.Ident
}

func (t *typedPackage) flowInfo() *flowInfo {
	if t.flow != nil {
		return t.flow
	}
	flow := &flowInfo{inits: map[*types.Var]ast.Expr{}, written: map[types.Object]bool{}, callees: map[*ast.Ident]bool{}, uses: map[types.Object][]*ast.Ident{}}
	for ident, obj := range t.info.Uses {
		flow.uses[obj] = append(flow.uses[obj], ident)
	}
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
			case *ast.CallExpr:
				if ident, ok := ast.Unparen(n.Fun).(*ast.Ident); ok {
					flow.callees[ident] = true
				}
			case *ast.SelectorExpr:
				// a method with a pointer receiver called on a variable takes its address; a selection the checker did not
				// resolve is read the same way
				ident, ok := ast.Unparen(n.X).(*ast.Ident)
				if !ok {
					break
				}
				v, isVar := objectOf(t.info, ident).(*types.Var)
				if !isVar {
					break
				}
				selection := t.info.Selections[n]
				if selection == nil {
					flow.written[v] = true
					break
				}
				if fn, isFunc := selection.Obj().(*types.Func); isFunc {
					if sig, isSig := fn.Type().(*types.Signature); isSig && sig.Recv() != nil {
						if _, pointer := types.Unalias(sig.Recv().Type()).(*types.Pointer); pointer {
							flow.written[v] = true
						}
					}
				}
			}
			return true
		})
	}
	t.flow = flow
	return flow
}

const (
	keyMethodNone = iota
	keyMethodKey
	keyMethodExpr
	keyMethodRefuse
	keyMethodNoTypes
)

// attrKeyMethod classifies a selector that names an attribute constructor method (String, Int, ...). keyMethodKey: the
// method of attribute.Key on a receiver the scan can read (recv). keyMethodExpr: the method expression attribute.Key.String,
// whose first argument is the receiver. keyMethodRefuse: the same method on a receiver it cannot read (a pointer, a
// dereference, a type parameter or interface that can hold a Key). keyMethodNoTypes: the package has no types.
func (s *fileScan) attrKeyMethod(sel *ast.SelectorExpr) (recv ast.Expr, class int) {
	if !attrConstructors[sel.Sel.Name] {
		return nil, keyMethodNone
	}
	if x, ok := sel.X.(*ast.Ident); ok && s.imports[x.Name] != "" {
		return nil, keyMethodNone
	}
	typed := s.pkg.typedInfo()
	if typed.err != nil {
		return nil, keyMethodNoTypes
	}
	selection := typed.info.Selections[sel]
	if selection == nil || (selection.Kind() != types.MethodVal && selection.Kind() != types.MethodExpr) {
		return nil, keyMethodNone
	}
	fn, isFunc := selection.Obj().(*types.Func)
	if !isFunc {
		return nil, keyMethodNone
	}
	sig, isSig := fn.Type().(*types.Signature)
	if !isSig || sig.Results().Len() != 1 || !isNamed(sig.Results().At(0).Type(), attributePkg, "KeyValue") {
		return nil, keyMethodNone
	}
	recvType := types.Unalias(selection.Recv())
	pointer := false
	if ptr, ok := recvType.(*types.Pointer); ok {
		pointer, recvType = true, types.Unalias(ptr.Elem())
	}
	switch recvType.Underlying().(type) {
	case *types.Interface:
		return nil, keyMethodRefuse
	}
	if _, isParam := recvType.(*types.TypeParam); isParam {
		return nil, keyMethodRefuse
	}
	if !isNamed(recvType, attributePkg, "Key") {
		return nil, keyMethodNone
	}
	if selection.Kind() == types.MethodExpr {
		if pointer {
			return nil, keyMethodRefuse
		}
		return nil, keyMethodExpr
	}
	receiver := ast.Unparen(sel.X)
	if _, deref := receiver.(*ast.StarExpr); deref || pointer {
		return nil, keyMethodRefuse
	}
	return receiver, keyMethodKey
}

func isNamed(t types.Type, pkgPath, name string) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == pkgPath && named.Obj().Name() == name
}

// keyMethodValue reads an attribute.Key method value or method expression (k.String, attribute.Key.String, not called where
// it is written): bound once to a local that is only ever called it is followed (keyAliases); any other use (returned,
// stored, passed, rebound) is a refusal.
func (s *fileScan) keyMethodValue(sel *ast.SelectorExpr) {
	if !attrConstructors[sel.Sel.Name] {
		return
	}
	depth := len(s.nodes) - 2
	var node ast.Node = sel
	for depth >= 0 {
		if _, paren := s.nodes[depth].(*ast.ParenExpr); !paren {
			break
		}
		node = s.nodes[depth]
		depth--
	}
	var parent ast.Node
	if depth >= 0 {
		parent = s.nodes[depth]
	}
	if call, isCall := parent.(*ast.CallExpr); isCall && call.Fun == node {
		return
	}
	recv, class := s.attrKeyMethod(sel)
	switch class {
	case keyMethodNone:
		return
	case keyMethodNoTypes:
		s.add(RuleBenignKind, sel.Pos(), s.pkg.refusal())
		return
	case keyMethodRefuse:
		s.add(RuleBenignKind, sel.Pos(), keyReceiverDetail)
		return
	}
	var bound *ast.Ident
	switch p := parent.(type) {
	case *ast.AssignStmt:
		if p.Tok == token.DEFINE && len(p.Lhs) == 1 && len(p.Rhs) == 1 && p.Rhs[0] == node {
			bound, _ = p.Lhs[0].(*ast.Ident)
		}
	case *ast.ValueSpec:
		if len(p.Names) == 1 && len(p.Values) == 1 && p.Values[0] == node {
			bound = p.Names[0]
		}
	}
	typed := s.pkg.typedInfo()
	if bound != nil {
		if v, isVar := typed.info.Defs[bound].(*types.Var); isVar {
			flow := typed.flowInfo()
			onlyCalled := !flow.written[v]
			for _, use := range flow.uses[v] {
				onlyCalled = onlyCalled && flow.callees[use]
			}
			if onlyCalled {
				s.pkg.keyAliases[v] = recv
				return
			}
		}
	}
	s.add(RuleBenignKind, sel.Pos(), keyMethodValueDetail)
}

// boundedTypeCheck type-checks the package in this process only after the same check has finished inside a helper
// process under a CPU limit, a memory limit and a wall-clock limit (and the scan's context): the checker's own work on a
// constant expression is not bounded by anything the scan counts, so a shape it cannot finish never runs here. Any abnormal
// helper result is a package that does not type-check; there is no in-process fallback.
func (p *pkgInfo) boundedTypeCheck() (*types.Info, []*ast.File, error) {
	if len(p.group) == 0 {
		return nil, nil, errors.New("no files")
	}
	moduleDir, err := moduleDirOf(p.root)
	if err != nil {
		return nil, nil, err
	}
	exports, err := exportFiles(p.budget.ctx, moduleDir, p.snap)
	if err != nil {
		return nil, nil, err
	}
	if err := probeTypeCheck(p.budget.ctx, p.helperCPU, p.root, p.group, exports); err != nil {
		return nil, nil, err
	}
	return typeCheck(p.root, p.group, exports)
}

// Limits of the type-check helper: at least three times what the largest package of the real tree needs (see the numbers
// in the change that set them). A package whose check does not finish inside them is refused.
var (
	childCPUSeconds = 4
	childRSSLimit   = int64(512 << 20)
	childWall       = 20 * time.Second
)

// childStats are the largest CPU time and resident set one helper used, for the limits' headroom.
var childStats struct{ cpuNanos, rssKB, count, nodes atomic.Int64 }

func noteChildStat(slot *atomic.Int64, value int64) {
	for {
		old := slot.Load()
		if value <= old || slot.CompareAndSwap(old, value) {
			return
		}
	}
}

// childEnvAllowed is the whole environment of the helper: nothing but its own memory hint is passed, so no credential or
// token variable of the scanning process reaches it.
func childEnvironment() []string {
	return []string{"GOMEMLIMIT=" + strconv.FormatInt(childRSSLimit*3/4, 10), "GOENV=off", "GOWORK=off"}
}

// buildEnvironment is the environment of the one `go build` that makes the helper: a fixed one, plus the toolchain's own
// locations. No -race or -toolexec through GOFLAGS, no cgo, the local toolchain only, no network.
func buildEnvironment() []string {
	env := []string{"GOFLAGS=", "GOENV=off", "GOWORK=off", "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off"}
	for _, name := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT", "TMPDIR", "XDG_CACHE_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

var goTool struct {
	sync.Mutex
	path string
	err  error
	done bool
}

// goBinary is the go command every step of the type-check uses: the one on PATH, once per process, and only when its
// toolchain is the one that built this binary. The export data of `go list` and the helper built by `go build` must come
// from the toolchain whose go/types reads them; a different go first on PATH (a test binary run outside `go test`, which
// is what puts the matching toolchain on PATH) used to end in a refusal that did not say why. A mismatch is named once on
// standard error and is an unavailable helper.
func goBinary() (string, error) {
	goTool.Lock()
	defer goTool.Unlock()
	if goTool.done {
		return goTool.path, goTool.err
	}
	goTool.done = true
	path, err := exec.LookPath("go")
	if err != nil {
		goTool.err = fmt.Errorf("%w: %v", errHelperUnavailable, err)
		fmt.Fprintf(os.Stderr, "emitscan: %v\n", goTool.err)
		return "", goTool.err
	}
	cmd := exec.Command(path, "env", "GOVERSION")
	cmd.Dir = os.TempDir()
	cmd.Env = buildEnvironment()
	out, err := cmd.Output()
	got := strings.TrimSpace(string(out))
	switch {
	case err != nil:
		goTool.err = fmt.Errorf("%w: %s env GOVERSION: %v", errHelperUnavailable, path, err)
	case got != runtime.Version():
		goTool.err = fmt.Errorf("%w: the go command on PATH (%s) is %s but this scanner was built with %s; put the matching toolchain first on PATH", errHelperUnavailable, path, got, runtime.Version())
	default:
		goTool.path = path
	}
	if goTool.err != nil {
		fmt.Fprintf(os.Stderr, "emitscan: %v\n", goTool.err)
	}
	return goTool.path, goTool.err
}

// helperOverride replaces the built helper (set by tests only, to plant a misbehaving one); helperBuilder replaces the
// build (tests only, to make it fail).
var (
	helperOverride string
	helperBuilder  func() (string, error)
)

var helperBinary struct {
	sync.Mutex
	dir  string
	path string
	err  error
	done bool
}

// childBinaryPath builds the helper once per process into a directory of this process's own (private to this user), from the
// module of the working directory or of the scan root. A failed build is remembered: the helper is unavailable.
func childBinaryPath(ctx context.Context, root string) (string, error) {
	if helperOverride != "" {
		return helperOverride, checkHelperFile(helperOverride)
	}
	helperBinary.Lock()
	defer helperBinary.Unlock()
	if !helperBinary.done {
		helperBinary.done = true
		helperBinary.path, helperBinary.err = buildHelper(ctx, root)
	}
	if helperBinary.err != nil {
		return "", helperBinary.err
	}
	if err := checkHelperFile(helperBinary.path); err != nil {
		return "", err
	}
	return helperBinary.path, nil
}

func buildHelper(ctx context.Context, root string) (string, error) {
	if helperBuilder != nil {
		path, err := helperBuilder()
		if err != nil {
			return "", fmt.Errorf("%w: %v", errHelperUnavailable, err)
		}
		return path, nil
	}
	importPath := reflect.TypeOf(parsedFile{}).PkgPath() + "/tcheck/child"
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		if dir, err := moduleDirOf(wd); err == nil {
			dirs = append(dirs, dir)
		}
	}
	if dir, err := moduleDirOf(root); err == nil {
		dirs = append(dirs, dir)
	}
	goPath, err := goBinary()
	if err != nil {
		return "", err
	}
	sweepStaleHelperDirs(os.TempDir())
	dir, err := os.MkdirTemp("", helperDirPrefix)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errHelperUnavailable, err)
	}
	writeCreatorMarker(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("%w: %v", errHelperUnavailable, err)
	}
	helperBinary.dir = dir
	target := filepath.Join(dir, "child")
	lastErr := errors.New("no module directory to build the type-check helper in")
	for _, moduleDir := range dirs {
		cmd := exec.CommandContext(context.WithoutCancel(ctx), goPath, "build", "-mod=readonly", "-trimpath", "-o", target, importPath)
		cmd.Dir = moduleDir
		cmd.Env = buildEnvironment()
		if output, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("build type-check helper: %w: %.200s", err, output)
			continue
		}
		return target, nil
	}
	return "", fmt.Errorf("%w: %v", errHelperUnavailable, lastErr)
}

const (
	helperDirPrefix = "emitscan-tcheck-"
	creatorMarker   = "creator"
)

type procStatus int

const (
	procUnknown procStatus = iota
	procKnown
	procGone
)

// writeCreatorMarker records, in the helper's directory, the process that made it, when that process started and the pid
// namespace both were read in. A killed run cannot remove its directory; the next run finds the marker and removes the
// directory only if the creator is gone. Where the start time or the namespace is not known no marker is written, and the
// directory is never swept.
func writeCreatorMarker(dir string) {
	pid := os.Getpid()
	start, status := processStart(pid)
	namespace, ok := pidNamespace()
	if status != procKnown || !ok {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, creatorMarker), []byte(strconv.Itoa(pid)+" "+start+" "+namespace+"\n"), 0o600)
}

// sweepStaleHelperDirs removes the helper directories under tmp that a killed run left behind: directories of this user
// whose marker names a process that no longer exists with that start time. Every lane on a host runs as the same user,
// so age is no evidence that a directory is unused. A directory without a readable marker, with an unparsable one (the
// start time must be a number), made in another pid namespace than this process's, or whose creator cannot be looked up
// is left alone.
func sweepStaleHelperDirs(tmp string) {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), helperDirPrefix) || !entry.IsDir() {
			continue
		}
		dir := filepath.Join(tmp, entry.Name())
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || !ownedByThisUser(info) {
			continue
		}
		marker := filepath.Join(dir, creatorMarker)
		if minfo, err := os.Lstat(marker); err != nil || !minfo.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			continue
		}
		fields := strings.Fields(string(data))
		if len(fields) != 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		_, startErr := strconv.ParseUint(fields[1], 10, 64)
		if pidErr != nil || pid <= 0 || startErr != nil {
			continue
		}
		startText := fields[1]
		if namespace, ok := pidNamespace(); !ok || namespace != fields[2] {
			continue
		}
		start, status := processStart(pid)
		if status == procUnknown || (status == procKnown && start == startText) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

// ShutdownHelper removes the helper's directory. A process that scanned calls it before it exits (a test binary, from TestMain).
func ShutdownHelper() {
	helperBinary.Lock()
	defer helperBinary.Unlock()
	if helperBinary.dir != "" {
		_ = os.RemoveAll(helperBinary.dir)
	}
	helperBinary.dir, helperBinary.path, helperBinary.err, helperBinary.done = "", "", nil, false
}

var probeCache = struct {
	sync.Mutex
	verdict map[string]error
}{verdict: map[string]error{}}

// probeKey names a package by the bytes the scan parsed, so a package already probed in this process is not probed again.
func probeKey(group []parsedFile) string {
	digest := sha256.New()
	for _, parsed := range group {
		fmt.Fprintf(digest, "%s\x00%d\x00", parsed.rel, len(parsed.src))
		digest.Write(parsed.src)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// scanHelperCPU is the CPU time all helpers of one scan may use together; when it is spent, every package that still needs a
// helper is refused. The real tree uses a small fraction of it.
var scanHelperCPU = 60 * time.Second

var errHelperCPUBudget = errors.New("type-check helpers used the CPU budget of this scan")

func probeTypeCheck(ctx context.Context, budget *atomic.Int64, root string, group []parsedFile, exports map[string]string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	req := tcheck.Request{Root: root, Exports: exports, CPUSeconds: childCPUSeconds, Sources: map[string][]byte{}}
	for _, parsed := range group {
		req.Rels = append(req.Rels, parsed.rel)
		req.Sources[parsed.rel] = parsed.src
	}
	key := probeKey(group)
	probeCache.Lock()
	cached, seen := probeCache.verdict[key]
	probeCache.Unlock()
	if seen {
		return cached
	}
	if budget != nil && budget.Load() <= 0 {
		return errHelperCPUBudget
	}
	verdict, cpu := runChild(ctx, root, req)
	if verdict == nil {
		noteChildStat(&childStats.nodes, int64(syntaxNodes(group)))
	}
	if budget != nil {
		budget.Add(-cpu)
	}
	if ctx.Err() == nil {
		probeCache.Lock()
		probeCache.verdict[key] = verdict
		probeCache.Unlock()
	}
	return verdict
}

// cappedBuffer keeps at most limit bytes and remembers that it overflowed.
type cappedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > c.limit {
		c.overflow = true
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

// runChild runs the helper on one request. Only an exit status 0 with exactly the acknowledgement line of this request
// is a pass; a start failure, a non-zero exit, a signal, a timeout, a memory kill, a truncated, oversized or foreign
// output is an error.
func runChild(ctx context.Context, root string, req tcheck.Request) (verdict error, cpuNanos int64) {
	binary, err := childBinaryPath(ctx, root)
	if err != nil {
		if !errors.Is(err, errHelperUnavailable) {
			err = fmt.Errorf("%w: %v", errHelperUnavailable, err)
		}
		return err, 0
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err, 0
	}
	if len(payload) > maxRequestBytes {
		return fmt.Errorf("%w: request is %d bytes, bound is %d bytes", errRequestTooLarge, len(payload), maxRequestBytes), 0
	}
	sum := sha256.Sum256(payload)
	want := "emitscan-tcheck-ok " + hex.EncodeToString(sum[:]) + "\n"
	ctx, cancel := context.WithTimeout(ctx, childWall)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = childEnvironment()
	cmd.Stdin = bytes.NewReader(payload)
	out := &cappedBuffer{limit: 4096}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %v", errHelperUnavailable, err), 0
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var waitErr error
	killed := false
	var polledPeak int64
loop:
	for {
		select {
		case waitErr = <-done:
			break loop
		case <-ticker.C:
			rss, peak := residentBytes(cmd.Process.Pid)
			if peak > polledPeak {
				polledPeak = peak
			}
			if !killed && memoryExceeded(rss, peak, childRSSLimit) {
				killed = true
				_ = cmd.Process.Kill()
			}
		}
	}
	cpuNanos = usedCPU(cmd.ProcessState)
	switch {
	case killed || memoryExceeded(0, polledPeak, childRSSLimit):
		return errChildLimit, cpuNanos
	case ctx.Err() != nil:
		return fmt.Errorf("type-check helper stopped: %w", ctx.Err()), cpuNanos
	case waitErr != nil:
		return fmt.Errorf("type-check helper: %w", waitErr), cpuNanos
	case out.overflow || out.String() != want:
		return errors.New("type-check helper answered with something other than the acknowledgement of its request"), cpuNanos
	}
	noteChildStat(&childStats.rssKB, polledPeak>>10)
	recordUsage(cmd.ProcessState) // only a helper that finished inside its limits counts toward the limits' headroom
	return nil, cpuNanos
}

// maxRequestBytes is below the helper's stdin cap (64 MiB, tcheck/child/main.go) so a large package is a refusal here and never a
// silently truncated request.
var maxRequestBytes = 48 << 20

var errRequestTooLarge = errors.New("type-check helper request too large")

var errChildLimit = errors.New("type-check stopped at its memory limit")

// memoryExceeded is true when the helper's resident set is over the limit now or was at its peak (a short spike between two
// polls still shows in the kernel's high-water mark).
func memoryExceeded(rss, peak, limit int64) bool { return rss > limit || peak > limit }

// RunHelperEnvProbe is a test hook: it runs the helper in env mode and returns the names of the variables it received.
func runChildMode(ctx context.Context, root, mode string) (string, error) {
	binary, err := childBinaryPath(ctx, root)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(tcheck.Request{Mode: mode})
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = childEnvironment()
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	return string(out), err
}
