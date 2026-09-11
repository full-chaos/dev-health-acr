package contextfabric

// THE INSTRUMENT, not a hand list.
//
// CHAOS-5544's first two commits fixed the four named sites its own commit
// message claimed were the whole surface. A codex review round found two
// more, independently-drifted sinks the claim missed (chaos4171_offer_phrasing.go,
// graphrank/tracer.go), a SECOND round -- after those were fixed -- found
// MORE still (telemetry.go's stored ids, graphrank's Subject.CanonicalID,
// falkorgraph's own request-id sink), and a THIRD round found three
// distinct blind spots in the INSTRUMENT ITSELF rather than in the
// production code: (1) a []string attribute (graphrank/tracer.go's
// top_ids/fired_ids/eliminated_ids) was invisible because the classifier
// only inspected scalar string types; (2) seven sites built their `[]any`
// attribute slice across several `append` calls and spread it
// (`logger.Info(msg, attrs...)`), a shape the scanner explicitly skipped
// because a spread's contents are not enumerable at the call site; (3) the
// scanner identified a "sanitizer" call by NAME ONLY, so a same-named
// impostor function anywhere would have silently passed.
//
// This enumerates from the producer: every slog attribute value logged
// anywhere under internal/contextfabric/... (this package and every
// subpackage) that is (a) string- or []string-typed, (b) not a
// compile-time constant, and (c) not wrapped by a call that RESOLVES (by
// types.Object identity, never by name) to this package's own
// SanitizeLogAttr/SanitizeLogStrings/SanitizeLogAttrs is a FAILURE, named
// by path:line. A spread argument (`attrs...`) that does not resolve to
// SanitizeLogAttrs is a FAILURE at the spread site itself. Nothing is
// exempted by name -- the only allowlist is by TYPE: a named string type
// (a closed enum, e.g. OfferPhrasingOutcome) whose OWN declaring package
// declares at least one const of that type, and any bool/numeric-typed
// value, are not flagged, because there is nothing free-text about them to
// forge a log line with. A new raw string/[]string log site, or a new
// unwrapped spread, added anywhere in this package family fails this test
// the moment it lands, not on the next adversarial review round.
import (
	"bytes"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"golang.org/x/tools/go/packages"
)

var chaos5544LoggerMethods = map[string]bool{
	"Info": true, "Warn": true, "Error": true, "Debug": true,
	"InfoContext": true, "WarnContext": true, "ErrorContext": true, "DebugContext": true,
}

// chaos5544ContextFabricPkgPath is the ONE package whose SanitizeLogAttr/
// SanitizeLogStrings/SanitizeLogAttrs count as the real barrier. Identity
// is resolved through go/types (info.Uses -> *types.Func -> Pkg().Path()),
// never by matching the callee's NAME -- an r3 review round found that a
// same-named function in a different package (or, worse, a decoy planted
// in this very file) passed the old name-only check.
const chaos5544ContextFabricPkgPath = "github.com/full-chaos/dev-health-acr/internal/contextfabric"

func chaos5544ResolveCalleeFunc(call *ast.CallExpr, info *types.Info) *types.Func {
	var ident *ast.Ident
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		ident = fn
	case *ast.SelectorExpr:
		ident = fn.Sel
	default:
		return nil
	}
	fn, _ := info.Uses[ident].(*types.Func)
	return fn
}

func chaos5544IsRealBarrierCall(call *ast.CallExpr, info *types.Info, name string) bool {
	fn := chaos5544ResolveCalleeFunc(call, info)
	return fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == chaos5544ContextFabricPkgPath && fn.Name() == name
}

func chaos5544IsStringConversion(call *ast.CallExpr, info *types.Info) (ast.Expr, bool) {
	id, ok := call.Fun.(*ast.Ident)
	if !ok || id.Name != "string" || len(call.Args) != 1 {
		return nil, false
	}
	if obj := info.Uses[id]; obj != nil {
		if _, isBuiltin := obj.(*types.Builtin); !isBuiltin {
			return nil, false
		}
	}
	return call.Args[0], true
}

// chaos5544Classify returns "" if expr needs no sanitizer, else a reason.
// Handles both scalar string values (SanitizeLogAttr) and []string values
// (SanitizeLogStrings), each identity-resolved, never name-matched.
func chaos5544Classify(expr ast.Expr, info *types.Info, enumTypes map[*types.Named]bool) string {
	if call, ok := expr.(*ast.CallExpr); ok {
		if inner, isConv := chaos5544IsStringConversion(call, info); isConv {
			return chaos5544Classify(inner, info, enumTypes)
		}
		if chaos5544IsRealBarrierCall(call, info, "SanitizeLogAttr") {
			return ""
		}
		if chaos5544IsRealBarrierCall(call, info, "SanitizeLogStrings") {
			return ""
		}
	}
	t := info.TypeOf(expr)
	if t == nil {
		return ""
	}
	u := t.Underlying()

	// []string (or a named type over []string): needs SanitizeLogStrings.
	if slice, isSlice := u.(*types.Slice); isSlice {
		elemBasic, elemIsBasic := slice.Elem().Underlying().(*types.Basic)
		if !elemIsBasic || (elemBasic.Kind() != types.String && elemBasic.Kind() != types.UntypedString) {
			return "" // a slice of something other than string -- out of scope
		}
		if named, isNamed := t.(*types.Named); isNamed && enumTypes[named] {
			return "" // a closed enum slice type with its own consts (unusual, but the same rule)
		}
		return "unsanitized []string log attribute"
	}

	basic, isBasic := u.(*types.Basic)
	if !isBasic {
		return ""
	}
	if basic.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsUnsigned) != 0 {
		return ""
	}
	if basic.Kind() != types.String && basic.Kind() != types.UntypedString {
		return ""
	}
	// A named string type counts as a closed enum ONLY if the type's own
	// declaring package declares at least one `const` of that type -- a
	// named type with zero constants (observability.RequestID is exactly
	// this: `type RequestID string`, no const block) is an opaque
	// identifier wrapper, not a bounded vocabulary, and free text can
	// still reach it. Named-type-alone was tried and was wrong: it
	// silently allowed observability.RequestID straight through
	// graphRequestIDLogAttrs (falkorgraph/config.go), an r2 finding.
	if named, isNamed := t.(*types.Named); isNamed && enumTypes[named] {
		return ""
	}
	if tv, ok := info.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return "" // a literal/constant
	}
	return "unsanitized string log attribute"
}

// chaos5544ScanForUnsanitizedLogAttrs walks every package under root
// (an import-path pattern ending in /...) and returns one finding per
// violation, sorted by file:line. Shared between the failing test and
// the fixture-package exercises below.
func chaos5544ScanForUnsanitizedLogAttrs(t *testing.T, dir, pattern string) []string {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		t.Fatalf("packages.Load(%q): %v", pattern, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("%d package error(s) loading %q -- the instrument cannot see a tree that does not compile", n, pattern)
	}

	// Build the enum-type set: every *types.Named whose declaring package
	// declares at least one `const` of that exact type, walked across the
	// whole loaded package graph (roots + every transitive import), not
	// just the packages under scan -- a type like OfferPhrasingOutcome is
	// USED under internal/contextfabric but DECLARED there too, while a
	// type declared in an imported package (contracts/v1, observability)
	// needs its own package's scope inspected.
	enumTypes := map[*types.Named]bool{}
	seenPkgs := map[*types.Package]bool{}
	var walkPkg func(p *types.Package)
	walkPkg = func(p *types.Package) {
		if p == nil || seenPkgs[p] {
			return
		}
		seenPkgs[p] = true
		scope := p.Scope()
		for _, name := range scope.Names() {
			if c, ok := scope.Lookup(name).(*types.Const); ok {
				if named, isNamed := c.Type().(*types.Named); isNamed {
					enumTypes[named] = true
				}
			}
		}
		for _, imp := range p.Imports() {
			walkPkg(imp)
		}
	}
	for _, pkg := range pkgs {
		walkPkg(pkg.Types)
	}

	type found struct {
		pos token.Position
	}
	var findings []found

	report := func(fset *token.FileSet, expr ast.Expr, info *types.Info) {
		if reason := chaos5544Classify(expr, info, enumTypes); reason != "" {
			findings = append(findings, found{pos: fset.Position(expr.Pos())})
		}
	}

	inspectSlogBuilder := func(fset *token.FileSet, info *types.Info, call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "slog" {
			return
		}
		if (sel.Sel.Name == "String" || sel.Sel.Name == "Any") && len(call.Args) == 2 {
			report(fset, call.Args[1], info)
		}
	}

	inspectCompositeLit := func(fset *token.FileSet, info *types.Info, cl *ast.CompositeLit) {
		arr, ok := cl.Type.(*ast.ArrayType)
		if !ok || arr.Len != nil {
			return
		}
		isEmptyIface := false
		switch elt := arr.Elt.(type) {
		case *ast.InterfaceType:
			isEmptyIface = elt.Methods == nil || len(elt.Methods.List) == 0
		case *ast.Ident:
			isEmptyIface = elt.Name == "any" // `any` is a bare identifier, not an inline interface, in the AST
		}
		if !isEmptyIface {
			return
		}
		for i := 0; i+1 < len(cl.Elts); i += 2 {
			keyLit, isBasicLit := cl.Elts[i].(*ast.BasicLit)
			if !isBasicLit || keyLit.Kind != token.STRING {
				continue
			}
			report(fset, cl.Elts[i+1], info)
		}
	}

	// inspectLoggerCall covers BOTH shapes: a flat, non-spread key/value arg
	// list, and a spread (`attrs...`) -- which, since r3, is REQUIRED to be
	// exactly a call resolving to the real SanitizeLogAttrs, not skipped.
	inspectLoggerCall := func(fset *token.FileSet, info *types.Info, call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !chaos5544LoggerMethods[sel.Sel.Name] {
			return
		}
		isCtx := len(sel.Sel.Name) > 7 && sel.Sel.Name[len(sel.Sel.Name)-7:] == "Context"
		start := 1
		if isCtx {
			start = 2
		}
		if start >= len(call.Args) {
			return
		}
		rest := call.Args[start:]

		if call.Ellipsis != token.NoPos {
			// A spread: exactly one argument, and it must resolve to the
			// real SanitizeLogAttrs -- a bare variable, a different
			// function's result, or an impostor are all failures HERE,
			// at the spread site, since the slice's own contents are a
			// runtime value this static walk cannot enumerate.
			if len(rest) != 1 {
				return
			}
			spreadCall, isCall := rest[0].(*ast.CallExpr)
			if isCall && chaos5544IsRealBarrierCall(spreadCall, info, "SanitizeLogAttrs") {
				return
			}
			findings = append(findings, found{pos: fset.Position(rest[0].Pos())})
			return
		}

		for i := 0; i+1 < len(rest); i += 2 {
			keyLit, isBasicLit := rest[i].(*ast.BasicLit)
			if !isBasicLit || keyLit.Kind != token.STRING {
				continue
			}
			report(fset, rest[i+1], info)
		}
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					inspectSlogBuilder(pkg.Fset, pkg.TypesInfo, node)
					inspectLoggerCall(pkg.Fset, pkg.TypesInfo, node)
				case *ast.CompositeLit:
					inspectCompositeLit(pkg.Fset, pkg.TypesInfo, node)
				}
				return true
			})
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].pos.Filename != findings[j].pos.Filename {
			return findings[i].pos.Filename < findings[j].pos.Filename
		}
		return findings[i].pos.Line < findings[j].pos.Line
	})
	out := make([]string, len(findings))
	for i, f := range findings {
		out[i] = f.pos.String()
	}
	return out
}

// repoRoot walks up from this test file's own directory to the module
// root (the directory carrying go.mod), so the scan works regardless of
// the working directory `go test` is invoked from.
func chaos5544RepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root (go.mod) walking up from " + thisFile)
		}
		dir = parent
	}
}

// TestNoUnsanitizedLogAttributeInContextFabric is THE GATE: zero tolerance,
// enumerated from the producer every time this test runs.
func TestNoUnsanitizedLogAttributeInContextFabric(t *testing.T) {
	root := chaos5544RepoRoot(t)
	findings := chaos5544ScanForUnsanitizedLogAttrs(t, root,
		"github.com/full-chaos/dev-health-acr/internal/contextfabric/...")
	if len(findings) != 0 {
		t.Errorf("%d unsanitized string/[]string log attribute(s) or unwrapped spread(s) found -- "+
			"every one must route through SanitizeLogAttr/SanitizeLogStrings (a value) or "+
			"SanitizeLogAttrs (a spread), bare in this package, contextfabric.-qualified elsewhere, "+
			"before it becomes a log attribute value:", len(findings))
		for _, f := range findings {
			t.Errorf("  %s", f)
		}
	}
}

// TestChaos5544SanitizerInstrumentCatchesAnUnwrappedSite proves the gate
// above is not a test that cannot fail: it plants a throwaway, uncommitted
// fixture package with ONE deliberately unwrapped string log attribute and
// asserts the SAME scanner reports exactly it.
func TestChaos5544SanitizerInstrumentCatchesAnUnwrappedSite(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID string) {
	logger.Info("fixture line", "request_id", requestID)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5544\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modSrc), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	formatted, err := format.Source([]byte(src))
	if err != nil {
		t.Fatalf("fixture does not gofmt clean: %v", err)
	}
	if !bytes.Equal(formatted, []byte(src)) {
		t.Fatalf("fixture source is not gofmt-normalized")
	}

	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, "./...")
	if len(findings) != 1 {
		t.Fatalf("the instrument found %d finding(s) in a fixture with exactly one unwrapped site "+
			"(request_id in LogIt) -- it must find exactly one, proving it can fail: %v",
			len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:6 (the request_id argument)", findings[0])
	}
}

// TestChaos5544SanitizerInstrumentCatchesAnUnwrappedSpread is
// CatchesAnUnwrappedSite's sibling for the r3 spread class: a fixture
// builds its []any attrs slice across two `append` calls, exactly the
// shape the seven r3 production sites used, and spreads it unwrapped.
func TestChaos5544SanitizerInstrumentCatchesAnUnwrappedSpread(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, extra string) {
	attrs := []any{"request_id", "req_fixed"}
	attrs = append(attrs, "extra", extra)
	logger.Info("fixture line", attrs...)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5544spread\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modSrc), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	formatted, err := format.Source([]byte(src))
	if err != nil {
		t.Fatalf("fixture does not gofmt clean: %v", err)
	}
	if !bytes.Equal(formatted, []byte(src)) {
		t.Fatalf("fixture source is not gofmt-normalized")
	}

	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, "./...")
	if len(findings) != 1 {
		t.Fatalf("the instrument found %d finding(s) in a fixture with exactly one unwrapped spread "+
			"-- it must find exactly one, proving the spread class can fail: %v", len(findings), findings)
	}
}

// TestChaos5544SanitizerInstrumentResolvesRealFunctionIdentity is the r3 P3
// pin: a fixture plants its OWN function named SanitizeLogAttr (a no-op
// impostor, in a package that is NOT github.com/full-chaos/dev-health-acr/
// internal/contextfabric) and asserts the scanner still reports the site --
// proving identity is resolved by types.Object, never by matching the
// callee's name string.
func TestChaos5544SanitizerInstrumentResolvesRealFunctionIdentity(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

// SanitizeLogAttr is a same-named IMPOSTOR: a genuine no-op, not this
// repo's contextfabric.SanitizeLogAttr. A name-only identity check would
// wrongly treat this as the real barrier.
func SanitizeLogAttr(s string) string { return s }

func LogIt(logger *slog.Logger, requestID string) {
	logger.Info("fixture line", "request_id", SanitizeLogAttr(requestID))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5544impostor\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modSrc), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	formatted, err := format.Source([]byte(src))
	if err != nil {
		t.Fatalf("fixture does not gofmt clean: %v", err)
	}
	if !bytes.Equal(formatted, []byte(src)) {
		t.Fatalf("fixture source is not gofmt-normalized")
	}

	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, "./...")
	if len(findings) != 1 {
		t.Fatalf("the instrument found %d finding(s) in a fixture whose ONLY sanitizer call is an "+
			"impostor (same name, wrong package) -- it must still report the site as unsanitized: %v",
			len(findings), findings)
	}
}
