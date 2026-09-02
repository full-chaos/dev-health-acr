package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4735 — DEFENCE-IN-DEPTH HEURISTIC over known family-keyed language
// shapes. This is NOT a proof of nonexistence, and it must not be described as
// one.
//
// WHY THE CLAIM WAS DOWNGRADED, which is the useful part of this comment.
// Three consecutive adversarial review rounds each defeated an earlier version
// of this sweep with a genuinely NEW construction, none of them contrived:
//
//	R1  `string(plan.Family) == "subject_investigation"` — a comparison to a
//	    raw string literal. Named no constant, so a needle set built on
//	    constant names could not see it. Also: the walk was not recursive, so
//	    every subpackage was invisible.
//	R2  `type phrase string` + `map[QuestionFamily]phrase{...}` — a named type
//	    whose underlying type is string. Matching textual types by NAME could
//	    not resolve it.
//	R3  family -> ordinal via `QuestionFamilyVocabulary()`, then index a
//	    `[]string` table. Requires DATA FLOW to see; syntax cannot.
//
// R1 and R2 are closed below. R3 IS NOT, and saying so is the point: closing
// it needs `go/types` to resolve aliases plus data-flow tracking from a
// QuestionFamily value to a text-yielding index. That is a different kind of
// analysis and it is ticketed separately rather than bolted on here.
//
// The pattern across all three is one mistake made three ways: each version
// was an ALLOWLIST OF SHAPES THE AUTHOR HAD THOUGHT OF, defended as a
// universal claim. The claim is what was wrong, not the shapes. So the
// universal claim now lives where it has actually held — the WIRE tests in
// internal/api (closed `details` key set, and `error.message` invariance
// across families), which caught R2's and R3's constructions when this sweep
// did not.
//
// WHAT THIS CATCHES (the value it still has: these are the shapes a person
// reaches for first, and it catches them in review rather than at the wire):
//   - a `switch` on a family-typed tag, or on family constants, whose arms
//     return or assign a string literal;
//   - a map from the family type to anything that can hold text;
//   - a map to text keyed by a family WIRE VALUE written as a raw string;
//   - a family-typed expression compared to a non-empty string literal;
//   - any of the above anywhere under the four swept trees, recursively.
//
// WHAT THIS DOES NOT CATCH, stated so no one mistakes green for proof:
//   - ordinal indirection (R3) — family to index to a text table;
//   - anything reached through a function boundary or a struct field, where
//     the family and the text are in different scopes;
//   - text tables in packages outside the four swept roots;
//   - a family read that reaches text via any other closed token in between.
//
// Assertion A (the closed four-purpose read list) is unaffected by the
// downgrade — it is a claim about which FILES name a family value, which
// syntax can answer exactly.

// sanctionedFamilyReadSites is the CLOSED four-purpose read list from the
// stage-2 amendment (design §13.4.3), as file paths relative to the
// repository root.
//
// Design §13.9a row 8 recorded a FIFTH purpose -- user-facing language keyed
// on the family -- as though it were purpose 2, budget-profile selection.
// That reading was wrong: budget-profile selection is a registry lookup
// (planBudget, chaos4636_answer_plan.go), which reads the family VALUE and
// names no constant, so it does not appear here at all. The census row is
// corrected on the ticket and in cf-rulings.md rather than by editing the
// finalized design.
var sanctionedFamilyReadSites = []string{
	// Purpose 1: the precedence table that PRODUCES the family.
	"internal/contextfabric/chaos4632_question_family_precedence.go",
	// Purpose 2: the registry, where a family's declared columns live.
	"internal/contextfabric/chaos4632_question_family_registry.go",
	// Purpose 3: the package-local vocabulary aliases.
	"internal/contextfabric/chaos4632_question_family_vocab.go",
	// Purpose 4: the wire vocabulary declaration itself.
	"internal/contracts/v1/context_fabric_answer_plan.go",
}

// familySweepRoots are the production trees a served field can be authored
// in. internal/mcp is included even though the 413 body is an API concern:
// the MCP surface serves the same answers, and a sweep that only covers the
// plane where the defect happened to land is a sweep that finds it once.
var familySweepRoots = []string{
	"internal/contextfabric",
	"internal/api",
	"internal/contracts/v1",
	"internal/mcp",
}

func TestChaos4735KnownFamilyLanguageShapesAreAbsent(t *testing.T) {
	root := repositoryRootForFamilySweep(t)
	constants := familyValueConstantNames(t, root)

	// NON-VACUITY. A sweep whose needle set came back empty would pass over
	// every file in the repository and prove nothing -- the exact
	// green-but-vacuous shape the mutation-proof rule exists to catch. The
	// closed vocabulary's own count is the oracle.
	if len(constants) != 2*contractsv1.ContextFabricQuestionFamilyCount {
		t.Fatalf("family constant needle set = %d names, want %d (both the contracts and the package-local alias spelling of every one of the %d closed vocabulary members): %v",
			len(constants), 2*contractsv1.ContextFabricQuestionFamilyCount, contractsv1.ContextFabricQuestionFamilyCount, sortedKeys(constants))
	}

	// discriminating is the needle set for assertion A: every family value
	// EXCEPT the refuse-to-guess sentinel. Assertion B keeps `constants`.
	discriminating := map[string]bool{}
	for name := range constants {
		if name == "QuestionFamilyUnclassified" || name == "ContextFabricQuestionFamilyUnclassified" {
			continue
		}
		discriminating[name] = true
	}
	if len(discriminating) != len(constants)-2 {
		t.Fatalf("expected the closed vocabulary to carry exactly one unclassified sentinel in both spellings; discriminating=%d of %d", len(discriminating), len(constants))
	}

	filesWithHits := map[string]bool{}
	var proseViolations []string
	var dispatchViolations []string

	for _, dir := range familySweepRoots {
		for _, path := range productionGoFiles(t, root, dir) {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatalf("relativize %s: %v", path, err)
			}
			relative = filepath.ToSlash(relative)

			fileSet := token.NewFileSet()
			file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", relative, err)
			}

			dispatches := familyDispatchesWithoutConstants(fileSet, file, relative)
			if fileNamesAFamilyConstant(file, discriminating) || len(dispatches) > 0 {
				filesWithHits[relative] = true
			}
			dispatchViolations = append(dispatchViolations, dispatches...)
			proseViolations = append(proseViolations, familyKeyedStringLiterals(fileSet, file, relative, constants)...)
		}
	}

	// ---- Assertion A: the read list stays closed (criterion 4). ----
	sanctioned := map[string]bool{}
	for _, path := range sanctionedFamilyReadSites {
		sanctioned[path] = true
	}
	var unsanctioned, missing []string
	for path := range filesWithHits {
		if !sanctioned[path] {
			unsanctioned = append(unsanctioned, path)
		}
	}
	for path := range sanctioned {
		if !filesWithHits[path] {
			missing = append(missing, path)
		}
	}
	sort.Strings(unsanctioned)
	sort.Strings(missing)
	if len(unsanctioned) > 0 {
		t.Errorf("production files name a discriminating question family value outside the closed four-purpose read list (design §13.4.3):\n  %s\nEach is a NEW purpose for reading the family and needs a ruling before it ships, not a line in this list.",
			strings.Join(unsanctioned, "\n  "))
	}
	if len(missing) > 0 {
		t.Errorf("sanctioned family read sites no longer name any family constant:\n  %s\nEither the purpose moved and this list is stale, or the sweep's needle set is wrong. A stale allowlist silently widens the sweep's blind spot.",
			strings.Join(missing, "\n  "))
	}

	// ---- Assertion C: no family dispatch that dodges the vocabulary. ----
	// Reported separately from A and B because it is its own defect class and
	// a shared message would make a failure say the wrong thing about which
	// rule broke.
	if len(dispatchViolations) > 0 {
		sort.Strings(dispatchViolations)
		t.Errorf("production code dispatches on the question family WITHOUT naming a closed-vocabulary constant:\n  %s\nThis is the shape adversarial review used to defeat the first version of this sweep: no constant, no switch, invisible to a needle set built on constants. Read the family through the registry, or compare against the declared constants.",
			strings.Join(dispatchViolations, "\n  "))
	}

	// ---- Assertion B: no family-keyed prose anywhere (criterion 1). ----
	if len(proseViolations) > 0 {
		sort.Strings(proseViolations)
		t.Errorf("question family is mapped to a string literal in production code -- the banned vocabulary->sentence shape (chris rulings 2026-08-31 13:35/13:40):\n  %s\nThe engine does not author user language. A continuation is a closed-vocabulary token declared on the family registry, phrased by the model layer or not at all.",
			strings.Join(proseViolations, "\n  "))
	}
}

// familyValueConstantNames returns every spelling a family VALUE constant can
// have in this repository: the contracts declaration
// (ContextFabricQuestionFamilyX) and the package-local alias
// (QuestionFamilyX).
//
// Derived from the contracts source rather than hardcoded, so a family added
// to the closed vocabulary is swept automatically instead of quietly falling
// outside the needle set.
func familyValueConstantNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	const declaration = "internal/contracts/v1/context_fabric_answer_plan.go"
	path := filepath.Join(root, filepath.FromSlash(declaration))

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", declaration, err)
	}

	names := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		decl, ok := node.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// Only constants declared with the family type itself. The
			// source/count/vocabulary helpers share the name prefix and are
			// not family values.
			typeName, ok := value.Type.(*ast.Ident)
			if !ok || typeName.Name != "ContextFabricQuestionFamily" {
				continue
			}
			for _, name := range value.Names {
				names[name.Name] = true
				names[strings.TrimPrefix(name.Name, "ContextFabric")] = true
			}
		}
		return true
	})
	return names
}

// fileNamesAFamilyConstant reports whether the file mentions a family value
// constant, in either spelling and through either a bare identifier or a
// package selector.
func fileNamesAFamilyConstant(file *ast.File, constants map[string]bool) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		if found {
			return false
		}
		switch typed := node.(type) {
		case *ast.SelectorExpr:
			if constants[typed.Sel.Name] {
				found = true
			}
			// Do not descend: the X half is a package name.
			return false
		case *ast.Ident:
			if constants[typed.Name] {
				found = true
			}
		}
		return true
	})
	return found
}

// familyDispatchesWithoutConstants finds family dispatch that NAMES NO FAMILY
// CONSTANT, and is therefore invisible to a sweep built only on the constant
// needle set.
//
// FOUND BY ADVERSARIAL REVIEW (codex round 1, P1, EXECUTED, reproduced by this
// lane before it was fixed). The first version of this sweep detected
// `switch`es and map literals keyed on family CONSTANTS. The reviewer
// constructed a 413 branch that compared the family to a RAW STRING LITERAL
// and served a snake_case phrase:
//
//	if string(budgetRefusal.Family) == "subject_investigation" {
//	        details["narrower_hint"] = "ask_about_one_subject"
//	}
//
// No constant, no switch, no map. Both the sweep and the handler test passed.
// That is a direct false negative on the one claim this whole ticket rests on,
// and it is the more likely shape in practice, not a contrived one: it is what
// you write when you do not know the constants exist.
//
// Two dispatch forms are caught, and both are reported REGARDLESS of whether
// they yield text. Comparing or indexing by family without using the closed
// vocabulary is already a family read outside the sanctioned four purposes;
// whether this particular instance also authors a sentence is a separate
// question that assertion B answers.
//
//  1. A family-typed expression compared to a NON-EMPTY string literal. The
//     `string(...)` conversion is seen through, because that is how the
//     reviewer's construction was written and how anyone would write it.
//  2. A map literal with a TEXTUAL value type whose keys are family WIRE
//     VALUES written as raw strings -- `map[string]string{
//     "subject_investigation": "..."}`. This closes the raw-keyed table, which
//     rule 1 misses because such a table's literal never mentions the family
//     type and never names a constant.
//
// A rejected third rule, recorded because the rejection is the interesting
// part: "indexing by a family-typed expression" was tried and REMOVED. It
// fired on `outcome.SampleFamilies[resolved.Family]++`
// (chaos4632_question_family_consensus.go), which is a vote TALLY --
// map[QuestionFamily]int, an aggregation whose keys happen to be families, not
// a table that yields anything. Keying on the index alone cannot tell a tally
// from a lookup without type information, and a rule that fires on correct
// code gets switched off. Rule 2 keys on the map's VALUE TYPE instead, which
// is the property that actually distinguishes them.
//
// RESIDUAL LIMIT, stated rather than hidden: a slice indexed by a family
// ORDINAL would evade both rules. It requires deriving an ordinal from the
// family first, which needs a comparison (rule 1) or the vocabulary array, and
// no such code exists today -- but this sweep does not prove it never could.
//
// The EMPTY string literal is excluded, on exactly the reasoning that excludes
// the `unclassified` sentinel from assertion A: `Family == ""` is an emptiness
// test, not a read of which family this is. Production writes the two together
// (`Family == "" || Family == QuestionFamilyUnclassified`), which is the
// clearest evidence they are the same check.
func familyDispatchesWithoutConstants(fileSet *token.FileSet, file *ast.File, relative string) []string {
	var violations []string
	report := func(pos token.Pos, shape string) {
		violations = append(violations, relative+":"+itoa(fileSet.Position(pos).Line)+" ("+shape+")")
	}
	// The needle set is empty on purpose: these detectors key on the family
	// TYPE, which is the whole point -- they must fire where no constant is
	// named.
	noConstants := map[string]bool{}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.BinaryExpr:
			if typed.Op != token.EQL && typed.Op != token.NEQ {
				return true
			}
			for left, right := range map[ast.Expr]ast.Expr{typed.X: typed.Y, typed.Y: typed.X} {
				if !exprNamesFamily(left, noConstants, true) {
					continue
				}
				literal, ok := right.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				// `""` is an emptiness test, not a family read.
				if literal.Value == `""` {
					continue
				}
				report(typed.OpPos, "question family compared to the string literal "+literal.Value+" instead of a closed-vocabulary constant")
			}
		case *ast.CompositeLit:
			mapType, ok := typed.Type.(*ast.MapType)
			if !ok || !mayHoldText(mapType.Value) {
				return true
			}
			for _, element := range typed.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				literal, ok := pair.Key.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				if familyWireValueLiterals()[literal.Value] {
					report(literal.Pos(), "map to text keyed by the family wire value "+literal.Value+" written as a raw string")
				}
			}
		}
		return true
	})
	return violations
}

// familyWireValueLiterals is the eight closed-vocabulary values as they would
// appear as Go string literals, quotes included. Derived from the vocabulary
// so a new family is covered without editing this test.
func familyWireValueLiterals() map[string]bool {
	values := map[string]bool{}
	for _, family := range contractsv1.ContextFabricQuestionFamilyVocabulary() {
		values[`"`+string(family)+`"`] = true
	}
	return values
}

// mayHoldText reports whether a sentence could be stored in this type. It
// FAILS CLOSED: everything is assumed to hold text unless it is a builtin
// that provably cannot.
//
// FOUND BY ADVERSARIAL REVIEW (codex round 2, P1; argued by the reviewer under
// read-only, then EXECUTED by this lane before fixing). The first version
// matched textual types by NAME -- `string`, or an identifier ending String /
// Text / Message. The reviewer defeated it in one line:
//
//	type phrase string
//	var budgetRefusalMessage = map[contextfabric.QuestionFamily]phrase{ ... }
//
// `phrase` matches no name rule, so the table was invisible. The lane
// reproduced it: the construction compiles and the sweep printed `ok`.
//
// Matching textual types by name was the mistake, and it was the same mistake
// as round 1's in a different costume -- an ALLOWLIST OF SHAPES I had thought
// of, guarding against an author who thinks of a different one. The sweep
// parses without type information, so it cannot resolve `phrase` to its
// underlying type. What it CAN do is invert the default: a family-keyed map
// is suspect unless its value type is one of a small, closed set of builtins
// that cannot hold a sentence. Named types resolve to nothing here, so they
// are suspect, which is the correct direction for a gate.
//
// Cost of failing closed, measured rather than assumed: the only family-keyed
// maps in production today are `map[QuestionFamily]int` vote tallies
// (chaos4632_question_family_consensus.go, chaos4632_question_family_telemetry.go).
// `int` is in the non-textual set, so this costs zero false positives now, and
// a future `map[QuestionFamily]SomeStruct` SHOULD be looked at by a human --
// a struct can carry a string field.
func mayHoldText(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		// Selectors (pkg.Type), pointers, slices, maps, interfaces: not
		// provably non-textual from syntax alone.
		return true
	}
	switch ident.Name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128",
		"bool", "byte", "rune":
		return false
	}
	return true
}

// familyKeyedStringLiterals finds every place a family-keyed switch or map
// yields a string literal.
//
// It is deliberately narrow about WHERE the literal sits -- returned,
// assigned, or a map value -- because that is the defect's actual shape. A
// broader "any string literal inside a family switch" rule would fire on
// telemetry keys and error wrapping and would be turned off within a week,
// which is worse than a narrower rule that stays on.
func familyKeyedStringLiterals(fileSet *token.FileSet, file *ast.File, relative string, constants map[string]bool) []string {
	var violations []string
	report := func(pos token.Pos, shape string) {
		violations = append(violations, relative+":"+
			itoa(fileSet.Position(pos).Line)+" ("+shape+")")
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.SwitchStmt:
			if !switchIsFamilyKeyed(typed, constants) {
				return true
			}
			for _, statement := range typed.Body.List {
				clause, ok := statement.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, body := range clause.Body {
					if pos, found := yieldedStringLiteral(body); found {
						report(pos, "family-keyed switch arm yields a string literal")
					}
				}
			}
		case *ast.CompositeLit:
			mapType, ok := typed.Type.(*ast.MapType)
			if !ok || !exprNamesFamily(mapType.Key, constants, true) {
				return true
			}
			// mayHoldText, not `== "string"`: codex round 2 defeated the
			// exact-name check with `type phrase string`. Fails closed.
			if !mayHoldText(mapType.Value) {
				return true
			}
			report(typed.Lbrace, "map from question family to a type that can hold text")
		}
		return true
	})
	return violations
}

// switchIsFamilyKeyed reports whether the switch dispatches on a family: on
// the tag's TYPE (a Family field or a QuestionFamily-typed value) or on case
// expressions naming family constants.
func switchIsFamilyKeyed(statement *ast.SwitchStmt, constants map[string]bool) bool {
	if statement.Tag != nil && exprNamesFamily(statement.Tag, constants, true) {
		return true
	}
	for _, item := range statement.Body.List {
		clause, ok := item.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range clause.List {
			if exprNamesFamily(expr, constants, false) {
				return true
			}
		}
	}
	return false
}

// exprNamesFamily reports whether expr references a family constant, or --
// when byType is set -- a family-typed selector such as `plan.Family`.
//
// The byType arm is what makes the sweep survive the obvious evasion:
// `switch plan.Family { case a: ...; }` where the arms use locally-aliased
// constants the needle set does not know.
func exprNamesFamily(expr ast.Expr, constants map[string]bool, byType bool) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if found {
			return false
		}
		switch typed := node.(type) {
		case *ast.SelectorExpr:
			if constants[typed.Sel.Name] {
				found = true
			}
			if byType && (typed.Sel.Name == "Family" || typed.Sel.Name == "QuestionFamily" || typed.Sel.Name == "ContextFabricQuestionFamily") {
				found = true
			}
			return false
		case *ast.Ident:
			if constants[typed.Name] {
				found = true
			}
			if byType && (typed.Name == "QuestionFamily" || typed.Name == "ContextFabricQuestionFamily") {
				found = true
			}
		}
		return true
	})
	return found
}

// yieldedStringLiteral reports a string literal that a statement RETURNS or
// ASSIGNS -- the two ways a switch arm hands a caller a sentence.
func yieldedStringLiteral(statement ast.Stmt) (token.Pos, bool) {
	switch typed := statement.(type) {
	case *ast.ReturnStmt:
		for _, result := range typed.Results {
			if pos, ok := stringLiteralPos(result); ok {
				return pos, true
			}
		}
	case *ast.AssignStmt:
		for _, value := range typed.Rhs {
			if pos, ok := stringLiteralPos(value); ok {
				return pos, true
			}
		}
	}
	return token.NoPos, false
}

// stringLiteralPos unwraps the conversions a literal is usually dressed in
// (`Family("...")`, `string("...")`) before deciding.
func stringLiteralPos(expr ast.Expr) (token.Pos, bool) {
	switch typed := expr.(type) {
	case *ast.BasicLit:
		if typed.Kind == token.STRING {
			return typed.Pos(), true
		}
	case *ast.CallExpr:
		if len(typed.Args) == 1 {
			return stringLiteralPos(typed.Args[0])
		}
	case *ast.BinaryExpr:
		// Concatenation is still authorship.
		if pos, ok := stringLiteralPos(typed.X); ok {
			return pos, true
		}
		return stringLiteralPos(typed.Y)
	}
	return token.NoPos, false
}

// productionGoFiles lists the non-test .go files under dir, RECURSIVELY.
//
// Recursive since codex round 1: the first version read only the top level,
// so every subpackage was a blind spot -- and these trees have many
// (falkorgraph, graphrank, devhealthfacts, pgprojection, answerprojection,
// modelprovider, and more under internal/contextfabric alone). A sweep whose
// coverage claim stops at one directory level is a sweep that names the wrong
// scope in its own failure message.
func productionGoFiles(t *testing.T, root, dir string) []string {
	t.Helper()
	base := filepath.Join(root, filepath.FromSlash(dir))
	var paths []string
	err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// testdata is fixture material, not production code; vendored
			// trees are not ours to police.
			if entry.Name() == "testdata" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(paths) == 0 {
		t.Fatalf("no production Go files under %s -- the sweep would pass vacuously", dir)
	}
	return paths
}

// repositoryRootForFamilySweep walks up from the test's working directory to
// the module root, so the sweep addresses files by repository-relative path
// regardless of which package it is run from.
func repositoryRootForFamilySweep(t *testing.T) string {
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

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
