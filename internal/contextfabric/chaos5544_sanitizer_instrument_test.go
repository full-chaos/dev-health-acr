package contextfabric

// THE INSTRUMENT, not a hand list.
//
// CHAOS-5544's first two commits fixed the four named sites its own commit
// message claimed were the whole surface. A codex review round found two
// more, independently-drifted sinks the claim missed (chaos4171_offer_phrasing.go,
// graphrank/tracer.go), and a SECOND round -- after those were fixed --
// found MORE still: telemetry.go's own served_request_id/source_result_id,
// graphrank's Subject.CanonicalID at eleven sites, and falkorgraph's own
// unsanitized request-id sink. Three rounds, three new cells, because every
// prior pass enumerated the surface BY HAND (a grep for known field names,
// a doc comment asserting "safe to log directly") rather than from the
// producer.
//
// This enumerates from the producer: every slog attribute value logged
// anywhere under internal/contextfabric/... (this package and every
// subpackage) that is (a) string-typed, (b) not a compile-time constant,
// and (c) not wrapped by a call to SanitizeLogAttr is a FAILURE, named by
// path:line. Nothing is exempted by name -- the only allowlist is by TYPE:
// a named string type (a closed enum, e.g. OfferPhrasingOutcome), even
// when explicitly converted via string(x), and any bool/numeric-typed
// value are not flagged, because there is nothing free-text about them to
// forge a log line with. A new raw string log site added anywhere in this
// package family fails this test the moment it lands, not on the next
// adversarial review round.
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

func chaos5544IsSanitizeCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == "SanitizeLogAttr"
	case *ast.SelectorExpr:
		return fn.Sel.Name == "SanitizeLogAttr"
	}
	return false
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
func chaos5544Classify(expr ast.Expr, info *types.Info, enumTypes map[*types.Named]bool) string {
	if call, ok := expr.(*ast.CallExpr); ok {
		if inner, isConv := chaos5544IsStringConversion(call, info); isConv {
			return chaos5544Classify(inner, info, enumTypes)
		}
		if chaos5544IsSanitizeCall(call) {
			return ""
		}
	}
	t := info.TypeOf(expr)
	if t == nil {
		return ""
	}
	basic, isBasic := t.Underlying().(*types.Basic)
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
// TestChaos5544SanitizerInstrumentCatchesAnUnwrappedSite's fixture-package
// exercise below.
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
		if start >= len(call.Args) || call.Ellipsis != token.NoPos {
			return
		}
		rest := call.Args[start:]
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
		t.Errorf("%d unsanitized string log attribute(s) found -- every one must route through "+
			"SanitizeLogAttr (bare in this package, contextfabric.SanitizeLogAttr elsewhere) before "+
			"it becomes a log attribute value:", len(findings))
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
