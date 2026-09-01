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

// CHAOS-4735: the question family may not be read to author user language,
// and the set of places that read it at all stays closed.
//
// WHY THIS IS AN AST SWEEP AND NOT A UNIT TEST. The defect this pins is a
// SHAPE, not a value: `narrowerQuestionFor` switched on plan.Family and
// returned one of five fixed English sentences, which the route served
// verbatim as error.details.narrower_question in the 413 body. A unit test
// asserting "narrowerQuestionFor no longer exists" dies the moment someone
// writes the same switch under a different name -- which is exactly the
// pressure the floor capstone puts on this code, because the floor's own
// narrower continuation sits on this mechanism and the natural build EXTENDS
// the switch. Acceptance criterion 1 asks for mechanical nonexistence; a
// sweep over the syntax is the only form of that claim which survives a
// rename.
//
// The two rulings this enforces are chris's, 2026-08-31 13:35 and 13:40:
// language is the model layer's job at BOTH boundaries, and vocabulary->
// sentence tables are banned outright rather than deprecated.
//
// It carries TWO assertions, because criteria 1 and 4 are two different
// claims about the same syntax and separating them makes a failure say which
// one broke:
//
//	A (criterion 4) -- the set of production files that name a DISCRIMINATING
//	  family value is exactly the sanctioned set. Checked in BOTH directions,
//	  so the sanctioned list cannot rot into a list of files that no longer
//	  mention a family.
//	B (criterion 1) -- no family-keyed switch or map anywhere, sanctioned
//	  files included, yields a string literal. This is the prose bar, and it
//	  is what criterion 5's mutation must trip.
//
// WHY "DISCRIMINATING", AND WHY THAT IS NOT A LOOPHOLE. The first run of this
// sweep flagged three files the ticket's own grep had not seen
// (chaos4632_question_family_consensus.go, chaos4636_answer_plan.go,
// chaos4636_plan_carry.go) because that grep listed only the five cohort
// families and the sweep derives all eight. Every one of those hits is
// QuestionFamilyUnclassified and nothing else: the resolver's refuse-to-guess
// initial value, the sanitizer's fallback, and two carry-time emptiness
// checks written literally as `Family == "" || Family == Unclassified`.
// Comparing against the member that MEANS "no family" is an emptiness test,
// not a read of which family this is, so it is not one of the four purposes
// and adding three files to the allowlist to accommodate it would dilute the
// one thing criterion 4 pins.
//
// The exclusion cannot be used to smuggle a read back in, for two reasons
// that are both mechanical rather than promised:
//
//  1. A file naming Unclassified AND any other family value is still counted,
//     because the hit test looks for a discriminating member. The sentinel
//     buys a file nothing.
//  2. Assertion B keeps the FULL vocabulary, so
//     `case QuestionFamilyUnclassified: return "..."` is still a violation --
//     and a `switch plan.Family` is caught by type regardless of which
//     constants its arms name.

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

func TestChaos4735NoFamilyKeyedStringTableInProduction(t *testing.T) {
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

			if fileNamesAFamilyConstant(file, discriminating) {
				filesWithHits[relative] = true
			}
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
			if value, ok := mapType.Value.(*ast.Ident); !ok || value.Name != "string" {
				return true
			}
			report(typed.Lbrace, "map from question family to string")
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

// productionGoFiles lists the non-test .go files directly under dir.
func productionGoFiles(t *testing.T, root, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		paths = append(paths, filepath.Join(root, filepath.FromSlash(dir), name))
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
