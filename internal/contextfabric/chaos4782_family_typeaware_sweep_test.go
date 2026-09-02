package contextfabric

// CHAOS-4782 -- type-aware, data-flow family-language sweep.
//
// This REPLACES chaos4735_family_language_sweep_test.go's mechanism, not
// its purpose. That sweep matched syntax and names; three consecutive
// adversarial review rounds each defeated a version of it with a
// genuinely new construction (R1: comparison to a raw string literal,
// closed; R2: a named string-underlying type as a map's value type,
// closed; R3: family -> ordinal via the vocabulary -> index into a text
// table, NOT closed -- the sweep's own header comment says so). The
// reviewer's conclusion, quoted in CHAOS-4782: "syntax/name matching
// cannot support the claimed universal nonexistence property. Use
// type-aware, flow-sensitive analysis at minimum."
//
// WHAT CHANGED, structurally, and why each change closes a specific hole:
//
//   - SEEDING is by go/types TYPE IDENTITY (types.Identical against the
//     resolved contractsv1.ContextFabricQuestionFamily type), not by
//     matching identifier names. A value is "family-typed" if the type
//     checker says so, however it is spelled -- including through the
//     package-local `QuestionFamily` alias, which is a genuine Go type
//     alias and therefore IS the same *types.Named to the type checker.
//   - TAINT FOLLOWS THE VALUE through assignment, parenthesisation, and
//     TYPE CONVERSION. A conversion changes an expression's STATIC type
//     but not the runtime value it carries, so `string(family)` (R1) and
//     `SomeAlias(family)` stay tainted after the conversion -- the
//     evasion in both R1 and R2 was hiding the family-ness behind a type
//     that doesn't match by NAME; tracking the value instead of the
//     syntactic type closes both by construction, not by extending a
//     builtin allowlist.
//   - RANGING OVER THE CLOSED VOCABULARY taints the loop's INDEX
//     variable, not just its element variable. This is what closes R3:
//     the position of a family value within
//     [Context]FabricQuestionFamilyVocabulary() is genuinely
//     family-derived information, even though the position itself has
//     type int and no data-flow chain of assignments literally copies the
//     family value into it. Ranging over any OTHER array/slice does not
//     taint anything -- this rule fires exactly on "which slot in the
//     closed vocabulary", which is the one thing a family value can
//     legitimately determine outside a sanctioned reader.
//   - FIELD-SENSITIVE, TWO-PASS propagation: pass 1 records which struct
//     FIELDS are ever assigned a tainted value anywhere in the swept
//     roots; pass 2 treats every READ of such a field as tainted too,
//     regardless of which function or which local variable holds the
//     struct. This closes the shape the CHAOS-4735 sweep's own doc
//     comment named as UNCAUGHT: "anything reached through a function
//     boundary or a struct field, where the family and the text are in
//     different scopes."
//   - SANCTIONED READERS are exempted by go/types OBJECT IDENTITY, not by
//     file path. The four purposes (design 13.4.3 / CHAOS-4782: the
//     precedence table that PRODUCES the family, the registry -- which is
//     also where budget-profile selection actually lives, see
//     sweep-for-4782.go.txt's correction of design row 8 -- LookupQuestionFamily,
//     and the vocabulary declarations on both sides of the wire boundary)
//     are resolved ONCE, at test time, to their declared *types.Object
//     set from the four purpose files. A read is sanctioned only if its
//     ENCLOSING top-level declaration resolves, through the type checker,
//     to one of those objects -- not "is this violation's file path in an
//     allowlist". This is strictly narrower than the old file-level
//     check: an unrelated function added to one of the four purpose files
//     is NOT automatically sanctioned just by sharing a file with a
//     sanctioned one.
//
// WHAT THIS STILL DOES NOT PROVE, stated so green is not mistaken for a
// nonexistence proof (the same discipline chaos4735_family_language_sweep_test.go
// applies to itself):
//   - Interprocedural taint is bounded to TWO hops: (1) a function whose
//     body directly derives a tainted value (seed, conversion, ordinal
//     index, or a read of a globally tainted field) and (2) one field
//     write/read hop across a function boundary. A value laundered
//     through three or more independent field/function hops before
//     reaching a sink is outside what this analysis proves clean OR
//     dirty -- it fails OPEN on facts it cannot derive, which is why the
//     analysis asserts its own convergence (below) rather than silently
//     stopping after a fixed number of passes.
//   - Reflection, encoding/json struct tags read at runtime, and
//     interface dispatch across a non-identical type are not modeled.
//   - This sweep, like its predecessor, covers exactly the four swept
//     roots (design 13.4.3's boundary): internal/contextfabric,
//     internal/api, internal/contracts/v1, internal/mcp.
//
// The two sweeps currently coexist: this ticket does not delete
// chaos4735_family_language_sweep_test.go, because the wire tests in
// internal/api that catch R2/R3 in practice are a DIFFERENT layer (the
// served body, not the source), and CHAOS-4782's acceptance criteria do
// not ask for the heuristic's removal. If a future round shows this sweep
// strictly dominates the heuristic (catches everything it catches, plus
// R3), the heuristic should be retired rather than kept as a second
// authority -- tracked as residue in the lane handoff, not done here to
// keep this change reviewable.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// chaos4782SweptImportPaths are the four production roots this sweep
// analyzes, as Go import paths (not file paths) so golang.org/x/tools/go/packages
// resolves them with full type information, recursively, in one
// type-checking session -- required for go/types object identity to be
// comparable across the four packages at all.
var chaos4782SweptImportPaths = []string{
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/...",
	"github.com/full-chaos/dev-health-acr/internal/api/...",
	"github.com/full-chaos/dev-health-acr/internal/contracts/v1/...",
	"github.com/full-chaos/dev-health-acr/internal/mcp/...",
}

// chaos4782SanctionedFiles are the four purpose files whose TOP-LEVEL
// DECLARATIONS bootstrap the sanctioned-object set. This is still an
// anchor into the source (the four purposes have to start somewhere), but
// unlike chaos4735_family_language_sweep_test.go's sanctionedFamilyReadSites,
// the ENFORCEMENT below never compares a violation's file path against
// this list -- it compares the violation's ENCLOSING DECLARATION's
// resolved go/types.Object against the set of objects declared in these
// files. A new, unrelated function added to one of these files is not
// sanctioned merely by proximity.
var chaos4782SanctionedFiles = []string{
	// Purpose 1: the precedence table that PRODUCES the family.
	"internal/contextfabric/chaos4632_question_family_precedence.go",
	// Purpose 2: the registry -- LookupQuestionFamily, which is also
	// where budget-profile selection lives (sweep-for-4782.go.txt's
	// correction of design 13.9a row 8).
	"internal/contextfabric/chaos4632_question_family_registry.go",
	// Purpose 3: the package-local vocabulary aliases.
	"internal/contextfabric/chaos4632_question_family_vocab.go",
	// Purpose 4: the wire vocabulary declaration itself.
	"internal/contracts/v1/context_fabric_answer_plan.go",
}

// chaos4782ContractsPurposeFile is purpose 4 alone, used by
// TestChaos4782CatchesHistoricalConstructions, whose fixture loads never
// include internal/contextfabric (purposes 1-3).
var chaos4782ContractsPurposeFile = []string{
	"internal/contracts/v1/context_fabric_answer_plan.go",
}

// chaos4782Violation is one finding: a file:line plus a human message.
type chaos4782Violation struct {
	pos     token.Position
	message string
}

func (v chaos4782Violation) String() string {
	return fmt.Sprintf("%s (%s)", v.pos, v.message)
}

// chaos4782Facts is everything the analysis resolves ONCE from a loaded
// program before walking it: the family type itself, its constants (both
// spellings, matching chaos4735's non-vacuity discipline), the closed
// vocabulary's wire-value string literals (for the raw-keyed-table rule,
// which needs no type information at all), and the sanctioned object set.
type chaos4782Facts struct {
	fset               *token.FileSet
	familyType         types.Type
	discriminating     map[types.Object]bool // family constants EXCLUDING the unclassified sentinel
	allFamilyConstants map[types.Object]bool // INCLUDING the sentinel; only used for the non-vacuity count
	wireValueLiterals  map[string]bool       // `"subject_investigation"` etc, quotes included
	sanctioned         map[types.Object]bool // sanctioned readers, by declared object
}

// chaos4782ResolveFacts derives chaos4782Facts from a single loaded
// program. It must be called on the SAME *packages.Package slice that will
// be walked, and on that slice ONLY -- go/types object identity (used
// throughout via pointer equality) is only meaningful within one
// type-checking session.
func chaos4782ResolveFacts(t *testing.T, pkgs []*packages.Package, sanctionedFiles []string) chaos4782Facts {
	t.Helper()

	var contractsPkg *packages.Package
	for _, p := range pkgs {
		if p.PkgPath == "github.com/full-chaos/dev-health-acr/internal/contracts/v1" {
			contractsPkg = p
		}
	}
	if contractsPkg == nil {
		t.Fatalf("internal/contracts/v1 was not part of the loaded program -- cannot resolve the family type")
	}

	familyObj := contractsPkg.Types.Scope().Lookup("ContextFabricQuestionFamily")
	if familyObj == nil {
		t.Fatalf("ContextFabricQuestionFamily not found in internal/contracts/v1 -- declaration renamed?")
	}
	familyType := familyObj.Type()

	discriminating := map[types.Object]bool{}
	allConstants := map[types.Object]bool{}
	wireValues := map[string]bool{}
	for _, p := range pkgs {
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			c, ok := obj.(*types.Const)
			if !ok || !types.Identical(c.Type(), familyType) {
				continue
			}
			allConstants[obj] = true
			wireValues[c.Val().ExactString()] = true // ExactString on a string constant includes quotes
			if strings.HasSuffix(name, "Unclassified") {
				continue
			}
			discriminating[obj] = true
		}
	}

	// NON-VACUITY, adapted from chaos4735's non-vacuity check. The
	// production-roots load carries the constant TWICE (the contracts.v1
	// wire spelling plus the contextfabric package-local alias spelling),
	// so it wants exactly 2*Count. A fixture load carries only the
	// contracts.v1 spelling (fixtures do not load internal/contextfabric
	// -- see TestChaos4782CatchesHistoricalConstructions), so it wants
	// exactly 1*Count. Any OTHER count means the needle set is broken
	// (some spelling silently missing or duplicated), not that a
	// different swept-package combination is fine to shrug at.
	count := contractsv1.ContextFabricQuestionFamilyCount
	if len(allConstants) == 0 || len(allConstants)%count != 0 || len(allConstants)/count > 2 {
		names := make([]string, 0, len(allConstants))
		for obj := range allConstants {
			names = append(names, obj.Pkg().Path()+"."+obj.Name())
		}
		sort.Strings(names)
		t.Fatalf("family constant needle set = %d objects, want exactly 1x or 2x %d (one spelling per loaded package that re-declares the closed vocabulary; every one of the %d closed vocabulary members): %v",
			len(allConstants), count, count, names)
	}
	spellings := len(allConstants) / count
	if len(discriminating) != len(allConstants)-spellings {
		t.Fatalf("expected the closed vocabulary to carry exactly one unclassified sentinel per loaded spelling (%d spellings); discriminating=%d of %d", spellings, len(discriminating), len(allConstants))
	}

	sanctioned := chaos4782ResolveSanctionedObjects(t, pkgs, sanctionedFiles)

	return chaos4782Facts{
		familyType:         familyType,
		discriminating:     discriminating,
		allFamilyConstants: allConstants,
		wireValueLiterals:  wireValues,
		sanctioned:         sanctioned,
	}
}

// chaos4782ResolveSanctionedObjects walks the top-level declarations of
// the named files (matched by suffix against each package's
// CompiledGoFiles, so it works regardless of which loaded package a file
// belongs to) and returns the *types.Object each one declares: every
// function's object, and every name in every var/const spec. A read is
// sanctioned iff its enclosing declaration resolves to one of these
// objects.
func chaos4782ResolveSanctionedObjects(t *testing.T, pkgs []*packages.Package, sanctionedFiles []string) map[types.Object]bool {
	t.Helper()
	sanctioned := map[types.Object]bool{}
	if len(sanctionedFiles) == 0 {
		// Historical-construction fixtures are loaded standalone (see
		// TestChaos4782CatchesHistoricalConstructions): they never
		// contain a sanctioned declaration, and loading the whole of
		// internal/contextfabric just to resolve an empty exemption set
		// would triple each fixture's load cost for no signal. An empty
		// input here means "exempt nothing", not "resolution failed".
		return sanctioned
	}
	matched := map[string]bool{}

	for _, p := range pkgs {
		for i, filePath := range p.CompiledGoFiles {
			relPath := filepath.ToSlash(filePath)
			for _, want := range sanctionedFiles {
				if !strings.HasSuffix(relPath, "/"+want) && relPath != want {
					continue
				}
				matched[want] = true
				file := p.Syntax[i]
				for _, decl := range file.Decls {
					switch d := decl.(type) {
					case *ast.FuncDecl:
						if obj := p.TypesInfo.Defs[d.Name]; obj != nil {
							sanctioned[obj] = true
						}
					case *ast.GenDecl:
						for _, spec := range d.Specs {
							switch s := spec.(type) {
							case *ast.ValueSpec:
								for _, name := range s.Names {
									if obj := p.TypesInfo.Defs[name]; obj != nil {
										sanctioned[obj] = true
									}
								}
							case *ast.TypeSpec:
								if obj := p.TypesInfo.Defs[s.Name]; obj != nil {
									sanctioned[obj] = true
								}
							}
						}
					}
				}
			}
		}
	}

	var missing []string
	for _, want := range sanctionedFiles {
		if !matched[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("sanctioned-reader source files not found in the loaded program (renamed or moved?): %v", missing)
	}
	if len(sanctioned) == 0 {
		t.Fatalf("resolved zero sanctioned objects from %v -- the sweep would exempt nothing, which is not the same as a correctly narrow allowlist", sanctionedFiles)
	}
	return sanctioned
}

// chaos4782MayHoldText fails CLOSED: everything can hold text unless it is
// one of a small set of builtins that provably cannot. Carried over from
// chaos4735_family_language_sweep_test.go's mayHoldText (found by codex
// round 2): matching textual types by NAME was the mistake; inverting the
// default is what survives a renamed textual type.
func chaos4782MayHoldText(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	if !ok {
		return true // struct, interface, pointer, slice, map: not provably non-textual.
	}
	switch basic.Info() & (types.IsInteger | types.IsFloat | types.IsComplex | types.IsBoolean) {
	case 0:
		return true // string, or an untyped/unknown basic kind: assume textual.
	default:
		return false
	}
}

// chaos4782FuncScope is the function or file-level declaration currently
// being walked, used both to seed per-declaration local taint state and to
// decide whether violations found inside it are exempt.
type chaos4782FuncScope struct {
	pkg        *packages.Package
	enclosing  types.Object // the top-level object this code lives inside (nil for none resolvable)
	sanctioned bool
}

// chaos4782Analyzer runs the two-pass, field-sensitive taint walk over a
// loaded program and reports violations. It is reusable across the main
// production-roots run and each historical-construction fixture: callers
// supply the facts (family type/constants/sanctioned set) resolved from
// THEIR OWN load, since go/types object identity does not survive across
// separate packages.Load calls.
type chaos4782Analyzer struct {
	facts      chaos4782Facts
	fieldTaint map[types.Object]bool // struct FIELD objects known to carry family-derived data
}

// chaos4782TaintKind distinguishes "this expression's value is exactly a
// family-typed value" (raw) from "this expression is derived from one, but
// is not itself family-typed" (derived -- the ordinal/converted case). The
// distinction matters for exactly one rule: the raw-map-key rule (mirrors
// chaos4735's rule 2, a map keyed by the family type itself) fires on RAW
// only, since a map keyed by an ordinary int is not inherently suspect
// (chaos4632_question_family_telemetry.go's vote tallies are keyed by the
// family type directly, not by a derived int, and ARE flagged correctly by
// the raw rule when their value type is textual -- they are not, so they
// are not flagged; a map keyed by a derived int is covered by the
// index-derives-text rule instead, which fires on any tainted index,
// raw or derived).
type chaos4782TaintKind int

const (
	chaos4782NotTainted chaos4782TaintKind = iota
	chaos4782TaintRaw
	chaos4782TaintDerived
)

func (k chaos4782TaintKind) tainted() bool { return k != chaos4782NotTainted }

// chaos4782Local is the per-function taint state: which local objects
// (params, `:=`-declared vars) are currently tainted, and at which kind.
type chaos4782Local map[types.Object]chaos4782TaintKind

// chaos4782Walker carries the state for one pass over one function body.
type chaos4782Walker struct {
	an          *chaos4782Analyzer
	pkg         *packages.Package
	scope       chaos4782FuncScope
	local       chaos4782Local
	collect     bool // pass 1: record field-taint facts instead of reporting
	newFields   map[types.Object]bool
	violations  []chaos4782Violation
	returnTaint chaos4782TaintKind
}

func (w *chaos4782Walker) report(pos token.Pos, message string) {
	if w.scope.sanctioned || w.collect {
		return
	}
	w.violations = append(w.violations, chaos4782Violation{pos: w.pkg.Fset.Position(pos), message: message})
}

// isConversion reports whether call is a type conversion (as opposed to a
// function call) by asking whether its Fun names a TYPE, not a value --
// this is what lets taint survive `string(family)` / `SomeAlias(family)`.
func (w *chaos4782Walker) isConversion(call *ast.CallExpr) bool {
	if len(call.Args) != 1 {
		return false
	}
	tv, ok := w.pkg.TypesInfo.Types[call.Fun]
	if ok && tv.IsType() {
		return true
	}
	// Parenthesised or selector-qualified type names (pkg.Type(x)) report
	// IsType() on the inner expression too; the Types map above already
	// covers both cases via go/types, but fall back to Uses for a bare
	// *ast.Ident naming a type object defensively.
	if id, ok := call.Fun.(*ast.Ident); ok {
		if obj := w.pkg.TypesInfo.Uses[id]; obj != nil {
			if _, isTypeName := obj.(*types.TypeName); isTypeName {
				return true
			}
		}
	}
	return false
}

// eval classifies one expression's taint under the CURRENT local state,
// recursing into subexpressions. It has no side effects other than
// (optionally) recording field-taint facts when w.collect is set -- taint
// facts about ASSIGNMENTS are recorded by the statement walker, not here.
func (w *chaos4782Walker) eval(expr ast.Expr) chaos4782TaintKind {
	switch e := expr.(type) {
	case nil:
		return chaos4782NotTainted
	case *ast.ParenExpr:
		return w.eval(e.X)
	case *ast.Ident:
		if obj := w.pkg.TypesInfo.Uses[e]; obj != nil {
			if k, ok := w.local[obj]; ok {
				return k
			}
			if w.an.fieldTaint[obj] {
				return chaos4782TaintDerived
			}
		}
		return w.rawIfFamilyTyped(e)
	case *ast.SelectorExpr:
		// A selector's own taint: either it directly resolves to a
		// tainted field object (field-sensitive, cross-function), or its
		// static type is family-identical (raw), or its base is tainted
		// derived (paren/conversion chains reaching through a selector
		// do not occur in practice here, so base-taint is not propagated
		// through a field select -- only an EXPLICIT field-taint fact
		// does, which is the field-sensitivity contract this analysis
		// documents).
		if obj := w.pkg.TypesInfo.Uses[e.Sel]; obj != nil && w.an.fieldTaint[obj] {
			return chaos4782TaintDerived
		}
		return w.rawIfFamilyTyped(e)
	case *ast.CallExpr:
		if w.isConversion(e) {
			// Conversion: taint survives, kind survives (a converted raw
			// family value is still exactly the family's runtime value,
			// just under a different static type -- this is precisely
			// the R1/R2 evasion).
			return w.eval(e.Args[0])
		}
		// An ordinary call: evaluate arguments for their own violations
		// (a tainted argument passed to an UNSANCTIONED function is not,
		// by itself, a violation under this analysis -- only comparison,
		// indexing, switch-dispatch, and field-write sinks are; this
		// keeps the analysis from flagging e.g. `log.Debug("family", family)`).
		// Sanctioned calls consume their arguments legitimately; the
		// call's result is not marked tainted merely because an argument
		// was -- interprocedural summaries beyond direct field relay are
		// out of scope (documented above).
		for _, arg := range e.Args {
			w.eval(arg)
		}
		return chaos4782NotTainted
	case *ast.IndexExpr:
		containerT := w.pkg.TypesInfo.TypeOf(e.X)
		indexKind := w.eval(e.Index)
		w.eval(e.X)
		if indexKind.tainted() && containerT != nil {
			elem := chaos4782ElemType(containerT)
			if elem != nil && chaos4782MayHoldText(elem) {
				w.report(e.Pos(), fmt.Sprintf(
					"index/key into %s is derived from a question-family value and the container can hold text -- this is the R3/R5 class: a family value's position or identity selects a text-yielding entry outside a sanctioned reader",
					containerT.String()))
			}
		}
		return chaos4782NotTainted
	case *ast.BinaryExpr:
		lk := w.eval(e.X)
		rk := w.eval(e.Y)
		if (e.Op == token.EQL || e.Op == token.NEQ) && (lk.tainted() || rk.tainted()) {
			if lit, litSide := chaos4782StringLiteral(e.X); litSide {
				w.checkLiteralCompare(e, lit)
			}
			if lit, litSide := chaos4782StringLiteral(e.Y); litSide {
				w.checkLiteralCompare(e, lit)
			}
		}
		return chaos4782NotTainted
	case *ast.UnaryExpr:
		return w.eval(e.X)
	case *ast.CompositeLit:
		w.evalCompositeLit(e)
		return chaos4782NotTainted
	default:
		return w.rawIfFamilyTyped(expr)
	}
}

// rawIfFamilyTyped is the SEED rule: any expression whose go/types-resolved
// type is identical to the family type is tainted, raw, regardless of how
// it is spelled -- this is what makes seeding type-based rather than
// name-based.
func (w *chaos4782Walker) rawIfFamilyTyped(expr ast.Expr) chaos4782TaintKind {
	t := w.pkg.TypesInfo.TypeOf(expr)
	if t != nil && types.Identical(t, w.an.facts.familyType) {
		return chaos4782TaintRaw
	}
	return chaos4782NotTainted
}

// checkLiteralCompare implements the R1 rule: a tainted value compared to a
// non-empty string literal. The empty-string literal is excluded on the
// same reasoning chaos4735 uses: `Family == ""` is an emptiness test, not a
// discriminating read.
func (w *chaos4782Walker) checkLiteralCompare(e *ast.BinaryExpr, lit *ast.BasicLit) {
	if lit.Value == `""` {
		return
	}
	w.report(e.OpPos, fmt.Sprintf(
		"a question-family-derived value is compared to the string literal %s instead of a closed-vocabulary constant -- the R1 class (codex round 1)", lit.Value))
}

func chaos4782StringLiteral(expr ast.Expr) (*ast.BasicLit, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			return e, true
		}
	case *ast.ParenExpr:
		return chaos4782StringLiteral(e.X)
	}
	return nil, false
}

// chaos4782ElemType returns the element type of an array/slice/map, or nil
// if t is none of those (the analysis only reasons about index/key
// containers).
func chaos4782ElemType(t types.Type) types.Type {
	switch u := t.Underlying().(type) {
	case *types.Array:
		return u.Elem()
	case *types.Slice:
		return u.Elem()
	case *types.Map:
		return u.Elem()
	}
	return nil
}

// evalCompositeLit handles two independent things: (1) struct-literal
// field values, which feed the field-taint pass (`T{Field: taintedExpr}`),
// and (2) map/array/slice literals, which are checked against the two
// structural rules carried over from chaos4735 -- a map whose KEY TYPE is
// family-identical and whose VALUE type may hold text (R2's shape, closed
// structurally as well as via the general index rule above), and a map
// literal keyed by the family's RAW WIRE VALUE written as a string literal
// (no family-typed expression appears at all, so the type/dataflow rules
// above cannot see it -- this one stays purely textual, same as chaos4735
// rule 2).
func (w *chaos4782Walker) evalCompositeLit(lit *ast.CompositeLit) {
	t := w.pkg.TypesInfo.TypeOf(lit)
	if t == nil {
		for _, elt := range lit.Elts {
			w.evalCompositeElt(elt, nil)
		}
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		w.evalStructLit(lit, u, t)
	case *types.Map:
		w.evalMapLit(lit, u, t)
	default:
		for _, elt := range lit.Elts {
			w.evalCompositeElt(elt, nil)
		}
	}
}

func (w *chaos4782Walker) evalStructLit(lit *ast.CompositeLit, structT *types.Struct, named types.Type) {
	for i, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			// Positional struct literal: still evaluate for nested
			// violations, but positional field-taint tracking is out of
			// scope (keyed literals are the realistic shape and the one
			// the fixtures use).
			w.eval(elt)
			_ = i
			continue
		}
		valueKind := w.eval(kv.Value)
		ident, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fieldObj := w.pkg.TypesInfo.Uses[ident]
		if fieldObj == nil {
			// Field idents in a composite literal are recorded under
			// Defs in some go/types configurations; fall back.
			fieldObj = w.pkg.TypesInfo.Defs[ident]
		}
		if fieldObj == nil || !valueKind.tainted() {
			continue
		}
		if w.collect {
			w.newFields[fieldObj] = true
		}
	}
	_ = named
}

func (w *chaos4782Walker) evalMapLit(lit *ast.CompositeLit, mapT *types.Map, named types.Type) {
	familyKeyed := types.Identical(mapT.Key(), w.an.facts.familyType)
	textual := chaos4782MayHoldText(mapT.Elem())
	if familyKeyed && textual {
		w.report(lit.Lbrace, fmt.Sprintf(
			"map literal of type %s: key type is the question-family type and the value type can hold text -- the R2 class (codex round 2)", named.String()))
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			w.eval(elt)
			continue
		}
		w.eval(kv.Value)
		if lit2, ok := chaos4782StringLiteral(kv.Key); ok && textual {
			if w.an.facts.wireValueLiterals[lit2.Value] {
				w.report(lit2.Pos(), fmt.Sprintf(
					"map literal of type %s: key %s is one of the family's raw wire values written as a string literal, and the value type can hold text", named.String(), lit2.Value))
			}
		} else {
			w.eval(kv.Key)
		}
	}
}

func (w *chaos4782Walker) evalCompositeElt(elt ast.Expr, _ types.Type) {
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		w.eval(kv.Key)
		w.eval(kv.Value)
		return
	}
	w.eval(elt)
}

// assign updates local taint state for one LHS/RHS pair of an assignment
// or `:=`, and (in collect mode) records field-taint facts for `x.Field =
// taintedExpr`.
func (w *chaos4782Walker) assign(lhs, rhs ast.Expr) {
	kind := w.eval(rhs)
	switch l := lhs.(type) {
	case *ast.Ident:
		if l.Name == "_" {
			return
		}
		obj := w.pkg.TypesInfo.Defs[l]
		if obj == nil {
			obj = w.pkg.TypesInfo.Uses[l]
		}
		if obj == nil {
			return
		}
		if kind.tainted() {
			w.local[obj] = kind
		} else {
			delete(w.local, obj)
		}
	case *ast.SelectorExpr:
		w.eval(l.X)
		if !kind.tainted() {
			return
		}
		fieldObj := w.pkg.TypesInfo.Uses[l.Sel]
		if fieldObj != nil && w.collect {
			w.newFields[fieldObj] = true
		}
	default:
		w.eval(lhs)
	}
}

// walkStmt walks statements IN SOURCE ORDER, threading the mutable local
// taint map forward -- this is what lets `idx := ordinalOf(family);
// table[idx]` see idx as tainted by the time the index expression is
// evaluated. Branches (if/else, switch cases) are walked with the
// PRE-BRANCH state and their taint effects are unioned back in
// conservatively (a var tainted in either arm is treated as tainted after)
// rather than fully merged per-path, which is a deliberate
// over-approximation matching this analysis's fail-closed posture.
func (w *chaos4782Walker) walkStmt(stmt ast.Stmt) {
	switch s := stmt.(type) {
	case nil:
		return
	case *ast.BlockStmt:
		for _, sub := range s.List {
			w.walkStmt(sub)
		}
	case *ast.AssignStmt:
		if len(s.Lhs) == len(s.Rhs) {
			// The common case: one RHS expression per LHS target.
			// Evaluated and assigned pairwise, in source order.
			for i := range s.Rhs {
				w.assign(s.Lhs[i], s.Rhs[i])
			}
		} else {
			// Multi-value assignment (a, b := f(), or a, ok := m[k]):
			// evaluate every RHS once for nested violations, but do not
			// track which specific return/comma-ok value carries taint
			// (documented residual limit) -- every LHS target becomes
			// untainted rather than guessed.
			for _, rhs := range s.Rhs {
				w.eval(rhs)
			}
			for _, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
					if obj := w.pkg.TypesInfo.Defs[id]; obj != nil {
						delete(w.local, obj)
					} else if obj := w.pkg.TypesInfo.Uses[id]; obj != nil {
						delete(w.local, obj)
					}
				}
			}
		}
	case *ast.ExprStmt:
		w.eval(s.X)
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok {
			return
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					w.assign(name, vs.Values[i])
				}
			}
		}
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			k := w.eval(r)
			if k.tainted() && k > w.returnTaint {
				w.returnTaint = k
			}
		}
	case *ast.IfStmt:
		w.walkStmt(s.Init)
		w.eval(s.Cond)
		before := w.snapshotLocal()
		w.walkStmt(s.Body)
		afterThen := w.snapshotLocal()
		w.restoreLocal(before)
		w.walkStmt(s.Else)
		w.unionLocal(afterThen)
	case *ast.SwitchStmt:
		w.walkSwitch(s)
	case *ast.TypeSwitchStmt:
		w.walkStmt(s.Init)
		w.walkStmt(s.Assign)
		for _, clause := range s.Body.List {
			cc := clause.(*ast.CaseClause)
			for _, sub := range cc.Body {
				w.walkStmt(sub)
			}
		}
	case *ast.ForStmt:
		w.walkStmt(s.Init)
		w.eval(s.Cond)
		w.walkStmt(s.Body)
		w.walkStmt(s.Post)
	case *ast.RangeStmt:
		w.walkRange(s)
	case *ast.LabeledStmt:
		w.walkStmt(s.Stmt)
	case *ast.GoStmt:
		w.eval(s.Call)
	case *ast.DeferStmt:
		w.eval(s.Call)
	case *ast.IncDecStmt:
		w.eval(s.X)
	case *ast.SendStmt:
		w.eval(s.Chan)
		w.eval(s.Value)
	default:
		// Empty/branch/select statements etc: nothing to taint.
	}
}

func (w *chaos4782Walker) walkSwitch(s *ast.SwitchStmt) {
	w.walkStmt(s.Init)
	tagKind := w.eval(s.Tag)
	before := w.snapshotLocal()
	var union chaos4782Local
	for _, clause := range s.Body.List {
		cc := clause.(*ast.CaseClause)
		w.restoreLocal(before)
		for _, expr := range cc.List {
			w.eval(expr)
		}
		if tagKind.tainted() {
			for _, sub := range cc.Body {
				chaos4782CheckYieldsLiteral(w, sub)
			}
		}
		for _, sub := range cc.Body {
			w.walkStmt(sub)
		}
		union = chaos4782MergeLocal(union, w.local)
	}
	w.restoreLocal(before)
	if union != nil {
		w.unionLocal(union)
	}
}

// chaos4782CheckYieldsLiteral flags a family-keyed switch arm that returns
// or assigns a string literal -- the family-keyed-prose shape, carried
// over from chaos4735 essentially unchanged (it needs no type resolution
// beyond knowing the tag is tainted, established by the caller).
func chaos4782CheckYieldsLiteral(w *chaos4782Walker, stmt ast.Stmt) {
	var lit *ast.BasicLit
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			if l, ok := chaos4782StringLiteral(chaos4782Unwrap(r)); ok {
				lit = l
			}
		}
	case *ast.AssignStmt:
		for _, r := range s.Rhs {
			if l, ok := chaos4782StringLiteral(chaos4782Unwrap(r)); ok {
				lit = l
			}
		}
	}
	if lit != nil {
		w.report(lit.Pos(), fmt.Sprintf(
			"family-keyed switch arm yields the string literal %s -- the family-keyed-prose class", lit.Value))
	}
}

// chaos4782Unwrap sees through a single conversion call
// (`SomeType("literal")`) to the literal beneath it, so a converted
// literal is still recognized as one.
func chaos4782Unwrap(expr ast.Expr) ast.Expr {
	if call, ok := expr.(*ast.CallExpr); ok && len(call.Args) == 1 {
		return chaos4782Unwrap(call.Args[0])
	}
	if p, ok := expr.(*ast.ParenExpr); ok {
		return chaos4782Unwrap(p.X)
	}
	return expr
}

// walkRange implements the ordinal-taint seed: ranging over a
// family-typed-element array/slice taints the INDEX variable. This is the
// rule that closes R3 -- the position of a family value within the closed
// vocabulary is family-derived information even though no assignment
// chain literally copies the family value into the index.
func (w *chaos4782Walker) walkRange(s *ast.RangeStmt) {
	xT := w.pkg.TypesInfo.TypeOf(s.X)
	w.eval(s.X)
	elemIsFamily := false
	if xT != nil {
		if elem := chaos4782ElemType(xT); elem != nil && types.Identical(elem, w.an.facts.familyType) {
			elemIsFamily = true
		}
	}
	if s.Key != nil {
		if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
			if obj := w.pkg.TypesInfo.Defs[id]; obj != nil {
				if elemIsFamily {
					w.local[obj] = chaos4782TaintDerived
				} else {
					delete(w.local, obj)
				}
			}
		}
	}
	if s.Value != nil {
		if id, ok := s.Value.(*ast.Ident); ok && id.Name != "_" {
			if obj := w.pkg.TypesInfo.Defs[id]; obj != nil {
				if elemIsFamily {
					w.local[obj] = chaos4782TaintRaw
				} else {
					delete(w.local, obj)
				}
			}
		}
	}
	w.walkStmt(s.Body)
}

func (w *chaos4782Walker) snapshotLocal() chaos4782Local {
	c := make(chaos4782Local, len(w.local))
	for k, v := range w.local {
		c[k] = v
	}
	return c
}

func (w *chaos4782Walker) restoreLocal(saved chaos4782Local) {
	w.local = make(chaos4782Local, len(saved))
	for k, v := range saved {
		w.local[k] = v
	}
}

func (w *chaos4782Walker) unionLocal(other chaos4782Local) {
	for k, v := range other {
		if cur, ok := w.local[k]; !ok || v > cur {
			w.local[k] = v
		}
	}
}

func chaos4782MergeLocal(a, b chaos4782Local) chaos4782Local {
	if a == nil {
		out := make(chaos4782Local, len(b))
		for k, v := range b {
			out[k] = v
		}
		return out
	}
	for k, v := range b {
		if cur, ok := a[k]; !ok || v > cur {
			a[k] = v
		}
	}
	return a
}

// chaos4782Run executes one full pass (field-taint collection, then a
// second pass consuming those facts) over every production .go file in
// pkgs, using facts already resolved from THIS SAME load. It iterates the
// two-pass cycle until no NEW field-taint facts appear, and fails the test
// loudly if that does not happen within a generous bound -- per AGENTS.md's
// "a measurement that did not happen must FAIL, loudly": silently capping
// iterations and reporting whatever partial result it has would be exactly
// the false-clean shape this repository's own review discipline forbids.
func chaos4782Run(t *testing.T, pkgs []*packages.Package, facts chaos4782Facts) []chaos4782Violation {
	t.Helper()
	an := &chaos4782Analyzer{facts: facts, fieldTaint: map[types.Object]bool{}}

	const maxIterations = 12
	converged := false
	for iter := 0; iter < maxIterations; iter++ {
		newFields := chaos4782OnePass(an, pkgs, true, nil)
		grew := false
		for f := range newFields {
			if !an.fieldTaint[f] {
				an.fieldTaint[f] = true
				grew = true
			}
		}
		if !grew {
			converged = true
			break
		}
	}
	if !converged {
		t.Fatalf("chaos4782: field-taint pass did not converge within %d iterations -- refusing to report a partial result", maxIterations)
	}

	var violations []chaos4782Violation
	chaos4782OnePass(an, pkgs, false, &violations)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].pos.Filename != violations[j].pos.Filename {
			return violations[i].pos.Filename < violations[j].pos.Filename
		}
		return violations[i].pos.Line < violations[j].pos.Line
	})
	return violations
}

// chaos4782OnePass walks every function body and every package-level
// var/const initializer in pkgs once. In collect mode it returns the
// field-taint facts newly observed (using the CURRENT an.fieldTaint as
// input, so successive calls see prior iterations' facts); otherwise it
// appends violations into *out.
func chaos4782OnePass(an *chaos4782Analyzer, pkgs []*packages.Package, collect bool, out *[]chaos4782Violation) map[types.Object]bool {
	newFields := map[types.Object]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Body == nil {
						continue
					}
					var enclosing types.Object
					if obj := pkg.TypesInfo.Defs[d.Name]; obj != nil {
						enclosing = obj
					}
					w := &chaos4782Walker{
						an:      an,
						pkg:     pkg,
						scope:   chaos4782FuncScope{pkg: pkg, enclosing: enclosing, sanctioned: an.facts.sanctioned[enclosing]},
						local:   chaos4782Local{},
						collect: collect,
					}
					if collect {
						w.newFields = map[types.Object]bool{}
					}
					if d.Recv != nil {
						// Method receiver taint is not modeled (no
						// receiver in the swept roots is family-typed);
						// nothing to seed.
					}
					for _, field := range d.Type.Params.List {
						for _, name := range field.Names {
							if obj := pkg.TypesInfo.Defs[name]; obj != nil {
								if t := obj.Type(); t != nil && types.Identical(t, an.facts.familyType) {
									w.local[obj] = chaos4782TaintRaw
								}
							}
						}
					}
					w.walkStmt(d.Body)
					if collect {
						for f := range w.newFields {
							newFields[f] = true
						}
					}
					if out != nil {
						*out = append(*out, w.violations...)
					}
				case *ast.GenDecl:
					if d.Tok != token.VAR && d.Tok != token.CONST {
						continue
					}
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						var enclosing types.Object
						if len(vs.Names) > 0 {
							enclosing = pkg.TypesInfo.Defs[vs.Names[0]]
						}
						w := &chaos4782Walker{
							an:      an,
							pkg:     pkg,
							scope:   chaos4782FuncScope{pkg: pkg, enclosing: enclosing, sanctioned: an.facts.sanctioned[enclosing]},
							local:   chaos4782Local{},
							collect: collect,
						}
						if collect {
							w.newFields = map[types.Object]bool{}
						}
						for i, name := range vs.Names {
							if i < len(vs.Values) {
								w.assign(name, vs.Values[i])
							}
						}
						if collect {
							for f := range w.newFields {
								newFields[f] = true
							}
						}
						if out != nil {
							*out = append(*out, w.violations...)
						}
					}
				}
			}
			if out != nil {
				chaos4782AssertionA(an, pkg, file, out)
			}
		}
	}
	if collect {
		return newFields
	}
	if out != nil {
		chaos4782DedupeViolations(out)
	}
	return nil
}

// chaos4782AssertionA is the closed-four-purpose-read-list check: any
// reference to a discriminating family CONSTANT whose enclosing
// declaration is not sanctioned by object identity is a violation. Run
// once per file (not per function) since it walks the whole file looking
// for Ident/SelectorExpr uses, tracking which top-level declaration each
// one falls under via a position-ordered scan of d.Decls.
func chaos4782AssertionA(an *chaos4782Analyzer, pkg *packages.Package, file *ast.File, out *[]chaos4782Violation) bool {
	if out == nil {
		return false
	}
	reported := false
	for _, decl := range file.Decls {
		var enclosing types.Object
		var body ast.Node
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if obj := pkg.TypesInfo.Defs[d.Name]; obj != nil {
				enclosing = obj
			}
			body = d
		case *ast.GenDecl:
			if d.Tok != token.VAR && d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
					if obj := pkg.TypesInfo.Defs[vs.Names[0]]; obj != nil {
						enclosing = obj
					}
				}
			}
			body = d
		default:
			continue
		}
		if body == nil || an.facts.sanctioned[enclosing] {
			continue
		}
		ast.Inspect(body, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			obj := pkg.TypesInfo.Uses[ident]
			if obj == nil || !an.facts.discriminating[obj] {
				return true
			}
			*out = append(*out, chaos4782Violation{
				pos: pkg.Fset.Position(ident.Pos()),
				message: fmt.Sprintf(
					"unsanctioned reference to the discriminating family constant %s.%s outside the closed four-purpose read list -- a new purpose for reading the family needs a ruling before it ships, not a wider allowlist",
					obj.Pkg().Path(), obj.Name()),
			})
			reported = true
			return true
		})
	}
	return reported
}

func chaos4782DedupeViolations(out *[]chaos4782Violation) {
	seen := map[string]bool{}
	var deduped []chaos4782Violation
	for _, v := range *out {
		key := v.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, v)
	}
	*out = deduped
}

// chaos4782LoadRoot walks up from the current test's working directory to
// the module root -- same technique as chaos4735's
// repositoryRootForFamilySweep, needed so package loading works
// regardless of which package `go test` happens to invoke this from.
func chaos4782LoadRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

func chaos4782LoadPackages(t *testing.T, dir string, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Dir: dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedSyntax,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	nerr := 0
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		nerr += len(p.Errors)
		for _, e := range p.Errors {
			t.Errorf("load error in %s: %v", p.PkgPath, e)
		}
	})
	if nerr > 0 {
		t.Fatalf("%d package load/type errors -- see above; the analysis refuses to run on a program that did not fully type-check", nerr)
	}
	if len(pkgs) == 0 {
		t.Fatalf("packages.Load returned zero packages for %v -- the sweep would pass vacuously", patterns)
	}
	return pkgs
}

// TestChaos4782TypeAwareFamilyLanguageSweep is the production gate: it
// loads the four swept roots with full type information and asserts zero
// violations. This is the test CHAOS-4782 exists to add; the mutation
// proofs in the lane handoff reintroduce each historical construction in a
// throwaway commit and confirm THIS test turns red, then restore and
// confirm green again.
func TestChaos4782TypeAwareFamilyLanguageSweep(t *testing.T) {
	root := chaos4782LoadRoot(t)
	pkgs := chaos4782LoadPackages(t, root, chaos4782SweptImportPaths...)
	facts := chaos4782ResolveFacts(t, pkgs, chaos4782SanctionedFiles)
	violations := chaos4782Run(t, pkgs, facts)

	// False-positive guard (CHAOS-4782 acceptance): the vote-tally maps
	// keyed by the family type with an int value must never be flagged.
	// Asserted directly against the FULL violation list (not just "the
	// sweep passed") so a regression here fails on its own message rather
	// than merging into "some violation somewhere".
	for _, v := range violations {
		if strings.Contains(v.pos.Filename, "chaos4632_question_family_consensus.go") ||
			strings.Contains(v.pos.Filename, "chaos4632_question_family_telemetry.go") {
			t.Errorf("false positive on a known-clean vote-tally file: %s", v)
		}
	}

	if len(violations) > 0 {
		var lines []string
		for _, v := range violations {
			lines = append(lines, v.String())
		}
		t.Errorf("type-aware family-language sweep found %d violation(s):\n  %s",
			len(violations), strings.Join(lines, "\n  "))
	}
}

// chaos4782Fixture names one historical/novel construction fixture and
// what the sweep must say about it.
type chaos4782Fixture struct {
	name        string
	importPath  string
	description string
}

var chaos4782Fixtures = []chaos4782Fixture{
	{
		name:        "R1_raw_string_literal_after_conversion",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/chaos4782/r1_raw_literal",
		description: "codex round 1: string(family) == \"subject_investigation\"",
	},
	{
		name:        "R2_named_string_underlying_map_value",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/chaos4782/r2_named_alias",
		description: "codex round 2: map[QuestionFamily]phrase, phrase a named string type",
	},
	{
		name:        "R3_ordinal_indirection_into_text_table",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/chaos4782/r3_ordinal_index",
		description: "codex round 3, and the reason this ticket exists: family -> vocabulary position -> []string index",
	},
	{
		name:        "R4_struct_field_relay_across_function_boundary",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/chaos4782/r4_struct_field_relay",
		description: "not caught by the CHAOS-4735 heuristic (its own doc names this gap): ordinal computed in one function, stored on a struct field, consumed by a different function",
	},
}

// TestChaos4782CatchesHistoricalConstructions loads each fixture as its
// OWN program (packages.Load call) -- go/types object identity is only
// comparable within one load, so facts are re-resolved per fixture rather
// than reused from the production-roots load. Each fixture is asserted to
// produce AT LEAST ONE violation; the fixture's own doc comment states
// which class it is, so a human reviewing a future failure here has the
// history without re-deriving it.
func TestChaos4782CatchesHistoricalConstructions(t *testing.T) {
	root := chaos4782LoadRoot(t)
	for _, fx := range chaos4782Fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			patterns := []string{
				fx.importPath,
				"github.com/full-chaos/dev-health-acr/internal/contracts/v1/...",
			}
			pkgs := chaos4782LoadPackages(t, root, patterns...)
			// Only the contracts/v1 purpose file, not the full
			// chaos4782SanctionedFiles: the other three sanctioned files
			// live in internal/contextfabric, which these standalone
			// fixtures do not import and this test does not load (paying
			// the full production-roots load cost per fixture would not
			// change which violation the fixture is meant to exercise).
			// Omitting the contracts/v1 file's own exemption entirely
			// would bury each fixture's real finding in noise: EVERY
			// discriminating constant's own declaration in
			// context_fabric_answer_plan.go would itself read as an
			// "unsanctioned reference".
			facts := chaos4782ResolveFacts(t, pkgs, chaos4782ContractsPurposeFile)
			violations := chaos4782Run(t, pkgs, facts)
			if len(violations) == 0 {
				t.Fatalf("RED-FIRST FAILURE: fixture %s (%s) produced ZERO violations -- the gate does not catch this historical construction", fx.name, fx.description)
			}
			t.Logf("%s: %d violation(s):", fx.name, len(violations))
			for _, v := range violations {
				t.Logf("  %s", v)
			}
		})
	}
}
