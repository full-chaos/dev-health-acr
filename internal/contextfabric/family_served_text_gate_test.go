package contextfabric

// The family-to-served-text gate: no question-family value may become
// text that reaches the wire.
//
// The ANALYSIS lives in family_taint_ssa_test.go -- value-flow taint over
// golang.org/x/tools/go/ssa. THIS file is the gate around it: what counts
// as a sanctioned reader, what gets loaded, and the two assertions.
//
// HISTORY, because it is the argument for the shape of this thing. The
// first version of this gate walked go/ast. Four adversarial review
// rounds each defeated it with a genuinely new SYNTACTIC site the walk
// did not reach: a comparison to a raw string literal after a conversion,
// then a composite literal wrapping the family as a map key, then a
// tagless switch whose comparison hid inside strings.EqualFold, then an
// ordinary call -- fmt.Sprintf -- whose RESULT the walk declined to
// taint. Each fix closed one more shape and the next round found the
// next. That is not four bugs, it is one: a syntax walker cannot be
// closed under an arbitrary call boundary, because a call may do anything
// with its argument and hand back something derived from it, and "how"
// has no bounded enumeration. Closures, method values, channel sends and
// interface dispatch were the same class, unprobed rather than closed.
// The gate was re-shaped onto the IR rather than patched a fifth time.
//
// TWO ASSERTIONS, and they are not the same claim.
//
//   - TestNoServedTextIsDerivedFromQuestionFamily is the gate. It fails
//     on the ENFORCED tier only: family-derived text stored into a field
//     of a type that reaches the encoding/json boundary, or handed to
//     that boundary directly. That is the property CHAOS-4782 exists to
//     hold. Everything else the analysis sees -- derived text returned
//     from a helper, kept in an internal struct, put on a channel -- is
//     logged with its full provenance and does not fail. Those are
//     intermediates, and if one ever reaches the wire the store that puts
//     it there is itself an enforced finding, at a line a reader can act
//     on. A gate that fails on its own intermediates gets switched off,
//     and a switched-off gate holds nothing.
//
//   - TestFamilyTextGateCatchesHistoricalConstructions is the acceptance
//     corpus: eleven fixtures, each a construction that defeated some
//     earlier version of this gate or that probes a weak joint of the
//     current one. Each must produce at least one finding, and the table
//     records WHICH TIER it must land in.
//     A standalone fixture package serves nothing of its own, so its
//     Result/Details struct is a documented stand-in for a served field;
//     what the corpus proves is that the ANALYSIS REACHES the
//     construction, which is precisely what four review rounds attacked.
//
// WHAT GREEN DOES NOT MEAN. The limits of the analysis are stated in
// family_taint_ssa_test.go's header -- flow-insensitive memory, CHA for
// dynamic dispatch, no reflection, implicit flow at full strength only
// under a DIRECTLY family-derived branch and narrowed to non-empty text
// constants otherwise, and a plain string parameter that is family-derived at
// only some call sites reported at the call site rather than inside the
// callee. Green means no enforced finding under those limits. It is not a
// nonexistence proof, and this comment exists so nobody reads it as one.
//
// The earlier heuristic language sweep still coexists with this gate; retiring
// it is tracked as residue, not done here.

import (
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

// familyGateFacts is everything the analysis resolves ONCE from a loaded
// program before walking it: the family type itself, its constants (both
// spellings, matching chaos4735's non-vacuity discipline), the closed
// vocabulary's wire-value string literals (for the raw-keyed-table rule,
// which needs no type information at all), and the sanctioned object set.
type familyGateFacts struct {
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
	findings := familySSAAnalyze(t, pkgs, facts, true)

	// The ENFORCED tier is the claim: no family-derived text is stored
	// into a field of any type that reaches the serialization boundary,
	// and none is handed to that boundary directly. Everything else the
	// analysis sees is reported below with its provenance and does not
	// fail -- see familyTaintFinding.enforced for why a gate that fails on
	// its own intermediates is a gate that gets switched off.
	var enforced, reported []familyTaintFinding
	for _, f := range findings {
		if f.enforced {
			enforced = append(enforced, f)
			continue
		}
		reported = append(reported, f)
	}

	// False-positive guard (CHAOS-4782 acceptance): the vote-tally maps
	// keyed by the family type with an int value must never FAIL the
	// gate. Asserted against the enforced tier by name so a regression
	// here fails on its own message rather than merging into "some
	// violation somewhere". Both files DO appear in the reported tier --
	// they legitimately turn the family into text for an internal sort
	// key and for telemetry attributes -- and that is the distinction the
	// enforced/reported split exists to make.
	for _, f := range enforced {
		if strings.Contains(f.pos.Filename, "chaos4632_question_family_consensus.go") ||
			strings.Contains(f.pos.Filename, "chaos4632_question_family_telemetry.go") {
			t.Errorf("false positive on a known-clean vote-tally file: %s", f)
		}
	}

	for _, f := range reported {
		t.Logf("reported (not enforced): %s", f)
	}

	if len(enforced) > 0 {
		var lines []string
		for _, f := range enforced {
			lines = append(lines, f.String())
		}
		t.Errorf("SSA value-flow family taint gate found %d sink(s) that reach the wire:\n  %s",
			len(enforced), strings.Join(lines, "\n  "))
	}
}

// familyGateFixture names one historical/novel construction fixture and
// what the sweep must say about it.
type familyGateFixture struct {
	name        string
	importPath  string
	description string
	// wantEnforced states which TIER this construction must land in when
	// the fixture is loaded standalone, so the corpus records the
	// distinction rather than only that "something was found".
	//
	// Only a construction that crosses a boundary INSIDE the fixture --
	// R11 writes its prose straight to an http.ResponseWriter -- is
	// enforced standalone. The rest store into a struct that stands in
	// for a served field but is not itself reachable from an encoder in a
	// package that serves nothing, so they land in the reported tier
	// here and would be enforced in production, where the equivalent
	// field IS reachable.
	wantEnforced bool
}

var familyGateFixtures = []familyGateFixture{
	{
		name:        "R7_ordinary_call_result_laundering",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r7_ordinary_call_sprintf",
		description: "codex round 3, P1, EXECUTED, re-executed by the lane: fmt.Sprintf(\"...%s...\", family) assigned to a served field. the acceptance case that ended the syntax-walker approach and the reason this gate is an SSA value-flow analysis: a walker is never closed under an arbitrary call boundary. Caught here by the uniform per-call-site rule, with no knowledge of fmt.Sprintf.",
	},
	{
		name:         "R12a_io_copy_reader_boundary_bypass",
		importPath:   "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r12_io_copy_reader",
		description:  "codex round 2 P1: prose wrapped in a strings.Reader and io.Copy'd to the response -- the value at the boundary is not text-typed",
		wantEnforced: true,
	},
	{
		name:         "R12b_bufio_write_string_boundary_bypass",
		importPath:   "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r12_bufio_write_string",
		description:  "codex round 2 P1: prose written via bufio.Writer.WriteString -- a writer method not named exactly Write",
		wantEnforced: true,
	},
	{
		name:         "R12e_concrete_reader_at_egress",
		importPath:   "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r12_concrete_reader_egress",
		description:  "pins the no-text-type-test-at-egress rule: the payload reaches the boundary as a CONCRETE *strings.Reader, so no structural text test can see it -- R12a does not pin this, because io.Copy's parameter is an interface and the text predicate accepts every interface",
		wantEnforced: true,
	},
	{
		name:         "R12c_template_execute_writer_param",
		importPath:   "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r12_template_execute",
		description:  "byte egress where the writer is the first PARAMETER of a method on a non-writer receiver (html/template Execute)",
		wantEnforced: true,
	},
	{
		name:         "R12d_custom_marshaller_results",
		importPath:   "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r12_custom_marshaller",
		description:  "encoder family from the other side: a MarshalJSON whose bytes reach the wire without this package ever calling it, so its RESULTS are the boundary",
		wantEnforced: true,
	},
	{
		name:        "R10_control_selected_nonconstant_text",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r10_control_nonconstant",
		description: "codex round 1 P1: prose selected by a family test but produced by a no-argument helper, so the implicit-flow rule's non-empty-text-CONSTANT restriction does not see it",
	},
	{
		name:         "R11_direct_bytes_write",
		importPath:   "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r11_direct_bytes_write",
		description:  "codex round 1 P1: family-derived prose written straight to http.ResponseWriter.Write, bypassing the encoding/json boundary the served set is derived from",
		wantEnforced: true,
	},
	{
		name:        "R8_global_map_relay_between_functions",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r8_global_map_relay",
		description: "authored by lane-4782-ssa against the SSA model's own weakest joint (not a review finding): family-derived text written into a package-level map in one function and read back in another, with no value, parameter, field or return connecting them -- pins the flow-insensitive per-origin memory model",
	},
	{
		name:        "R9_interface_method_dispatch",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/r9_interface_dispatch",
		description: "authored by lane-4782-ssa against the SSA model's own weakest joint (not a review finding): the family stored on a struct field, the text produced by a method, the method reached through an interface -- the class the handoff called un-probed rather than closed, and the only rule that depends on the CHA call graph",
	},
	{
		name:        "R6_tagless_switch_comparison_hidden_in_call",
		importPath:  "github.com/full-chaos/dev-health-acr/internal/contextfabric/testdata/family_served_text_gate/p2_repro_tagless_switch",
		description: "codex round 2, P1, EXECUTED against this gate: tagless switch, the comparison against family hidden inside strings.EqualFold -- a NEW class, not a re-find",
	},
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
			findings := familySSAAnalyze(t, pkgs, facts, false)
			if len(findings) == 0 {
				t.Fatalf("RED-FIRST FAILURE: fixture %s (%s) produced ZERO violations -- the gate does not catch this historical construction", fx.name, fx.description)
			}
			var enforced int
			for _, f := range findings {
				if f.enforced {
					enforced++
				}
			}
			if fx.wantEnforced && enforced == 0 {
				t.Errorf("fixture %s crosses a serialization boundary inside the fixture, so it must land in the ENFORCED tier; got %d finding(s), none enforced", fx.name, len(findings))
			}
			if !fx.wantEnforced && enforced > 0 {
				t.Errorf("fixture %s serves nothing of its own, so every finding should be REPORTED; got %d enforced -- the served-type derivation reached further than expected and the tier split needs re-reading", fx.name, enforced)
			}
			t.Logf("%s: %d finding(s):", fx.name, len(findings))
			for _, f := range findings {
				t.Logf("  %s", f)
			}
		})
	}
}
