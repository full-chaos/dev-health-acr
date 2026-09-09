package hintsource_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE PRODUCER ENUMERATION. It asserts what the PRODUCERS ACTUALLY EMIT, by
// parsing them, rather than comparing the registry against a second list this
// test also wrote.
//
// The hole it closes is row 9 of the design's axes table and it is the only
// reason the enumeration is trustworthy: an engine producer added later that
// writes a fresh source literal and forgets to register it would be classified
// as CALLER AUTHORED — contest-exempt and short-circuit eligible — which is
// exactly the misclassification this change exists to remove, reintroduced one
// commit later and silently. A hand-written "expected members" list cannot
// catch that: it would have to be updated by the same person who forgot.
//
// It walks every production .go file under internal/ (tests excluded, because a
// test may legitimately construct a caller-authored hint with any string),
// finds every composite literal that sets a Source: field on a subject-hint
// type, and requires each constant string to be a registered member.
func TestEveryProductionHintSourceLiteralIsRegistered(t *testing.T) {
	root := repoRoot(t)
	registered := map[string]bool{}
	for _, source := range hintsource.All() {
		registered[string(source)] = true
	}
	if len(registered) == 0 {
		t.Fatal("the registry is empty; this test would pass vacuously")
	}

	found := map[string][]string{}
	// unreadable collects Source: values this test CANNOT evaluate statically.
	// They are findings, not omissions: see the non-literal branch below.
	var unreadable []string
	fset := token.NewFileSet()
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			composite, ok := node.(*ast.CompositeLit)
			if !ok || !isSubjectHintType(composite.Type) {
				return true
			}
			for _, element := range composite.Elts {
				kv, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Source" {
					continue
				}
				rel, _ := filepath.Rel(root, path)
				site := rel + ":" + strconv.Itoa(fset.Position(kv.Pos()).Line)
				literal, ok := kv.Value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					// NOT a literal. r1 finding, reproduced: an earlier version
					// of this test skipped these, and a producer that built its
					// source from a variable — `Source: runtimeSource` — passed
					// the whole enumeration while minting something unregistered.
					// Skipping was the hole, not the safe case.
					//
					// The only acceptable non-literal is a reference INTO this
					// package, which is what a registered producer looks like
					// after this change. Anything else cannot be read
					// statically, so this test cannot say whether it is
					// registered, and a check that cannot answer must say so
					// rather than pass.
					if referencesThisPackage(kv.Value) {
						continue
					}
					unreadable = append(unreadable, site)
					continue
				}
				value, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr != nil {
					return true
				}
				found[value] = append(found[value], site)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking production sources: %v", err)
	}

	for value, sites := range found {
		if !registered[value] {
			t.Errorf("production code mints the subject-hint source %q at %v, and it is NOT a registered member "+
				"of hintsource. An unregistered engine source is read as CALLER AUTHORED — contest-exempt and "+
				"short-circuit eligible — which is the misclassification this enumeration exists to remove. "+
				"Register it with its two attributes, or use the constant if it is already registered.",
				value, sites)
		}
	}
	for _, site := range unreadable {
		t.Errorf("production code sets a subject-hint Source at %s from an expression this test cannot read "+
			"statically, and which does not reference the hintsource package. The enumeration's whole claim is "+
			"that every engine-minted source is registered; a source built at runtime defeats that claim "+
			"silently. Use a hintsource constant, or register the value and name it here.", site)
	}
	t.Logf("registered=%v raw production Source: literals=%v unreadable=%v", hintsource.All(), found, unreadable)
}

// referencesThisPackage reports whether an expression mentions the hintsource
// package anywhere inside it — `hintsource.PriorSubjectReceipt`,
// `string(hintsource.X)`, or a concatenation containing one. That is the shape
// a registered producer has, and it is the ONLY non-literal shape this test
// accepts.
func referencesThisPackage(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "hintsource" {
			found = true
		}
		return true
	})
	return found
}

// AND THE CONTROL, because the test above passes trivially when the walk finds
// nothing: the producers must be reachable and must be using the constants.
func TestBothRegisteredSourcesHaveAProductionProducer(t *testing.T) {
	root := repoRoot(t)
	for _, source := range hintsource.All() {
		name := constantName(t, source)
		if !grepProduction(t, root, "hintsource."+name) {
			t.Errorf("no production file references hintsource.%s — a registry member with no producer is a "+
				"member nothing emits, and the enumeration then describes nothing", name)
		}
	}
}

func constantName(t *testing.T, source hintsource.Source) string {
	t.Helper()
	switch source {
	case hintsource.PriorSubjectReceipt:
		return "PriorSubjectReceipt"
	case hintsource.AnswerReuseAuthorizationRecheck:
		return "AnswerReuseAuthorizationRecheck"
	}
	t.Fatalf("registry member %q has no constant name in this test; a new member was added without extending "+
		"the producer control, so it is unmeasured", source)
	return ""
}

func grepProduction(t *testing.T, root, needle string) bool {
	t.Helper()
	hit := false
	err := filepath.Walk(filepath.Join(root, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || hit {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(filepath.ToSlash(path), "/contextfabric/hintsource/") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), needle) {
			hit = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking production sources: %v", err)
	}
	return hit
}

func isSubjectHintType(expr ast.Expr) bool {
	switch typed := expr.(type) {
	case *ast.Ident:
		return strings.HasSuffix(typed.Name, "SubjectHint")
	case *ast.SelectorExpr:
		return strings.HasSuffix(typed.Sel.Name, "SubjectHint")
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

// ROW 10 OF THE AXES TABLE: a source string that cannot exist is rejected by
// the CONTRACT, before any classification runs, and the enumeration therefore
// never has to have an opinion about it.
//
// Pinned here rather than assumed because the enumeration's "unenumerated means
// caller-authored" rule would otherwise be reasoning about the empty string and
// about 65-byte strings as if they were real populations. They are not: v1
// validation refuses them at the door.
func TestTheContractRejectsSourceStringsTheEnumerationNeverSees(t *testing.T) {
	valid := contractsv1.ContextFabricSubjectHint{
		Kind: contractsv1.ContextFabricSubjectProject, ID: "project_ask_dev", Label: "Ask Dev",
		Source: string(hintsource.PriorSubjectReceipt),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("the control failed: a registered source must VALIDATE, or every rejection below proves "+
			"nothing about the source field. err = %v", err)
	}
	for name, source := range map[string]string{
		"empty":              "",
		"whitespace only":    "   ",
		"untrimmed":          " prior_subject_receipt",
		"longer than bounds": strings.Repeat("s", 65),
	} {
		hint := valid
		hint.Source = source
		if err := hint.Validate(); err == nil {
			t.Errorf("%s source %q VALIDATED; the enumeration's caller-authored default would then have to "+
				"describe a population the contract was supposed to have refused", name, source)
		}
	}
}

// THE WALK'S OWN CLASSIFIER, tested directly, because the walk over production
// sources can only ever see the shapes production currently contains — today,
// two constant references and nothing else. A rule that is never exercised by
// the corpus it runs on is a rule nobody has checked.
//
// This is the r1 finding stated as a property: for each expression shape a
// producer could write, the walk must either read the value or REFUSE to
// classify it. Silently skipping is what let a runtime-built unregistered
// source through.
func TestTheWalkRefusesEverySourceShapeItCannotRead(t *testing.T) {
	for name, testCase := range map[string]struct {
		expr           string
		wantReadable   bool
		wantThisModule bool
	}{
		"a registered literal":          {expr: `"prior_subject_receipt"`, wantReadable: true},
		"an unregistered literal":       {expr: `"something_else"`, wantReadable: true},
		"a package constant":            {expr: `hintsource.PriorSubjectReceipt`, wantThisModule: true},
		"a converted package constant":  {expr: `string(hintsource.PriorSubjectReceipt)`, wantThisModule: true},
		"a concatenation including one": {expr: `string(hintsource.PriorSubjectReceipt) + "_v2"`, wantThisModule: true},
		"a bare variable":               {expr: `runtimeSource`},
		"a function call":               {expr: `deriveSource()`},
		"a struct field":                {expr: `cfg.Source`},
		"a concatenation of literals":   {expr: `"prior" + "_subject_receipt"`},
	} {
		expr, err := parser.ParseExpr(testCase.expr)
		if err != nil {
			t.Fatalf("%s: the fixture does not parse: %v", name, err)
		}
		literal, isLiteral := expr.(*ast.BasicLit)
		readable := isLiteral && literal.Kind == token.STRING
		if readable != testCase.wantReadable {
			t.Errorf("%s (%s): readable as a literal = %v, want %v", name, testCase.expr, readable, testCase.wantReadable)
		}
		if got := referencesThisPackage(expr); got != testCase.wantThisModule {
			t.Errorf("%s (%s): referencesThisPackage = %v, want %v — a shape that neither reads as a literal "+
				"nor references this package must be REFUSED, not skipped", name, testCase.expr, got, testCase.wantThisModule)
		}
		if !readable && !testCase.wantThisModule && referencesThisPackage(expr) {
			t.Errorf("%s (%s): would be accepted with no way to check its value", name, testCase.expr)
		}
	}
}
