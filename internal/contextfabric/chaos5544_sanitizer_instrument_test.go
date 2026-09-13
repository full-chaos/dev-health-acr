package contextfabric

// THE INSTRUMENT, not a hand list.
//
// CHAOS-5544's first two commits fixed the four named sites its own commit
// message claimed were the whole surface. A codex review round found two
// more, independently-drifted sinks the claim missed (chaos4171_offer_phrasing.go,
// graphrank/tracer.go), a SECOND round -- after those were fixed -- found
// MORE still (telemetry.go's stored ids, graphrank's Subject.CanonicalID,
// falkorgraph's own request-id sink), and a THIRD round found three
// distinct blind spots in the INSTRUMENT ITSELF: (1) a []string attribute
// was invisible because the classifier only inspected scalar string types;
// (2) seven sites built their `[]any` attribute slice across several
// `append` calls and spread it, a shape the scanner explicitly skipped;
// (3) the scanner identified a "sanitizer" call by NAME ONLY. The FIRST
// fix for (2) -- wrap the whole spread in one SanitizeLogAttrs([]any)
// []any barrier -- was itself found wrong by the PR-ref CodeQL gate
// (never a review round) before merge: CodeQL's array-level taint model
// conflates a request-derived NUMBER passing through that function's
// type-switch default case with an unsanitized STRING, because it cannot
// see that the default branch is a no-op specifically because the value
// cannot carry a forged line break. A function with the shape func([]any)
// []any is, for CodeQL's purposes, no better a barrier than the original
// rune-remap loop this whole ticket exists to replace.
//
// So values are sanitized at their OWN construction site (a composite
// literal element, or an `append` call's key/value argument), never at
// the spread boundary -- SanitizeLogAttr/SanitizeLogStrings called
// directly on an OWN string/[]string expression is a shape CodeQL DOES
// recognize, at every tip already proven clean. A spread's OWN
// requirement is narrower: its argument's construction must be
// STATICALLY TRACEABLE to composite literals and simple append
// reassignments (each already checked by the rules above) within the
// same function, or to a spread of a call to another function inside
// this same scanned tree (whose own body is checked wherever it lives).
// An opaque spread -- a parameter, a field, a call to something outside
// the scanned tree -- is a FAILURE, reported at the spread site, never
// silently trusted.
//
// This enumerates from the producer: every slog attribute value logged
// anywhere under internal/contextfabric/... (this package and every
// subpackage) that is (a) string- or []string-typed, (b) not a
// compile-time constant, and (c) not wrapped by a call that RESOLVES (by
// types.Object identity, never by name) to this package's own
// SanitizeLogAttr/SanitizeLogStrings is a FAILURE, named by path:line.
// Nothing is exempted by name -- the only allowlist is by TYPE: a named
// string type (a closed enum, e.g. OfferPhrasingOutcome) whose OWN
// declaring package declares at least one const of that type, and any
// bool/numeric-typed value, are not flagged, because there is nothing
// free-text about them to forge a log line with.
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

// chaos5544LoggerMethods maps a slog logger method name to the index of
// its first variadic/key-value argument. r3 review round (CHAOS-5558)
// found the PRIOR map (bool-valued, with a "does the name end in
// Context" heuristic computing the start index) omitted `Log`
// ((*slog.Logger).Log(ctx, level, msg, args...)) entirely -- a real,
// if currently unused inside internal/contextfabric, logger entry point.
// `Log` also does not fit the two-shape heuristic (start=1 for a plain
// name, start=2 for a "...Context" name): its own args start at index 3
// (ctx, level, msg), a THIRD shape the heuristic had no way to express.
// Explicit per-name start indices replace the heuristic entirely, the
// same identity-over-inference preference this file already applies
// everywhere else.
var chaos5544LoggerMethods = map[string]int{
	"Info": 1, "Warn": 1, "Error": 1, "Debug": 1,
	"InfoContext": 2, "WarnContext": 2, "ErrorContext": 2, "DebugContext": 2,
	"Log": 3,
}

// chaos5544ContextFabricPkgPath is the ONE package whose SanitizeLogAttr/
// SanitizeLogStrings count as the real barrier. Identity is resolved
// through go/types (info.Uses -> *types.Func -> Pkg().Path()), never by
// matching the callee's NAME -- an r3 review round found that a
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
	// The predeclared type "string" resolves through info.Uses to the
	// UNIVERSE scope's own *types.TypeName object (never *types.Builtin --
	// that kind is for builtin FUNCTIONS like append/len, not predeclared
	// types). Comparing identity against types.Universe.Lookup("string")
	// -- rather than merely checking "not obviously a function" -- is what
	// keeps a local variable or parameter literally named `string` (an
	// impostor shadowing the predeclared type) from being misread as this
	// conversion.
	if info.Uses[id] != types.Universe.Lookup("string") {
		return nil, false
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
	// Unalias FIRST: a `type X = Y` alias (SubjectKind = contractsv1.
	// ContextFabricSubjectKind is exactly this shape) type-checks, from Go
	// 1.23's materialized aliases onward, to its own *types.Alias node --
	// a plain `t.(*types.Named)` assertion below would fail on it even
	// though the aliased type itself is a real, const-bearing enum.
	// types.Unalias walks through any number of alias layers to the
	// underlying *types.Named (or other) type the alias ultimately names.
	t = types.Unalias(t)
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

	// r3 review round (CHAOS-5558) found a value with static type `any`
	// fell through every check above to "" (unflagged) purely because it
	// isn't *types.Basic -- FAIL OPEN, and a genuine blind spot: a string
	// erased into an `any`-typed variable or parameter before it reaches a
	// []any{...}/append pair is indistinguishable, to this static walk,
	// from a bool/struct/anything-else typed value that legitimately
	// carries no free text. Scoped to the EMPTY interface specifically
	// (NumMethods() == 0 -- `any`/`interface{}`), not every named
	// interface: a value statically typed `error` (or another non-empty
	// interface) is a real, pre-existing, differently-remediated concern
	// in this codebase (raw error text as a log attribute has its own
	// documented rule and helper, e.g. classifyWriteError) -- conflating
	// the two produced findings at genuinely unrelated production sites
	// this ticket does not own, executed-confirmed while building this
	// fix. An IndexExpr (`values[i]`) is exempted for the same reason a
	// CompositeLit's own elements are: genkitruntime's attemptLogFields
	// stages values in a []any COMPOSITE LITERAL (already checked
	// directly, by Rule A, at ITS OWN construction) and then reads them
	// back by index -- the read itself carries no NEW content the literal
	// didn't already expose to this same scan.
	if iface, isInterface := u.(*types.Interface); isInterface && iface.NumMethods() == 0 {
		if _, isIndex := expr.(*ast.IndexExpr); !isIndex {
			return "unproven any-typed log attribute -- cannot statically verify it carries no free text"
		}
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
	// whole loaded package graph (roots + every transitive import).
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
				if named, isNamed := types.Unalias(c.Type()).(*types.Named); isNamed {
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

	// funcDecl bundles a *ast.FuncDecl declared in a ROOT package with the
	// go/token/go/types plumbing (its own package's Fset/TypesInfo) needed
	// to check it in isolation.
	type funcDecl struct {
		decl *ast.FuncDecl
		fset *token.FileSet
		info *types.Info
	}
	// localFuncs maps every *types.Func declared in a ROOT package (one of
	// the scanned pkgs' own Syntax, not merely imported) to its
	// declaration. A spread of THIS function's call result is trusted
	// only after RECURSING into its own return statements (see
	// checkLocalFuncReturnsSafety below) -- trusting it by identity alone
	// was tried and was wrong: attemptLogFields (genkitruntime/runtime.go)
	// builds a values-then-append-by-index []any internally, and a bare
	// identity check let that internal construction go completely
	// unchecked.
	localFuncs := map[*types.Func]funcDecl{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok {
					if obj, ok := pkg.TypesInfo.Defs[fd.Name].(*types.Func); ok {
						localFuncs[obj] = funcDecl{decl: fd, fset: pkg.Fset, info: pkg.TypesInfo}
					}
				}
			}
		}
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

	// checkPairs is used below by inspectSlogBuilder's Group case; declared
	// here (var, assigned further down where its own implementation lives)
	// so the two can reference each other regardless of source order.
	var checkPairs func(fset *token.FileSet, info *types.Info, elts []ast.Expr)
	// checkGroupArgs is Group's own arg scanner, forward-declared for the
	// same reason (assigned after chaos5544IsOpaqueAttrCallResult, which
	// it needs, is defined below).
	var checkGroupArgs func(fset *token.FileSet, info *types.Info, args []ast.Expr)

	// inspectSlogBuilder covers every slog constructor that can carry an
	// unsanitized value THROUGH a static type (slog.Value, slog.Attr) the
	// scalar/[]string classifier above cannot see into on its own --
	// CHAOS-5558's builder-coverage gap (#497 r3's own latent P3): String/
	// Any were the original two; Group/StringValue/AnyValue are net new.
	// slog.String(k,v)/slog.Any(k,v) produce a slog.Attr directly and were
	// already covered; slog.StringValue(v)/slog.AnyValue(v) produce a bare
	// slog.Value -- used inside a slog.Attr{Key:..., Value: ...} composite
	// literal or passed to LogAttrs/AddAttrs -- which chaos5544Classify
	// cannot classify at all (its static type is a struct, not string), so
	// without this the wrapped value slips through invisibly. slog.Group's
	// own variadic args alternate key/value exactly like a logger call's
	// flat arg list, so its OWN pairs need the same checkPairs pass a
	// logger call gets; a Group whose sole arg is a pre-built []slog.Attr
	// (the OTHER documented Group shape) is not handled here -- no
	// production or fixture site uses it, and doing so soundly needs the
	// same []slog.Attr tracing LogAttrs/AddAttrs below already do.
	// chaos5544ResolveSlogBuiltinName resolves call's callee to a function
	// IDENTITY in package "log/slog" and returns its name, covering BOTH
	// shapes the callee can take: a qualified selector (`slog.Group(...)`,
	// or any local alias -- `sel.X` resolves through info.Uses to a
	// *types.PkgName whose Imported() names the real path regardless of
	// the alias, r1's own fix) AND a bare identifier from a DOT IMPORT
	// (`import . "log/slog"; Group(...)`), which is not a SelectorExpr at
	// all -- r2 review round found this second shape entirely unhandled,
	// executed-confirmed (findings: [] on a dot-imported Group call with a
	// genuinely unwrapped value). info.Uses on the bare identifier
	// resolves directly to the *types.Func for the dot-imported name.
	chaos5544ResolveSlogBuiltinName := func(call *ast.CallExpr, info *types.Info) (string, bool) {
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			pkgIdent, ok := fn.X.(*ast.Ident)
			if !ok {
				return "", false
			}
			pkgName, isPkgName := info.Uses[pkgIdent].(*types.PkgName)
			if !isPkgName || pkgName.Imported().Path() != "log/slog" {
				return "", false
			}
			return fn.Sel.Name, true
		case *ast.Ident:
			obj, isFunc := info.Uses[fn].(*types.Func)
			if !isFunc || obj.Pkg() == nil || obj.Pkg().Path() != "log/slog" {
				return "", false
			}
			return obj.Name(), true
		default:
			return "", false
		}
	}
	inspectSlogBuilder := func(fset *token.FileSet, info *types.Info, call *ast.CallExpr) {
		name, ok := chaos5544ResolveSlogBuiltinName(call, info)
		if !ok {
			return
		}
		switch {
		case (name == "String" || name == "Any") && len(call.Args) == 2:
			report(fset, call.Args[1], info)
		case (name == "StringValue" || name == "AnyValue") && len(call.Args) == 1:
			report(fset, call.Args[0], info)
		case name == "Group" && len(call.Args) >= 1:
			checkGroupArgs(fset, info, call.Args[1:])
		}
	}

	// isOpaqueAttrSpread and inspectAttrSpreadCall are CHAOS-5558's other
	// half of the builder-coverage gap: LogAttrs/AddAttrs, whose own
	// variadic parameter is []slog.Attr, not []any. Every ELEMENT of such a
	// slice is independently visible to the unconditional per-CallExpr walk
	// below regardless of nesting (a slog.String/Any/Group/StringValue/
	// AnyValue call inside a composite literal, an append, anywhere) --
	// unlike the []any Rule A/D/E machinery above, no backward trace is
	// needed to SEE those constructor calls, because []slog.Attr has no
	// other legal way to acquire a string-carrying value. What static
	// tracing cannot see is a spread whose slice was built OUTSIDE this
	// function entirely (a parameter, a field, a call result) -- exactly
	// the opaque-spread failure mode Rule already enforces for []any,
	// applied here to the narrower question "was this spread constructed
	// where we can see it at all", not "is every element individually
	// safe" (that part is already covered for free).
	// chaos5544IsOpaqueAttrCallResult decides whether a CallExpr producing
	// (or contributing to) an slog.Attr/[]slog.Attr value has a
	// construction this walk can actually see into. r2 review round found
	// the PRIOR version of isOpaqueAttrSpread trusted ANY CallExpr
	// unconditionally ("constructed right here") -- true only for a LOCAL
	// function (whose own body this walk separately scans) or a
	// recognized slog builder (String/Any/Group/StringValue/AnyValue,
	// each already checked unconditionally wherever it appears); a call to
	// an EXTERNAL, non-builder function returning a pre-built slog.Attr
	// from unsanitized data is exactly as opaque as a bare parameter, and
	// was previously invisible -- executed-confirmed (findings: [] on a
	// fixture where a helper.MakeAttr(requestID) declared OUTSIDE the
	// scanned tree wraps an unsanitized value in slog.String and returns
	// it as a plain slog.Attr).
	chaos5544IsOpaqueAttrCallResult := func(call *ast.CallExpr, info *types.Info) bool {
		fn := chaos5544ResolveCalleeFunc(call, info)
		if fn == nil {
			return true
		}
		if _, isLocal := localFuncs[fn]; isLocal {
			return false // this function's OWN body is independently scanned
		}
		if name, ok := chaos5544ResolveSlogBuiltinName(call, info); ok {
			switch name {
			case "String", "Any", "Group", "StringValue", "AnyValue":
				return false
			// THE NON-STRING BUILDERS. slog.Bool/Int/Int64/Uint64/Float64/
			// Duration/Time take a value parameter whose static type is not a
			// string and not a []string, so there is no corpus text for them
			// to carry and nothing for SanitizeLogAttr to do -- wrapping one
			// is not even expressible. Treating them as opaque reported a
			// false finding on every such attr spliced into a Group's arg
			// list, which is how this was found: six of them on one line of
			// the semantic-state group. They are trusted for their TYPE, not
			// for their name being on a list -- their key is a literal and
			// their value cannot be a string -- and
			// TestChaos5544SanitizerInstrumentStillCatchesAStringBesideNonStringAttrs
			// proves the gate still fires on an unwrapped string standing
			// beside them.
			case "Bool", "Int", "Int64", "Uint64", "Float64", "Duration", "Time":
				return false
			}
		}
		return true // an external, non-builder function -- opaque
	}
	// isSlogAttrType reports whether t's underlying named type is
	// log/slog.Attr specifically (not slog.Value, not any other struct).
	isSlogAttrType := func(t types.Type) bool {
		named, isNamed := types.Unalias(t).(*types.Named)
		if !isNamed || named.Obj().Pkg() == nil {
			return false
		}
		return named.Obj().Pkg().Path() == "log/slog" && named.Obj().Name() == "Attr"
	}
	// checkGroupArgs is Group's OWN arg scanner: r3 review round
	// (CHAOS-5558) found checkPairs' pure odd/even INDEX parity wrong for
	// Group specifically -- go doc: "the remaining arguments are converted
	// to Attrs as in Logger.Log", and Logger.Log's own documented rule is
	// that an argument that is ALREADY an Attr is used directly, never
	// treated as a key or a value; only non-Attr arguments are paired up
	// as key/value. A pre-built Attr spliced into a Group's arg list at
	// any position (most often via an external helper call) was
	// previously invisible: checkPairs would treat it as a "key" at an
	// even index (keys are never classified) or misclassify a REAL key at
	// the wrong parity once an Attr shifted every position after it.
	// Tracked by a state machine instead of a fixed index: an Attr-typed
	// element is checked with the SAME opaque-call-result scrutiny a
	// non-spread LogAttrs/AddAttrs argument gets and does not consume a
	// key/value slot; anything else alternates key (skipped, same as
	// checkPairs) then value (classified, same as checkPairs).
	checkGroupArgs = func(fset *token.FileSet, info *types.Info, args []ast.Expr) {
		expectingValue := false
		for _, a := range args {
			t := info.TypeOf(a)
			if t != nil && isSlogAttrType(t) {
				switch v := a.(type) {
				case *ast.CompositeLit:
					// its own fields (e.g. Value: slog.StringValue(x)) are
					// checked unconditionally wherever they appear
				case *ast.CallExpr:
					if chaos5544IsOpaqueAttrCallResult(v, info) {
						findings = append(findings, found{pos: fset.Position(a.Pos())})
					}
				default:
					findings = append(findings, found{pos: fset.Position(a.Pos())})
				}
				continue // an Attr is self-contained -- does not flip expectingValue
			}
			if expectingValue {
				report(fset, a, info)
				expectingValue = false
				continue
			}
			expectingValue = true // this element was a key -- never classified, same as checkPairs
		}
	}
	isOpaqueAttrSpread := func(expr ast.Expr, info *types.Info, funcAssigns map[types.Object][]ast.Expr) bool {
		switch e := expr.(type) {
		case *ast.CompositeLit:
			return false // constructed right here -- its own elements are independently checked
		case *ast.CallExpr:
			return chaos5544IsOpaqueAttrCallResult(e, info)
		case *ast.Ident:
			if e.Name == "nil" && info.Uses[e] == types.Universe.Lookup("nil") {
				return false
			}
			obj := info.Uses[e]
			if obj == nil {
				obj = info.Defs[e]
			}
			_, known := funcAssigns[obj]
			return !known // no recorded local assignment -- a parameter or other opaque source
		default:
			return true
		}
	}
	inspectAttrSpreadCall := func(fset *token.FileSet, info *types.Info, call *ast.CallExpr, funcAssigns map[types.Object][]ast.Expr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != "LogAttrs" && sel.Sel.Name != "AddAttrs" {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		if call.Ellipsis != token.NoPos {
			spread := call.Args[len(call.Args)-1]
			if isOpaqueAttrSpread(spread, info, funcAssigns) {
				findings = append(findings, found{pos: fset.Position(spread.Pos())})
			}
			return
		}
		// Non-spread: LogAttrs(ctx, level, msg, a1, a2, ...) / AddAttrs(a1,
		// a2, ...) -- each individual slog.Attr argument, which for
		// LogAttrs starts AFTER its own three fixed leading params
		// (ctx, level, msg -- none of which is itself an Attr and must
		// never be classified as one). A composite literal's own fields,
		// and a call to a recognized slog builder or a LOCAL function, are
		// checked unconditionally wherever they appear (same reasoning as
		// the spread case above); anything else -- an opaque call, a bare
		// identifier, a field/index expression -- is a construction this
		// walk cannot see into and must refuse, not silently trust.
		attrStart := 0
		if sel.Sel.Name == "LogAttrs" {
			attrStart = 3
		}
		if attrStart >= len(call.Args) {
			return
		}
		for _, arg := range call.Args[attrStart:] {
			switch a := arg.(type) {
			case *ast.CompositeLit:
			case *ast.CallExpr:
				if chaos5544IsOpaqueAttrCallResult(a, info) {
					findings = append(findings, found{pos: fset.Position(a.Pos())})
				}
			default:
				findings = append(findings, found{pos: fset.Position(a.Pos())})
			}
		}
	}

	isEmptyIfaceSliceType := func(typ ast.Expr) bool {
		arr, ok := typ.(*ast.ArrayType)
		if !ok || arr.Len != nil {
			return false
		}
		switch elt := arr.Elt.(type) {
		case *ast.InterfaceType:
			return elt.Methods == nil || len(elt.Methods.List) == 0
		case *ast.Ident:
			return elt.Name == "any" // `any` is a bare identifier, not an inline interface, in the AST
		}
		return false
	}

	// checkPairs reports every value at an odd index (0=key,1=value,...)
	// whose preceding element is a string literal key -- shared by
	// composite literals and append() argument lists.
	// checkPairs treats elts as a strict alternating key/value list (the
	// only shape a []any log-attrs slice is ever built in) and checks
	// every ODD-indexed VALUE, regardless of whether the key at the
	// preceding even index is a literal. An earlier version of this
	// instrument required the key to be a literal string before checking
	// its value, which silently let a genuinely unsafe value through
	// whenever the key was itself a computed expression -- e.g.
	// `prefix+"shape", string(sample.Shape)` inside a per-sample loop,
	// where the key is a string CONCATENATION, not a literal. The key's
	// own shape has no bearing on whether the value next to it is safe.
	// r3 review round (CHAOS-5558) found the loop bound (`i+1 <
	// len(elts)`) silently dropped a TRAILING unpaired element on an
	// odd-length list -- `logger.Info(msg, "k", "safe", requestID)` has
	// requestID at the final, unpaired index, and the OLD loop stopped one
	// iteration short of it. slog's own runtime does not discard that
	// element: an unpaired trailing arg is still emitted, under the
	// sentinel key `!BADKEY` (`(*Logger).Log`'s own documented handling
	// of a malformed args list) -- so it reaches the log line regardless
	// of whether this scanner checks it. The trailing odd element is
	// therefore checked the same as any other value position.
	checkPairs = func(fset *token.FileSet, info *types.Info, elts []ast.Expr) {
		for i := 0; i+1 < len(elts); i += 2 {
			report(fset, elts[i+1], info)
		}
		if len(elts)%2 == 1 {
			report(fset, elts[len(elts)-1], info)
		}
	}

	// isAppendCall reports whether call is a call to the builtin append.
	isAppendCall := func(call *ast.CallExpr, info *types.Info) bool {
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "append" {
			return false
		}
		_, isBuiltin := info.Uses[id].(*types.Builtin)
		return isBuiltin
	}

	// isEmptyIfaceSlice reports whether t is (an alias/underlying of) a
	// []any / []interface{}.
	isEmptyIfaceSlice := func(t types.Type) bool {
		slice, isSlice := t.(*types.Slice)
		if !isSlice {
			return false
		}
		iface, isIface := slice.Elem().Underlying().(*types.Interface)
		return isIface && iface.NumMethods() == 0
	}

	// variadicAnyTailStart returns the index of call's first variadic
	// argument if call's STATIC callee -- a package-level function OR a
	// local closure (a variable holding an *ast.FuncLit), resolved either
	// way through go/types -- both TAKES a final variadic `...any` (or
	// `...interface{}`) parameter AND RETURNS a single []any -- exactly
	// the shape of a hand-written per-event field-builder closure
	// (`func(extra ...any) []any`) -- and ok=false otherwise. Restricting
	// to this exact shape (not merely "any variadic-any call") keeps Rule
	// E from misreading an unrelated variadic call such as fmt.Sprintf's
	// `(format string, a ...any) string` as an alternating key/value
	// list. append and slog logger methods are excluded by their own
	// dedicated rules (D and the logger-call handling) to avoid a
	// duplicate finding at the same position.
	variadicAnyTailStart := func(call *ast.CallExpr, info *types.Info) (idx int, ok bool) {
		if isAppendCall(call, info) {
			return 0, false
		}
		if sel, isSel := call.Fun.(*ast.SelectorExpr); isSel {
			if _, isLoggerMethod := chaos5544LoggerMethods[sel.Sel.Name]; isLoggerMethod {
				return 0, false // the logger call itself: inspectLoggerCall owns this shape
			}
		}
		sig, isSig := info.TypeOf(call.Fun).(*types.Signature)
		if !isSig || !sig.Variadic() || sig.Params().Len() == 0 {
			return 0, false
		}
		if sig.Results().Len() != 1 || !isEmptyIfaceSlice(sig.Results().At(0).Type()) {
			return 0, false // must return exactly one []any
		}
		last := sig.Params().At(sig.Params().Len() - 1)
		if !isEmptyIfaceSlice(last.Type()) {
			return 0, false // not `...any`/`...interface{}`
		}
		return sig.Params().Len() - 1, true
	}

	// collectAssignsInFunc walks one function body and records, for every
	// identifier ever assigned a `[]any` literal or reassigned via
	// `ident = append(ident, ...)`, each RHS expression -- regardless of
	// which block/branch it appears in (order does not matter: every
	// write must independently be a safe shape for the identifier to be
	// trusted at all).
	collectAssignsInFunc := func(body *ast.BlockStmt, info *types.Info) map[types.Object][]ast.Expr {
		out := map[types.Object][]ast.Expr{}
		record := func(lhs ast.Expr, rhs ast.Expr) {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				return
			}
			obj := info.Defs[id]
			if obj == nil {
				obj = info.Uses[id]
			}
			if obj == nil {
				return
			}
			out[obj] = append(out[obj], rhs)
		}
		ast.Inspect(body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
					record(s.Lhs[0], s.Rhs[0])
				}
			}
			return true
		})
		return out
	}

	// checkSpreadSafety and checkLocalFuncReturnsSafety are mutually
	// recursive (a spread may resolve to a local function's call result,
	// whose own return expressions must in turn be checked the same way),
	// hence the forward `var` declarations.
	var checkSpreadSafety func(fset *token.FileSet, expr ast.Expr, info *types.Info, funcAssigns map[types.Object][]ast.Expr, visiting map[types.Object]bool, funcVisiting map[*types.Func]bool) bool
	var checkLocalFuncReturnsSafety func(fn *types.Func, funcVisiting map[*types.Func]bool) bool

	// checkLocalFuncReturnsSafety is what makes trusting "a spread of a
	// local function's call result" SOUND rather than merely identity-
	// based: it finds fn's own FuncDecl, computes ITS OWN funcAssigns, and
	// recursively checks every bare `return expr` in its body via
	// checkSpreadSafety, with a FRESH per-callee visiting set (a
	// different function's locals are a different scope) -- reporting any
	// finding along the way exactly like a top-level call would.
	//
	// Trusting fn by identity alone was tried and was wrong:
	// attemptLogFields (genkitruntime/runtime.go) stages its values in one
	// []any composite literal, THEN assembles the real key/value pairs by
	// INDEXING into it in a loop (`fields = append(fields, key,
	// values[i])`) -- a shape no rule above recognizes as unsafe, because
	// `values[i]` is a dynamically-indexed read whose static type is the
	// element type `any`, carrying no usable string/[]string information.
	// The only place the actual concrete types are still visible is the
	// values slice's OWN composite literal, which is exactly what
	// recursing into the callee's body (checkSpreadSafety's own
	// *ast.CompositeLit case) reaches.
	//
	// funcVisiting guards mutual/self recursion between local functions
	// (A calls B calls A), the same way checkSpreadSafety's `visiting`
	// guards the append accumulator cycle: a function already being
	// checked is trusted at the back-edge.
	checkLocalFuncReturnsSafety = func(fn *types.Func, funcVisiting map[*types.Func]bool) bool {
		fd, ok := localFuncs[fn]
		if !ok {
			return false
		}
		if funcVisiting[fn] {
			return true
		}
		funcVisiting[fn] = true
		defer delete(funcVisiting, fn)

		if fd.decl.Body == nil {
			return false
		}
		calleeAssigns := collectAssignsInFunc(fd.decl.Body, fd.info)
		safe := true
		ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
			ret, isReturn := n.(*ast.ReturnStmt)
			if !isReturn {
				return true
			}
			if len(ret.Results) != 1 {
				safe = false // a shape this walk cannot characterize -- refuse, never skip
				return true
			}
			if !checkSpreadSafety(fd.fset, ret.Results[0], fd.info, calleeAssigns, map[types.Object]bool{}, funcVisiting) {
				safe = false
			}
			return true
		})
		return safe
	}

	// checkSpreadSafety is Rules D+E, SCOPED: it is reached only by
	// following the actual data flow backward from a logger call's own
	// spread argument (inspectLoggerCall's ellipsis branch, below) --
	// never applied tree-wide. An earlier version ran Rule D (append) and
	// Rule E (variadic-any calls) unconditionally over every append/
	// variadic call in the whole scanned tree, which misfired on
	// completely unrelated []interface{} accumulators -- e.g.
	// falkorgraph/client.go's Redis command-argument builder
	// (`args = append(args, "GRAPH.CONSTRAINT", ..., graphKey, ...)`,
	// spread into s.db.Conn.Do, never a logger) -- reporting a Redis
	// command argument as an unsanitized log attribute. Folding the
	// per-value checks INTO the same backward trace that already proves
	// reachability means a value is only ever checked when it is
	// genuinely on a path to a slog call.
	//
	// It returns true if expr's construction is fully accounted for
	// (every value found along the way was checked directly via
	// checkPairs/report as this function walked it), false if expr is
	// opaque -- a parameter, a field, or a call this walk cannot see
	// into -- in which case the CALLER (inspectLoggerCall) reports the
	// unresolved spread site itself, never silently trusting it.
	//
	// visiting guards the natural accumulator cycle `x = append(x, ...)`:
	// Args[0] of that call is `x` itself, whose own recorded assigns
	// include this very statement -- an unguarded walk recurses forever.
	// An identifier already being traced is trusted at the back-edge
	// (every one of its assignments is independently walked by the outer
	// call that first started tracing it).
	checkSpreadSafety = func(fset *token.FileSet, expr ast.Expr, info *types.Info, funcAssigns map[types.Object][]ast.Expr, visiting map[types.Object]bool, funcVisiting map[*types.Func]bool) bool {
		switch e := expr.(type) {
		case *ast.CompositeLit:
			if !isEmptyIfaceSliceType(e.Type) {
				return false
			}
			checkPairs(fset, info, e.Elts) // Rule A's own check, reached via the trace this time
			return true
		case *ast.CallExpr:
			// make([]any, ...) (with or without an explicit length/cap):
			// every element such a call produces is the zero value of
			// `any`, i.e. nil -- there is no shape in which this call
			// alone can carry a string. Safe unconditionally; whatever
			// gets INTO the slice afterward is checked at its own
			// append/index-assignment site, not here.
			if id, isIdent := e.Fun.(*ast.Ident); isIdent && id.Name == "make" {
				if _, isBuiltin := info.Uses[id].(*types.Builtin); isBuiltin && isEmptyIfaceSlice(info.TypeOf(e)) {
					return true
				}
			}
			if isAppendCall(e, info) {
				if !checkSpreadSafety(fset, e.Args[0], info, funcAssigns, visiting, funcVisiting) {
					return false
				}
				if e.Ellipsis != token.NoPos {
					// append(dst, other...) -- validate `other` too.
					return checkSpreadSafety(fset, e.Args[len(e.Args)-1], info, funcAssigns, visiting, funcVisiting)
				}
				checkPairs(fset, info, e.Args[1:]) // Rule D: this append's own key/value tail
				return true
			}
			// A call whose own final parameter is variadic `...any` and
			// which returns a single []any (a per-event field-builder
			// closure, most often): Rule E checks that call's own
			// key/value tail directly, then -- if IT is itself a
			// spread (base(other...) rather than base("k", v)) --
			// recurses into that spread argument too.
			if start, ok := variadicAnyTailStart(e, info); ok {
				if e.Ellipsis != token.NoPos {
					if len(e.Args) == 0 {
						return false
					}
					return checkSpreadSafety(fset, e.Args[len(e.Args)-1], info, funcAssigns, visiting, funcVisiting)
				}
				if start < len(e.Args) {
					checkPairs(fset, info, e.Args[start:])
				}
				return true
			}
			// A spread of some OTHER function's call result: trust it only
			// after recursing into that function's own return expressions
			// (checkLocalFuncReturnsSafety) -- never by identity alone.
			fn := chaos5544ResolveCalleeFunc(e, info)
			return fn != nil && checkLocalFuncReturnsSafety(fn, funcVisiting)
		case *ast.Ident:
			if e.Name == "nil" && info.Uses[e] == types.Universe.Lookup("nil") {
				return true // a literal nil []any (e.g. requestIDLogAttrs' no-request-id branch) carries nothing
			}
			obj := info.Uses[e]
			if obj == nil {
				obj = info.Defs[e]
			}
			if obj != nil && visiting[obj] {
				return true // back-edge of an in-progress trace -- see comment above
			}
			assigns, known := funcAssigns[obj]
			if !known || len(assigns) == 0 {
				return false // a parameter, a field, or nothing found -- opaque
			}
			if obj != nil {
				visiting[obj] = true
				defer delete(visiting, obj)
			}
			for _, rhs := range assigns {
				if !checkSpreadSafety(fset, rhs, info, funcAssigns, visiting, funcVisiting) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}

	// inspectLoggerCall covers BOTH shapes: a flat, non-spread key/value
	// arg list (checked directly), and a spread (`attrs...`), which must
	// TRACE to composite literals / appends / trusted local-function
	// spreads -- never skipped, never satisfied by name alone.
	inspectLoggerCall := func(fset *token.FileSet, info *types.Info, call *ast.CallExpr, funcAssigns map[types.Object][]ast.Expr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		start, isLoggerMethod := chaos5544LoggerMethods[sel.Sel.Name]
		if !isLoggerMethod {
			return
		}
		if start >= len(call.Args) {
			return
		}
		rest := call.Args[start:]

		if call.Ellipsis != token.NoPos {
			if len(rest) != 1 {
				return
			}
			if !checkSpreadSafety(fset, rest[0], info, funcAssigns, map[types.Object]bool{}, map[*types.Func]bool{}) {
				findings = append(findings, found{pos: fset.Position(rest[0].Pos())})
			}
			return
		}
		checkPairs(fset, info, rest)
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				fd, isFunc := n.(*ast.FuncDecl)
				if !isFunc || fd.Body == nil {
					return true
				}
				funcAssigns := collectAssignsInFunc(fd.Body, pkg.TypesInfo)
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if call, isCall := n.(*ast.CallExpr); isCall {
						inspectSlogBuilder(pkg.Fset, pkg.TypesInfo, call)
						inspectLoggerCall(pkg.Fset, pkg.TypesInfo, call, funcAssigns)
						inspectAttrSpreadCall(pkg.Fset, pkg.TypesInfo, call, funcAssigns)
					}
					return true
				})
				return false // don't re-descend into the same body via the outer walk
			})
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].pos.Filename != findings[j].pos.Filename {
			return findings[i].pos.Filename < findings[j].pos.Filename
		}
		if findings[i].pos.Line != findings[j].pos.Line {
			return findings[i].pos.Line < findings[j].pos.Line
		}
		return findings[i].pos.Column < findings[j].pos.Column
	})
	// De-duplicate by exact position: a []any{...} composite literal that
	// is ALSO reachable via a logger spread's backward trace is checked
	// twice by design (once unconditionally by Rule A, once again as
	// checkSpreadSafety walks through it) -- two independent proofs of
	// the same fact, not two distinct findings.
	seen := map[string]bool{}
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		s := f.pos.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
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
		t.Errorf("%d unsanitized string/[]string log attribute(s) or untraceable spread(s) found -- "+
			"every value must route through SanitizeLogAttr/SanitizeLogStrings at its OWN construction "+
			"site (bare in this package, contextfabric.-qualified elsewhere); a spread must trace to "+
			"composite literals/appends or a trusted local-function spread:", len(findings))
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

// TestChaos5544SanitizerInstrumentCatchesAnUnwrappedAppendValue is
// CatchesAnUnwrappedSite's sibling for the r3 spread class: a fixture
// builds its []any attrs slice across two `append` calls (the exact
// shape the seven r3 production sites used) with one value left
// unwrapped, and spreads the fully-traceable slice. The spread itself is
// traceable (literal + append), so the finding must land on the
// unwrapped append VALUE, not the spread.
func TestChaos5544SanitizerInstrumentCatchesAnUnwrappedAppendValue(t *testing.T) {
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with exactly one unwrapped append "+
			"value -- it must find exactly one, proving the append-value class can fail: %v",
			len(findings), findings)
	}
	if want := fmt.Sprintf("%s:7:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:7 (the append's extra value), not the "+
			"spread -- a traceable spread must not itself be flagged", findings[0])
	}
}

// TestChaos5544SanitizerInstrumentRefusesAnOpaqueSpread proves the spread
// tracer's OTHER failure mode: a spread argument that is a function
// PARAMETER (or any other construction this static walk cannot see into)
// is refused at the spread site, never silently trusted just because it
// is a []any.
func TestChaos5544SanitizerInstrumentRefusesAnOpaqueSpread(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, attrs []any) {
	logger.Info("fixture line", attrs...)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5544opaque\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture whose spread is an untraceable "+
			"parameter -- it must refuse it as opaque: %v", len(findings), findings)
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

// TestChaos5558SanitizerInstrumentCatchesAnUnwrappedGroupValue is the
// builder-coverage fixture for slog.Group (CHAOS-5558, #497 r3's own latent
// P3): a value nested inside a Group's own alternating key/value tail was
// invisible before this ticket -- Group's own args were never passed
// through checkPairs.
func TestChaos5558SanitizerInstrumentCatchesAnUnwrappedGroupValue(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID string) {
	logger.Info("fixture line", "outer", slog.Group("inner", "request_id", requestID))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558group\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with exactly one unwrapped "+
			"slog.Group value -- it must find exactly one: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:6 (the Group's request_id value)", findings[0])
	}
}

// TestChaos5558SanitizerInstrumentCatchesUnwrappedValueBuilders is the
// builder-coverage fixture for slog.StringValue/slog.AnyValue: their
// return type is a bare slog.Value, invisible to chaos5544Classify's
// string/[]string type switch entirely until this ticket taught
// inspectSlogBuilder to look inside the call. Both sites also exercise
// LogAttrs's own non-spread argument list (each slog.Attr composite
// literal's Value field), proving LogAttrs needs no dedicated per-arg
// handling: the constructor calls inside it are already visible to the
// unconditional per-CallExpr walk.
func TestChaos5558SanitizerInstrumentCatchesUnwrappedValueBuilders(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID, other string) {
	logger.LogAttrs(nil, slog.LevelInfo, "fixture line",
		slog.Attr{Key: "request_id", Value: slog.StringValue(requestID)},
		slog.Attr{Key: "other", Value: slog.AnyValue(other)},
	)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558value\n\ngo 1.21\n"
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
	if len(findings) != 2 {
		t.Fatalf("the instrument found %d finding(s) in a fixture with exactly two unwrapped "+
			"StringValue/AnyValue sites -- it must find exactly two: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:7:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("findings[0] = %q, want it to point at fixture.go:7 (the StringValue argument)", findings[0])
	}
	if want := fmt.Sprintf("%s:8:", filepath.Join(dir, "fixture.go")); findings[1][:len(want)] != want {
		t.Fatalf("findings[1] = %q, want it to point at fixture.go:8 (the AnyValue argument)", findings[1])
	}
}

// TestChaos5558SanitizerInstrumentRefusesAnOpaqueAttrSpread is LogAttrs/
// AddAttrs's own opaque-spread rule, the []slog.Attr counterpart to
// TestChaos5544SanitizerInstrumentRefusesAnOpaqueSpread's []any one: a
// spread argument whose []slog.Attr slice was built OUTSIDE this function
// (here, a bare parameter) is refused at the spread site -- it is not
// trusted just because nothing inside THIS function looks unsafe, since
// nothing inside this function can see how it was built at all.
func TestChaos5558SanitizerInstrumentRefusesAnOpaqueAttrSpread(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogViaLogAttrs(logger *slog.Logger, attrs []slog.Attr) {
	logger.LogAttrs(nil, slog.LevelInfo, "fixture line", attrs...)
}

func LogViaAddAttrs(rec *slog.Record, attrs []slog.Attr) {
	rec.AddAttrs(attrs...)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558opaqueattr\n\ngo 1.21\n"
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
	if len(findings) != 2 {
		t.Fatalf("the instrument found %d finding(s) in a fixture with one opaque LogAttrs spread "+
			"and one opaque AddAttrs spread -- it must find exactly two: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("findings[0] = %q, want it to point at fixture.go:6 (the LogAttrs spread)", findings[0])
	}
	if want := fmt.Sprintf("%s:10:", filepath.Join(dir, "fixture.go")); findings[1][:len(want)] != want {
		t.Fatalf("findings[1] = %q, want it to point at fixture.go:10 (the AddAttrs spread)", findings[1])
	}
}

// TestChaos5558SanitizerInstrumentTrustsATraceableAttrSpread is the
// positive control for the rule above: a []slog.Attr built by a LOCAL
// composite literal, then spread, must NOT itself be reported as opaque
// (its construction is right there) -- while the unsanitized value inside
// that literal is still caught, by the ordinary unconditional walk, proving
// the opaque-spread rule does not paper over the real finding by trusting
// the spread wholesale.
func TestChaos5558SanitizerInstrumentTrustsATraceableAttrSpread(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID string) {
	attrs := []slog.Attr{slog.String("request_id", requestID)}
	logger.LogAttrs(nil, slog.LevelInfo, "fixture line", attrs...)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558traceableattr\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with a traceable local-var attr "+
			"spread -- it must find exactly the construction site, not the spread, and not zero: %v",
			len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:6 (the slog.String value), not the "+
			"spread on line 7 -- a traceable spread must not itself be flagged", findings[0])
	}
}

// TestChaos5558SanitizerInstrumentResolvesSlogByIdentityNotName is the r1
// review round's own P3 finding, pinned: inspectSlogBuilder used to check
// the package qualifier by NAME ("slog"), not identity -- an aliased
// import (`import log "log/slog"`) made an unsanitized value inside
// log.Group/log.String/etc invisible, executed-confirmed with
// `findings: []` on a fixture whose request_id genuinely never passes
// through a sanitizer. Matches this file's own established standard
// (chaos5544ResolveCalleeFunc/chaos5544IsStringConversion already resolve
// by go/types identity, never by name) -- this was the one builder-side
// holdout.
func TestChaos5558SanitizerInstrumentResolvesSlogByIdentityNotName(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import log "log/slog"

func LogIt(logger *log.Logger, requestID string) {
	logger.Info("fixture line", "outer", log.Group("inner", "request_id", requestID))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558slogalias\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture that imports \"log/slog\" under the "+
			"alias \"log\" with one genuinely unwrapped value -- it must find exactly one, resolving the "+
			"package by identity regardless of the local import name: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:6 (the Group's request_id value)", findings[0])
	}
}

// TestChaos5558SanitizerInstrumentCatchesADotImportedBuilder is r2's own
// P3 pin: a DOT IMPORT (`import . "log/slog"`) makes a builder call a bare
// *ast.Ident, not the *ast.SelectorExpr every prior check assumed --
// genuinely different from the aliased-QUALIFIED-import case
// TestChaos5558SanitizerInstrumentResolvesSlogByIdentityNotName already
// pins above. chaos5544ResolveSlogBuiltinName resolves both shapes through
// go/types identity.
func TestChaos5558SanitizerInstrumentCatchesADotImportedBuilder(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import . "log/slog"

func LogIt(logger *Logger, requestID string) {
	logger.Info("fixture line", "outer", Group("inner", "request_id", requestID))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558dotimport\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with a dot-imported \"log/slog\" and one "+
			"genuinely unwrapped Group value -- it must find exactly one: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:6 (the Group's request_id value)", findings[0])
	}
}

// TestChaos5558SanitizerInstrumentRefusesAnOpaqueNonSpreadAttrArgument is
// r2's other own finding: a NON-SPREAD LogAttrs/AddAttrs argument that is
// a call to a function OUTSIDE the scanned tree, returning a pre-built
// slog.Attr from unsanitized data, was trusted unconditionally ("each
// arg's own constructor call is checked unconditionally elsewhere" --
// true only when that constructor call is ITSELF inside the scanned
// tree). The fixture's helper package is deliberately NOT covered by the
// "." scan pattern below (mirroring production, which scans only
// github.com/full-chaos/dev-health-acr/internal/contextfabric/..., not
// every package that could ever call into it), so helper.MakeAttr's own
// body -- which DOES wrap its value correctly with slog.String -- is
// invisible from the caller's side, and the caller must refuse the call
// result as opaque rather than trust it by absence of a visible problem.
func TestChaos5558SanitizerInstrumentRefusesAnOpaqueNonSpreadAttrArgument(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "helper"), 0o755); err != nil {
		t.Fatalf("mkdir helper: %v", err)
	}
	helperSrc := `package helper

import "log/slog"

func MakeAttr(v string) slog.Attr {
	return slog.String("request_id", v)
}
`
	if err := os.WriteFile(filepath.Join(dir, "helper", "helper.go"), []byte(helperSrc), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	src := `package fixture

import (
	"log/slog"

	"fixture.example/chaos5558opaquenonspread/helper"
)

func LogAttrsCaller(logger *slog.Logger, requestID string) {
	logger.LogAttrs(nil, slog.LevelInfo, "fixture line", helper.MakeAttr(requestID))
}

func AddAttrsCaller(rec *slog.Record, requestID string) {
	rec.AddAttrs(helper.MakeAttr(requestID))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558opaquenonspread\n\ngo 1.21\n"
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

	// "." not "./..." -- the helper subpackage must NOT be scanned, the
	// same boundary the real repo pattern draws around internal/contextfabric.
	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, ".")
	if len(findings) != 2 {
		t.Fatalf("the instrument found %d finding(s) in a fixture with one opaque non-spread LogAttrs "+
			"argument and one opaque non-spread AddAttrs argument (both calling an out-of-tree helper) -- "+
			"it must find exactly two: %v", len(findings), findings)
	}
}

// TestChaos5558SanitizerInstrumentTrustsALocalNonSpreadAttrArgument is the
// positive control for the rule above: a non-spread LogAttrs argument that
// calls a LOCAL function (declared inside the scanned tree) must NOT be
// flagged as opaque merely for being a function call -- that function's
// own body is independently scanned, and its own unwrapped value (if any)
// is caught there, at its own definition site, not the call site.
func TestChaos5558SanitizerInstrumentTrustsALocalNonSpreadAttrArgument(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func makeAttr(v string) slog.Attr {
	return slog.String("request_id", "constant")
}

func LogIt(logger *slog.Logger, requestID string) {
	logger.LogAttrs(nil, slog.LevelInfo, "fixture line", makeAttr(requestID))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558localnonspread\n\ngo 1.21\n"
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

	// makeAttr's body uses a compile-time string CONSTANT, not its own
	// parameter -- genuinely safe on its own merits, so this fixture only
	// proves what it claims: the non-spread call site itself is trusted
	// because makeAttr is LOCAL (its body is independently scanned), not
	// because of anything specific to what that body happens to do.
	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, "./...")
	if len(findings) != 0 {
		t.Fatalf("the instrument found %d finding(s) in a fixture whose non-spread LogAttrs argument calls "+
			"a LOCAL function -- it must trust the call site and let the function's own body (already "+
			"scanned) speak for itself: %v", len(findings), findings)
	}
}

// TestChaos5558SanitizerInstrumentCatchesAnUnwrappedLogMethodValue is the
// r3 review round's P2 pin: chaos5544LoggerMethods omitted `Log`
// ((*slog.Logger).Log(ctx, level, msg, args...)), a real logger entry
// point `go doc` confirms -- 0 current contextfabric call sites use it
// (latent, not active), but the scanner must still recognize it.
func TestChaos5558SanitizerInstrumentCatchesAnUnwrappedLogMethodValue(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import (
	"context"
	"log/slog"
)

func LogIt(logger *slog.Logger, ctx context.Context, requestID string) {
	logger.Log(ctx, slog.LevelInfo, "fixture line", "request_id", requestID)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558logmethod\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with one unwrapped value passed to "+
			"logger.Log -- it must find exactly one: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:9:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:9 (the Log call's request_id value)", findings[0])
	}
}

// TestChaos5558SanitizerInstrumentFailsClosedOnAnAnyTypedValue is the r3
// review round's other P2 pin: a value statically typed `any` (the
// concrete string erased by an intermediate variable) fell through the
// classifier to "" -- unflagged -- purely for not being *types.Basic.
// This fixture proves the fail-CLOSED behavior: a genuinely opaque
// any-typed identifier (not an IndexExpr into an already-checked slice,
// the one exempted shape -- see
// TestChaos5558SanitizerInstrumentTrustsAnAnyTypedSliceIndex below) must
// be flagged, not assumed safe.
func TestChaos5558SanitizerInstrumentFailsClosedOnAnAnyTypedValue(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID string) {
	var hidden any = requestID
	logger.Info("fixture line", "request_id", hidden)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558anytyped\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with one any-typed value carrying an "+
			"unsanitized string -- it must fail closed and find exactly one: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:7:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:7 (the hidden any-typed value)", findings[0])
	}
}

// TestChaos5558SanitizerInstrumentTrustsAnAnyTypedSliceIndex is the
// positive control for the any-typed fail-closed rule: genkitruntime's OWN
// production shape (attemptLogFields) stages values in a []any COMPOSITE
// LITERAL -- already checked directly, at ITS OWN construction, by the
// unconditional per-element walk -- and then reads them back by INDEX to
// build the real key/value pairs. The indexed read's static type is also
// `any`, but flagging it would be a duplicate, false finding: the literal
// already proved (or would have proved) every element safe. This fixture
// reproduces that exact shape with an ALREADY-SANITIZED value and asserts
// zero findings.
func TestChaos5558SanitizerInstrumentTrustsAnAnyTypedSliceIndex(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func SanitizeLogAttr(s string) string { return s }

func LogIt(logger *slog.Logger, requestID string) {
	values := []any{SanitizeLogAttr(requestID)}
	fields := make([]any, 0, 2)
	fields = append(fields, "request_id", values[0])
	logger.Info("fixture line", fields...)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558anyindex\n\ngo 1.21\n"
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
	if len(findings) != 0 {
		t.Fatalf("the instrument found %d finding(s) in a fixture whose any-typed value is an INDEX into an "+
			"already-checked []any composite literal -- it must trust the read, not double-flag it: %v",
			len(findings), findings)
	}
}

// TestChaos5558SanitizerInstrumentRefusesAnOpaqueAttrInsideGroup is the r3
// review round's third P2 pin: go doc's own words for slog.Group --
// "the remaining arguments are converted to Attrs as in Logger.Log" --
// mean a pre-built slog.Attr can be SPLICED into a Group's arg list at
// any position, not only alternating key/value pairs. checkPairs'
// fixed-index-parity view could not see this: an Attr at an even
// ("key") index was never classified at all. This fixture splices an
// opaque, out-of-tree helper's Attr into a Group call and proves it is
// now refused.
func TestChaos5558SanitizerInstrumentRefusesAnOpaqueAttrInsideGroup(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "helper"), 0o755); err != nil {
		t.Fatalf("mkdir helper: %v", err)
	}
	helperSrc := `package helper

import "log/slog"

func MakeAttr(v string) slog.Attr {
	return slog.String("request_id", v)
}
`
	if err := os.WriteFile(filepath.Join(dir, "helper", "helper.go"), []byte(helperSrc), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	src := `package fixture

import (
	"log/slog"

	"fixture.example/chaos5558opaquegroup/helper"
)

func LogIt(logger *slog.Logger, requestID string) {
	logger.Info("fixture line", "outer", slog.Group("inner", helper.MakeAttr(requestID), "extra", "safe"))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558opaquegroup\n\ngo 1.21\n"
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

	// "." not "./..." -- the helper subpackage must NOT be scanned, the
	// same boundary the real repo pattern draws around internal/contextfabric.
	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, ".")
	if len(findings) != 1 {
		t.Fatalf("the instrument found %d finding(s) in a fixture with one opaque Attr spliced into a "+
			"Group call -- it must find exactly one: %v", len(findings), findings)
	}
}

// TestChaos5558SanitizerInstrumentCatchesATrailingOddLoggerArg is the r3
// review round's fourth P2 pin: checkPairs' `i+1 < len(elts)` loop bound
// silently dropped a TRAILING unpaired element on an odd-length arg list.
// slog's own runtime does not discard it (logged under the sentinel key
// `!BADKEY`), so the scanner must not either.
func TestChaos5558SanitizerInstrumentCatchesATrailingOddLoggerArg(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID string) {
	logger.Info("fixture line", "request_id", "safe", requestID)
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5558oddarg\n\ngo 1.21\n"
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
		t.Fatalf("the instrument found %d finding(s) in a fixture with one unwrapped TRAILING odd logger "+
			"arg -- it must find exactly one: %v", len(findings), findings)
	}
	if want := fmt.Sprintf("%s:6:", filepath.Join(dir, "fixture.go")); findings[0][:len(want)] != want {
		t.Fatalf("finding = %q, want it to point at fixture.go:6 (the trailing requestID arg)", findings[0])
	}
}

// TestChaos5544SanitizerInstrumentStillCatchesAStringBesideNonStringAttrs is
// the control for trusting slog's non-string builders: a Group whose arg list
// mixes slog.Bool/slog.Int attrs with ONE unwrapped string attr must still be
// reported. Without it, "trust Bool and Int" could have been read as "trust
// everything in a Group that contains one".
func TestChaos5544SanitizerInstrumentStillCatchesAStringBesideNonStringAttrs(t *testing.T) {
	dir := t.TempDir()
	src := `package fixture

import "log/slog"

func LogIt(logger *slog.Logger, requestID string, n int) {
	logger.Info("fixture line", "outer", slog.Group("inner",
		slog.Bool("present", true),
		slog.Int("count", n),
		slog.String("request_id", requestID),
		slog.Bool("done", false),
	))
}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	modSrc := "module fixture.example/chaos5544mixed\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modSrc), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	findings := chaos5544ScanForUnsanitizedLogAttrs(t, dir, "./...")
	if len(findings) != 1 {
		t.Fatalf("want exactly the one unwrapped string attr, got %d: %v", len(findings), findings)
	}
	t.Logf("instrument reported: %v", findings)
}
