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
//   - SANCTIONED READERS are exempted by go/types OBJECT IDENTITY of an
//     EXPLICIT, CLOSED LIST of individual declarations (familyGateSanctionedSymbols)
//     -- not by file path, and (codex round 1, P2) not by "every
//     declaration in a purpose file" either, which is extensionally the
//     same file-level granularity as a path allowlist. Each read site is
//     sanctioned only if its ENCLOSING top-level declaration resolves,
//     through the type checker, to one of the specific listed objects. A
//     new, unrelated function added to one of the four purpose FILES is
//     NOT sanctioned; only the specific declarations design 13.4.3 names
//     as the four purposes are (the precedence table that PRODUCES the
//     family, the registry -- which is also where budget-profile
//     selection actually lives, see sweep-for-4782.go.txt's correction of
//     design row 8 -- LookupQuestionFamily, and the vocabulary
//     declarations on both sides of the wire boundary).
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

// familyGateSweptImportPaths are the four production roots this sweep
// analyzes, as Go import paths (not file paths) so golang.org/x/tools/go/packages
// resolves them with full type information, recursively, in one
// type-checking session -- required for go/types object identity to be
// comparable across the four packages at all.
var familyGateSweptImportPaths = []string{
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/...",
	"github.com/full-chaos/dev-health-acr/internal/api/...",
	"github.com/full-chaos/dev-health-acr/internal/contracts/v1/...",
	"github.com/full-chaos/dev-health-acr/internal/mcp/...",
}

// familyGateContextFabricPkgPath and familyGateContractsPkgPath are the two
// packages that own a sanctioned declaration.
const (
	familyGateContextFabricPkgPath = "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	familyGateContractsPkgPath     = "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// familyGateSanctionedSymbol names ONE declared symbol that constitutes a
// sanctioned reader, by (package, name) -- resolved via go/types package
// SCOPE LOOKUP, which returns the declaration's *types.Object directly.
// `file` is a belt-and-braces check, not the enforcement mechanism: after
// resolving the object, its declaration position must fall in this file,
// or resolution fails loudly (renamed or moved without updating this
// list).
//
// CODEX ROUND 1, P2, EXECUTED: an earlier version of this file sanctioned
// EVERY top-level declaration found in the four purpose FILES. That is
// extensionally the same file-level granularity chaos4735's
// sanctionedFamilyReadSites had (a NEW, unrelated function added to one of
// these files was sanctioned merely by sharing a file with a real
// purpose), just reached through go/types identity instead of a path
// string -- identity of "everything in this file" is not narrower than
// "this file". The fix is this EXPLICIT, closed list of the declarations
// that actually constitute each purpose. This is not the "allowlist of
// violation SHAPES" the ticket warns against (that anti-pattern was about
// enumerating ways prose could be authored); this is the sanctioned-READER
// list design 13.4.3 already calls a CLOSED set of four purposes -- a
// human names it once, and each individual read site is checked against it
// by identity rather than by path or name string.
type familyGateSanctionedSymbol struct {
	pkgPath string
	file    string // repo-relative, for the position check only
	name    string
}

// familyGateSanctionedSymbols is the full closed list: every declaration
// that legitimately reads, produces, or dispatches on the question family,
// across all four purposes. Derived by running this gate with an EMPTY
// sanctioned set against the real tree and mapping every resulting
// violation back to its enclosing declaration (recorded so the next person
// re-deriving this list has the method, not just the result).
var familyGateSanctionedSymbols = []familyGateSanctionedSymbol{
	// Purpose 1: the precedence table that PRODUCES the family.
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_precedence.go", "UnreachableQuestionFamilies"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_precedence.go", "familyIsUnreachable"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_precedence.go", "ResolveFamilyForSample"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_precedence.go", "precedenceFamily"},
	// Purpose 2: the registry -- LookupQuestionFamily, which is also where
	// budget-profile selection lives (sweep-for-4782.go.txt's correction
	// of design 13.9a row 8) -- and the table its lookup reads.
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_registry.go", "questionFamilyDefinitions"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_registry.go", "LookupQuestionFamily"},
	// Purpose 3: the package-local vocabulary aliases.
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamily"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilySubjectInvestigation"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyDiscoveredCohortRanking"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyScopedCohortStatus"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyGroupedCohortStatus"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyExplicitComparison"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyTrend"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyInvestmentAllocation"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyUnclassified"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "questionFamilies"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "QuestionFamilyVocabulary"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "ValidQuestionFamily"},
	{familyGateContextFabricPkgPath, "internal/contextfabric/chaos4632_question_family_vocab.go", "SanitizeQuestionFamily"},
	// Purpose 4: the wire vocabulary declaration itself.
	familyGateContractsPurposeSymbolType,
	familyGateContractsPurposeSymbolOrderedArray,
	familyGateContractsPurposeSymbolSubjectInvestigation,
	familyGateContractsPurposeSymbolDiscoveredCohortRanking,
	familyGateContractsPurposeSymbolScopedCohortStatus,
	familyGateContractsPurposeSymbolGroupedCohortStatus,
	familyGateContractsPurposeSymbolExplicitComparison,
	familyGateContractsPurposeSymbolTrend,
	familyGateContractsPurposeSymbolInvestmentAllocation,
	familyGateContractsPurposeSymbolUnclassified,
	familyGateContractsPurposeSymbolVocabulary,
	familyGateContractsPurposeSymbolValid,
	familyGateContractsPurposeSymbolCount,
}

// The purpose-4 symbols are declared individually (not inline in the slice
// literal above) because TestFamilyTextGateCatchesHistoricalConstructions's
// fixture loads carry ONLY purpose 4 (their loads never include
// internal/contextfabric) and need the same list under a second name.
const familyGateContractsFile = "internal/contracts/v1/context_fabric_answer_plan.go"

var (
	familyGateContractsPurposeSymbolType                    = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamily"}
	familyGateContractsPurposeSymbolOrderedArray            = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "contextFabricQuestionFamilies"}
	familyGateContractsPurposeSymbolSubjectInvestigation    = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilySubjectInvestigation"}
	familyGateContractsPurposeSymbolDiscoveredCohortRanking = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyDiscoveredCohortRanking"}
	familyGateContractsPurposeSymbolScopedCohortStatus      = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyScopedCohortStatus"}
	familyGateContractsPurposeSymbolGroupedCohortStatus     = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyGroupedCohortStatus"}
	familyGateContractsPurposeSymbolExplicitComparison      = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyExplicitComparison"}
	familyGateContractsPurposeSymbolTrend                   = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyTrend"}
	familyGateContractsPurposeSymbolInvestmentAllocation    = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyInvestmentAllocation"}
	familyGateContractsPurposeSymbolUnclassified            = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyUnclassified"}
	familyGateContractsPurposeSymbolVocabulary              = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyVocabulary"}
	familyGateContractsPurposeSymbolValid                   = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ValidContextFabricQuestionFamily"}
	familyGateContractsPurposeSymbolCount                   = familyGateSanctionedSymbol{familyGateContractsPkgPath, familyGateContractsFile, "ContextFabricQuestionFamilyCount"}
)

// familyGateContractsPurposeSymbols is purpose 4 alone, used by
// TestFamilyTextGateCatchesHistoricalConstructions, whose fixture loads
// never include internal/contextfabric (purposes 1-3).
var familyGateContractsPurposeSymbols = []familyGateSanctionedSymbol{
	familyGateContractsPurposeSymbolType,
	familyGateContractsPurposeSymbolOrderedArray,
	familyGateContractsPurposeSymbolSubjectInvestigation,
	familyGateContractsPurposeSymbolDiscoveredCohortRanking,
	familyGateContractsPurposeSymbolScopedCohortStatus,
	familyGateContractsPurposeSymbolGroupedCohortStatus,
	familyGateContractsPurposeSymbolExplicitComparison,
	familyGateContractsPurposeSymbolTrend,
	familyGateContractsPurposeSymbolInvestmentAllocation,
	familyGateContractsPurposeSymbolUnclassified,
	familyGateContractsPurposeSymbolVocabulary,
	familyGateContractsPurposeSymbolValid,
	familyGateContractsPurposeSymbolCount,
}

// familyGateViolation is one finding: a file:line plus a human message.
type familyGateViolation struct {
	pos     token.Position
	message string
}

func (v familyGateViolation) String() string {
	return fmt.Sprintf("%s (%s)", v.pos, v.message)
}

// familyGateFacts is everything the analysis resolves ONCE from a loaded
// program before walking it: the family type itself, its constants (both
// spellings, matching chaos4735's non-vacuity discipline), the closed
// vocabulary's wire-value string literals (for the raw-keyed-table rule,
// which needs no type information at all), and the sanctioned object set.
type familyGateFacts struct {
	fset               *token.FileSet
	familyType         types.Type
	discriminating     map[types.Object]bool // family constants EXCLUDING the unclassified sentinel
	allFamilyConstants map[types.Object]bool // INCLUDING the sentinel; only used for the non-vacuity count
	wireValueLiterals  map[string]bool       // `"subject_investigation"` etc, quotes included
	sanctioned         map[types.Object]bool // sanctioned readers, by declared object
}

// familyGateResolveFacts derives familyGateFacts from a single loaded
// program. It must be called on the SAME *packages.Package slice that will
// be walked, and on that slice ONLY -- go/types object identity (used
// throughout via pointer equality) is only meaningful within one
// type-checking session.
func familyGateResolveFacts(t *testing.T, pkgs []*packages.Package, sanctionedSymbols []familyGateSanctionedSymbol) familyGateFacts {
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
	// -- see TestFamilyTextGateCatchesHistoricalConstructions), so it wants
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

	sanctioned := familyGateResolveSanctionedObjects(t, pkgs, sanctionedSymbols)

	return familyGateFacts{
		familyType:         familyType,
		discriminating:     discriminating,
		allFamilyConstants: allConstants,
		wireValueLiterals:  wireValues,
		sanctioned:         sanctioned,
	}
}

// familyGateResolveSanctionedObjects walks the top-level declarations of
// the named files (matched by suffix against each package's
// CompiledGoFiles, so it works regardless of which loaded package a file
// belongs to) and returns the *types.Object each one declares: every
// function's object, and every name in every var/const spec. A read is
// sanctioned iff its enclosing declaration resolves to one of these
// objects.
func familyGateResolveSanctionedObjects(t *testing.T, pkgs []*packages.Package, symbols []familyGateSanctionedSymbol) map[types.Object]bool {
	t.Helper()
	sanctioned := map[types.Object]bool{}
	if len(symbols) == 0 {
		// Historical-construction fixtures are loaded standalone (see
		// TestFamilyTextGateCatchesHistoricalConstructions): most never
		// contain a sanctioned declaration at all. An empty input here
		// means "exempt nothing", not "resolution failed".
		return sanctioned
	}
	pkgByPath := map[string]*packages.Package{}
	for _, p := range pkgs {
		pkgByPath[p.PkgPath] = p
	}

	for _, sym := range symbols {
		p, ok := pkgByPath[sym.pkgPath]
		if !ok {
			t.Fatalf("sanctioned symbol %s.%s: package %s is not part of the loaded program", sym.pkgPath, sym.name, sym.pkgPath)
		}
		obj := p.Types.Scope().Lookup(sym.name)
		if obj == nil {
			t.Fatalf("sanctioned symbol %s.%s not found in %s -- renamed or moved?", sym.pkgPath, sym.name, sym.pkgPath)
		}
		gotFile := filepath.ToSlash(p.Fset.Position(obj.Pos()).Filename)
		if !strings.HasSuffix(gotFile, "/"+sym.file) && gotFile != sym.file {
			t.Fatalf("sanctioned symbol %s.%s resolved to %s, want a file ending in %s -- moved without updating familyGateSanctionedSymbols?", sym.pkgPath, sym.name, gotFile, sym.file)
		}
		sanctioned[obj] = true
	}
	if len(sanctioned) == 0 {
		t.Fatalf("resolved zero sanctioned objects from %d symbols -- the sweep would exempt nothing, which is not the same as a correctly narrow allowlist", len(symbols))
	}
	return sanctioned
}

// familyGateMayHoldText fails CLOSED: everything can hold text unless it is
// one of a small set of builtins that provably cannot. Carried over from
// chaos4735_family_language_sweep_test.go's mayHoldText (found by codex
// round 2): matching textual types by NAME was the mistake; inverting the
// default is what survives a renamed textual type.
func familyGateMayHoldText(t types.Type) bool {
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

// familyGateFuncScope is the function or file-level declaration currently
// being walked, used both to seed per-declaration local taint state and to
// decide whether violations found inside it are exempt.
type familyGateFuncScope struct {
	pkg        *packages.Package
	enclosing  types.Object // the top-level object this code lives inside (nil for none resolvable)
	sanctioned bool
}

// familyGateAnalyzer runs the two-pass, field-sensitive taint walk over a
// loaded program and reports violations. It is reusable across the main
// production-roots run and each historical-construction fixture: callers
// supply the facts (family type/constants/sanctioned set) resolved from
// THEIR OWN load, since go/types object identity does not survive across
// separate packages.Load calls.
type familyGateAnalyzer struct {
	facts      familyGateFacts
	fieldTaint map[types.Object]bool // struct FIELD objects known to carry family-derived data
}

// familyGateTaintKind distinguishes "this expression's value is exactly a
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
type familyGateTaintKind int

const (
	familyGateNotTainted familyGateTaintKind = iota
	familyGateTaintRaw
	familyGateTaintDerived
)

func (k familyGateTaintKind) tainted() bool { return k != familyGateNotTainted }

// familyGateLocal is the per-function taint state: which local objects
// (params, `:=`-declared vars) are currently tainted, and at which kind.
type familyGateLocal map[types.Object]familyGateTaintKind

// familyGateWalker carries the state for one pass over one function body.
type familyGateWalker struct {
	an          *familyGateAnalyzer
	pkg         *packages.Package
	scope       familyGateFuncScope
	local       familyGateLocal
	collect     bool // pass 1: record field-taint facts instead of reporting
	newFields   map[types.Object]bool
	violations  []familyGateViolation
	returnTaint familyGateTaintKind
}

func (w *familyGateWalker) report(pos token.Pos, message string) {
	if w.scope.sanctioned || w.collect {
		return
	}
	w.violations = append(w.violations, familyGateViolation{pos: w.pkg.Fset.Position(pos), message: message})
}

// isConversion reports whether call is a type conversion (as opposed to a
// function call) by asking whether its Fun names a TYPE, not a value --
// this is what lets taint survive `string(family)` / `SomeAlias(family)`.
func (w *familyGateWalker) isConversion(call *ast.CallExpr) bool {
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
func (w *familyGateWalker) eval(expr ast.Expr) familyGateTaintKind {
	switch e := expr.(type) {
	case nil:
		return familyGateNotTainted
	case *ast.ParenExpr:
		return w.eval(e.X)
	case *ast.Ident:
		if obj := w.pkg.TypesInfo.Uses[e]; obj != nil {
			if k, ok := w.local[obj]; ok {
				return k
			}
			if w.an.fieldTaint[obj] {
				return familyGateTaintDerived
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
			return familyGateTaintDerived
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
		return familyGateNotTainted
	case *ast.IndexExpr:
		containerT := w.pkg.TypesInfo.TypeOf(e.X)
		indexKind := w.eval(e.Index)
		w.eval(e.X)
		if indexKind.tainted() && containerT != nil {
			elem := familyGateElemType(containerT)
			if elem != nil && familyGateMayHoldText(elem) {
				w.report(e.Pos(), fmt.Sprintf(
					"index/key into %s is derived from a question-family value and the container can hold text -- this is the R3/R5 class: a family value's position or identity selects a text-yielding entry outside a sanctioned reader",
					containerT.String()))
			}
		}
		return familyGateNotTainted
	case *ast.BinaryExpr:
		lk := w.eval(e.X)
		rk := w.eval(e.Y)
		if (e.Op == token.EQL || e.Op == token.NEQ) && (lk.tainted() || rk.tainted()) {
			if lit, litSide := familyGateStringLiteral(e.X); litSide {
				w.checkLiteralCompare(e, lit)
			}
			if lit, litSide := familyGateStringLiteral(e.Y); litSide {
				w.checkLiteralCompare(e, lit)
			}
		}
		return familyGateNotTainted
	case *ast.UnaryExpr:
		return w.eval(e.X)
	case *ast.CompositeLit:
		return w.evalCompositeLit(e)
	default:
		return w.rawIfFamilyTyped(expr)
	}
}

// rawIfFamilyTyped is the SEED rule: any expression whose go/types-resolved
// type is identical to the family type is tainted, raw, regardless of how
// it is spelled -- this is what makes seeding type-based rather than
// name-based.
func (w *familyGateWalker) rawIfFamilyTyped(expr ast.Expr) familyGateTaintKind {
	t := w.pkg.TypesInfo.TypeOf(expr)
	if t != nil && types.Identical(t, w.an.facts.familyType) {
		return familyGateTaintRaw
	}
	return familyGateNotTainted
}

// checkLiteralCompare implements the R1 rule: a tainted value compared to a
// non-empty string literal. The empty-string literal is excluded on the
// same reasoning chaos4735 uses: `Family == ""` is an emptiness test, not a
// discriminating read.
func (w *familyGateWalker) checkLiteralCompare(e *ast.BinaryExpr, lit *ast.BasicLit) {
	if lit.Value == `""` {
		return
	}
	w.report(e.OpPos, fmt.Sprintf(
		"a question-family-derived value is compared to the string literal %s instead of a closed-vocabulary constant -- the R1 class (codex round 1)", lit.Value))
}

func familyGateStringLiteral(expr ast.Expr) (*ast.BasicLit, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			return e, true
		}
	case *ast.ParenExpr:
		return familyGateStringLiteral(e.X)
	}
	return nil, false
}

// familyGateElemType returns the element type of an array/slice/map, or nil
// if t is none of those (the analysis only reasons about index/key
// containers).
func familyGateElemType(t types.Type) types.Type {
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

// evalCompositeLit handles three independent things: (1) struct-literal
// field values, which feed the field-taint pass (`T{Field: taintedExpr}`)
// AND make the composite literal EXPRESSION ITSELF tainted whenever any
// field value is -- see evalStructLit's doc comment for why this matters;
// (2) map/array/slice literals, which are checked against the two
// structural rules carried over from chaos4735 -- a map whose KEY TYPE is
// family-identical and whose VALUE type may hold text (R2's shape, closed
// structurally as well as via the general index rule above), and a map
// literal keyed by the family's RAW WIRE VALUE written as a string literal
// (no family-typed expression appears at all, so the type/dataflow rules
// above cannot see it -- this one stays purely textual, same as chaos4735
// rule 2); (3) any element of a composite literal that is itself tainted
// makes the literal tainted-derived, so a tainted array/slice/map VALUE
// used downstream (e.g. as an index or another literal's field) is still
// seen.
func (w *familyGateWalker) evalCompositeLit(lit *ast.CompositeLit) familyGateTaintKind {
	t := w.pkg.TypesInfo.TypeOf(lit)
	if t == nil {
		tainted := familyGateNotTainted
		for _, elt := range lit.Elts {
			if k := w.evalCompositeElt(elt); k.tainted() {
				tainted = familyGateTaintDerived
			}
		}
		return tainted
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		return w.evalStructLit(lit, u, t)
	case *types.Map:
		return w.evalMapLit(lit, u, t)
	default:
		tainted := familyGateNotTainted
		for _, elt := range lit.Elts {
			if k := w.evalCompositeElt(elt); k.tainted() {
				tainted = familyGateTaintDerived
			}
		}
		return tainted
	}
}

// evalStructLit records field-taint facts (unchanged) AND now returns
// whether the LITERAL ITSELF is tainted, when any field value is.
//
// CODEX ROUND 1, P1, EXECUTED: without this, a composite literal that
// WRAPS a tainted value (e.g. `struct{ Family QuestionFamily }{Family:
// family}`) evaluated as untainted at the expression level, so using it
// directly as a MAP KEY or an INDEX -- rather than assigning it to a named
// variable's field, which the field-taint pass already covered -- was
// invisible to every sink rule. A struct VALUE that carries a tainted
// field is itself family-derived data; wrapping does not launder it.
func (w *familyGateWalker) evalStructLit(lit *ast.CompositeLit, structT *types.Struct, named types.Type) familyGateTaintKind {
	litTainted := familyGateNotTainted
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			// Positional struct literal: still evaluate for nested
			// violations and literal-level taint, but positional
			// field-taint tracking (the GLOBAL, cross-function pass) is
			// out of scope (keyed literals are the realistic shape and
			// the one the fixtures use).
			if w.eval(elt).tainted() {
				litTainted = familyGateTaintDerived
			}
			continue
		}
		valueKind := w.eval(kv.Value)
		if valueKind.tainted() {
			litTainted = familyGateTaintDerived
		}
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
	_ = structT
	return litTainted
}

func (w *familyGateWalker) evalMapLit(lit *ast.CompositeLit, mapT *types.Map, named types.Type) familyGateTaintKind {
	familyKeyed := types.Identical(mapT.Key(), w.an.facts.familyType)
	textual := familyGateMayHoldText(mapT.Elem())
	if familyKeyed && textual {
		w.report(lit.Lbrace, fmt.Sprintf(
			"map literal of type %s: key type is the question-family type and the value type can hold text -- the R2 class (codex round 2)", named.String()))
	}
	litTainted := familyGateNotTainted
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			if w.eval(elt).tainted() {
				litTainted = familyGateTaintDerived
			}
			continue
		}
		if w.eval(kv.Value).tainted() {
			litTainted = familyGateTaintDerived
		}
		if lit2, ok := familyGateStringLiteral(kv.Key); ok && textual {
			if w.an.facts.wireValueLiterals[lit2.Value] {
				w.report(lit2.Pos(), fmt.Sprintf(
					"map literal of type %s: key %s is one of the family's raw wire values written as a string literal, and the value type can hold text", named.String(), lit2.Value))
			}
		} else if w.eval(kv.Key).tainted() {
			litTainted = familyGateTaintDerived
		}
	}
	return litTainted
}

func (w *familyGateWalker) evalCompositeElt(elt ast.Expr) familyGateTaintKind {
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		keyKind := w.eval(kv.Key)
		valueKind := w.eval(kv.Value)
		if keyKind.tainted() || valueKind.tainted() {
			return familyGateTaintDerived
		}
		return familyGateNotTainted
	}
	return w.eval(elt)
}

// assign updates local taint state for one LHS/RHS pair of an assignment
// or `:=`, and (in collect mode) records field-taint facts for `x.Field =
// taintedExpr`.
func (w *familyGateWalker) assign(lhs, rhs ast.Expr) {
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
func (w *familyGateWalker) walkStmt(stmt ast.Stmt) {
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

func (w *familyGateWalker) walkSwitch(s *ast.SwitchStmt) {
	w.walkStmt(s.Init)
	tagKind := w.eval(s.Tag)
	before := w.snapshotLocal()
	var union familyGateLocal
	for _, clause := range s.Body.List {
		cc := clause.(*ast.CaseClause)
		w.restoreLocal(before)
		for _, expr := range cc.List {
			w.eval(expr)
		}
		if tagKind.tainted() {
			for _, sub := range cc.Body {
				familyGateCheckYieldsLiteral(w, sub)
			}
		}
		for _, sub := range cc.Body {
			w.walkStmt(sub)
		}
		union = familyGateMergeLocal(union, w.local)
	}
	w.restoreLocal(before)
	if union != nil {
		w.unionLocal(union)
	}
}

// familyGateCheckYieldsLiteral flags a family-keyed switch arm that returns
// or assigns a string literal -- the family-keyed-prose shape, carried
// over from chaos4735 essentially unchanged (it needs no type resolution
// beyond knowing the tag is tainted, established by the caller).
func familyGateCheckYieldsLiteral(w *familyGateWalker, stmt ast.Stmt) {
	var lit *ast.BasicLit
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		for _, r := range s.Results {
			if l, ok := familyGateStringLiteral(familyGateUnwrap(r)); ok {
				lit = l
			}
		}
	case *ast.AssignStmt:
		for _, r := range s.Rhs {
			if l, ok := familyGateStringLiteral(familyGateUnwrap(r)); ok {
				lit = l
			}
		}
	}
	if lit != nil {
		w.report(lit.Pos(), fmt.Sprintf(
			"family-keyed switch arm yields the string literal %s -- the family-keyed-prose class", lit.Value))
	}
}

// familyGateUnwrap sees through a single conversion call
// (`SomeType("literal")`) to the literal beneath it, so a converted
// literal is still recognized as one.
func familyGateUnwrap(expr ast.Expr) ast.Expr {
	if call, ok := expr.(*ast.CallExpr); ok && len(call.Args) == 1 {
		return familyGateUnwrap(call.Args[0])
	}
	if p, ok := expr.(*ast.ParenExpr); ok {
		return familyGateUnwrap(p.X)
	}
	return expr
}

// walkRange implements the ordinal-taint seed: ranging over a
// family-typed-element array/slice taints the INDEX variable. This is the
// rule that closes R3 -- the position of a family value within the closed
// vocabulary is family-derived information even though no assignment
// chain literally copies the family value into the index.
func (w *familyGateWalker) walkRange(s *ast.RangeStmt) {
	xT := w.pkg.TypesInfo.TypeOf(s.X)
	w.eval(s.X)
	elemIsFamily := false
	if xT != nil {
		if elem := familyGateElemType(xT); elem != nil && types.Identical(elem, w.an.facts.familyType) {
			elemIsFamily = true
		}
	}
	if s.Key != nil {
		if id, ok := s.Key.(*ast.Ident); ok && id.Name != "_" {
			if obj := w.pkg.TypesInfo.Defs[id]; obj != nil {
				if elemIsFamily {
					w.local[obj] = familyGateTaintDerived
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
					w.local[obj] = familyGateTaintRaw
				} else {
					delete(w.local, obj)
				}
			}
		}
	}
	w.walkStmt(s.Body)
}

func (w *familyGateWalker) snapshotLocal() familyGateLocal {
	c := make(familyGateLocal, len(w.local))
	for k, v := range w.local {
		c[k] = v
	}
	return c
}

func (w *familyGateWalker) restoreLocal(saved familyGateLocal) {
	w.local = make(familyGateLocal, len(saved))
	for k, v := range saved {
		w.local[k] = v
	}
}

func (w *familyGateWalker) unionLocal(other familyGateLocal) {
	for k, v := range other {
		if cur, ok := w.local[k]; !ok || v > cur {
			w.local[k] = v
		}
	}
}

func familyGateMergeLocal(a, b familyGateLocal) familyGateLocal {
	if a == nil {
		out := make(familyGateLocal, len(b))
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

// familyGateRun executes one full pass (field-taint collection, then a
// second pass consuming those facts) over every production .go file in
// pkgs, using facts already resolved from THIS SAME load. It iterates the
// two-pass cycle until no NEW field-taint facts appear, and fails the test
// loudly if that does not happen within a generous bound -- per AGENTS.md's
// "a measurement that did not happen must FAIL, loudly": silently capping
// iterations and reporting whatever partial result it has would be exactly
// the false-clean shape this repository's own review discipline forbids.
func familyGateRun(t *testing.T, pkgs []*packages.Package, facts familyGateFacts) []familyGateViolation {
	t.Helper()
	an := &familyGateAnalyzer{facts: facts, fieldTaint: map[types.Object]bool{}}

	const maxIterations = 12
	converged := false
	for iter := 0; iter < maxIterations; iter++ {
		newFields := familyGateOnePass(an, pkgs, true, nil)
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
		t.Fatalf("familyGate: field-taint pass did not converge within %d iterations -- refusing to report a partial result", maxIterations)
	}

	var violations []familyGateViolation
	familyGateOnePass(an, pkgs, false, &violations)
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].pos.Filename != violations[j].pos.Filename {
			return violations[i].pos.Filename < violations[j].pos.Filename
		}
		return violations[i].pos.Line < violations[j].pos.Line
	})
	return violations
}

// familyGateOnePass walks every function body and every package-level
// var/const initializer in pkgs once. In collect mode it returns the
// field-taint facts newly observed (using the CURRENT an.fieldTaint as
// input, so successive calls see prior iterations' facts); otherwise it
// appends violations into *out.
func familyGateOnePass(an *familyGateAnalyzer, pkgs []*packages.Package, collect bool, out *[]familyGateViolation) map[types.Object]bool {
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
					w := &familyGateWalker{
						an:      an,
						pkg:     pkg,
						scope:   familyGateFuncScope{pkg: pkg, enclosing: enclosing, sanctioned: an.facts.sanctioned[enclosing]},
						local:   familyGateLocal{},
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
									w.local[obj] = familyGateTaintRaw
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
						w := &familyGateWalker{
							an:      an,
							pkg:     pkg,
							scope:   familyGateFuncScope{pkg: pkg, enclosing: enclosing, sanctioned: an.facts.sanctioned[enclosing]},
							local:   familyGateLocal{},
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
				familyGateAssertionA(an, pkg, file, out)
			}
		}
	}
	if collect {
		return newFields
	}
	if out != nil {
		familyGateDedupeViolations(out)
	}
	return nil
}

// familyGateAssertionA is the closed-four-purpose-read-list check: any
// reference to a discriminating family CONSTANT whose enclosing
// declaration is not sanctioned by object identity is a violation. Run
// once per file (not per function) since it walks the whole file looking
// for Ident/SelectorExpr uses, tracking which top-level declaration each
// one falls under via a position-ordered scan of d.Decls.
func familyGateAssertionA(an *familyGateAnalyzer, pkg *packages.Package, file *ast.File, out *[]familyGateViolation) bool {
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
			*out = append(*out, familyGateViolation{
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

func familyGateDedupeViolations(out *[]familyGateViolation) {
	seen := map[string]bool{}
	var deduped []familyGateViolation
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

// familyGateLoadRoot walks up from the current test's working directory to
// the module root -- same technique as chaos4735's
// repositoryRootForFamilySweep, needed so package loading works
// regardless of which package `go test` happens to invoke this from.
func familyGateLoadRoot(t *testing.T) string {
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

// familyGateBuildFlags pins the build tags this load compiles under to
// EXACTLY what the real binaries and CI build with.
//
// CODEX ROUND 1, P3, EXECUTED: a production file gated behind an
// unrecognized build tag compiled cleanly (`go build`/`go vet`/`go test
// -tags <tag>`) while packages.Load's default (empty BuildFlags, i.e. no
// tags) silently excluded it -- no error, just absence. Checked at
// gate-authoring time (2026-09-02): NEITHER the Makefile NOR
// .github/workflows/ci.yml passes `-tags` to any build/vet/test
// invocation anywhere in this repository, so "no tags" is not a
// convenient default here -- it is what CI actually builds with. Pinning
// it explicitly (rather than leaving the zero value to mean the same
// thing implicitly) makes that coupling visible: if this repository ever
// adopts a build tag for production code, updating this constant is the
// forcing function, not an afterthought this analysis would otherwise
// drift out of sync with silently.
var familyGateBuildFlags = []string{}

func familyGateLoadPackages(t *testing.T, dir string, patterns ...string) []*packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Dir: dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedSyntax,
		Tests:      false,
		BuildFlags: familyGateBuildFlags,
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

// TestNoServedTextIsDerivedFromQuestionFamily is the production gate: it
// loads the four swept roots with full type information and asserts zero
// violations. This is the test CHAOS-4782 exists to add; the mutation
// proofs in the lane handoff reintroduce each historical construction in a
// throwaway commit and confirm THIS test turns red, then restore and
// confirm green again.
func TestNoServedTextIsDerivedFromQuestionFamily(t *testing.T) {
	root := familyGateLoadRoot(t)
	pkgs := familyGateLoadPackages(t, root, familyGateSweptImportPaths...)
	facts := familyGateResolveFacts(t, pkgs, familyGateSanctionedSymbols)
	violations := familyGateRun(t, pkgs, facts)

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

// familyGateFixture names one historical/novel construction fixture and
// what the sweep must say about it.
type familyGateFixture struct {
	name        string
	importPath  string
	description string
}

var familyGateFixtures = []familyGateFixture{
	{
		name:        "R5b_composite_literal_wrapped_family_key",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/p1_repro_composite_key",
		description: "codex round 1, P1, EXECUTED against this gate: a struct wrapping the family value used as a map key -- a NEW class, not a re-find",
	},
	{
		name:        "R1_raw_string_literal_after_conversion",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r1_raw_literal",
		description: "codex round 1: string(family) == \"subject_investigation\"",
	},
	{
		name:        "R2_named_string_underlying_map_value",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r2_named_alias",
		description: "codex round 2: map[QuestionFamily]phrase, phrase a named string type",
	},
	{
		name:        "R3_ordinal_indirection_into_text_table",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r3_ordinal_index",
		description: "codex round 3, and the reason this ticket exists: family -> vocabulary position -> []string index",
	},
	{
		name:        "R4_struct_field_relay_across_function_boundary",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r4_struct_field_relay",
		description: "not caught by the CHAOS-4735 heuristic (its own doc names this gap): ordinal computed in one function, stored on a struct field, consumed by a different function",
	},
}

// TestFamilyTextGateCatchesHistoricalConstructions loads each fixture as its
// OWN program (packages.Load call) -- go/types object identity is only
// comparable within one load, so facts are re-resolved per fixture rather
// than reused from the production-roots load. Each fixture is asserted to
// produce AT LEAST ONE violation; the fixture's own doc comment states
// which class it is, so a human reviewing a future failure here has the
// history without re-deriving it.
func TestFamilyTextGateCatchesHistoricalConstructions(t *testing.T) {
	root := familyGateLoadRoot(t)
	for _, fx := range familyGateFixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			patterns := []string{
				fx.importPath,
				"github.com/full-chaos/dev-health-acr/internal/contracts/v1/...",
			}
			pkgs := familyGateLoadPackages(t, root, patterns...)
			// Only the contracts/v1 purpose file, not the full
			// familyGateSanctionedSymbols: the other three sanctioned files
			// live in internal/contextfabric, which these standalone
			// fixtures do not import and this test does not load (paying
			// the full production-roots load cost per fixture would not
			// change which violation the fixture is meant to exercise).
			// Omitting the contracts/v1 file's own exemption entirely
			// would bury each fixture's real finding in noise: EVERY
			// discriminating constant's own declaration in
			// context_fabric_answer_plan.go would itself read as an
			// "unsanctioned reference".
			facts := familyGateResolveFacts(t, pkgs, familyGateContractsPurposeSymbols)
			violations := familyGateRun(t, pkgs, facts)
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
