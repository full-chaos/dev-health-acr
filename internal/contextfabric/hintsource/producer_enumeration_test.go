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

	fset := token.NewFileSet()
	found := map[string][]string{}
	var unreadable []string
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
		rel, _ := filepath.Rel(root, path)
		scanHintSources(fset, file, rel, found, &unreadable)
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

// scanHintSources is THE SCAN, separated from the corpus it runs over.
//
// r1 finding, and then the battery's own finding on top of it. The rule "an
// expression I cannot read is a finding, not a skip" lived inline in the walk,
// and the only corpus the walk ever saw was production — which contains exactly
// two expression shapes, both package constants. A mutation restoring the old
// skip therefore changed nothing observable and SURVIVED: the rule was real,
// and no test exercised it. Pulling the scan out means the same code that reads
// production can be run over a fixture containing every shape a producer could
// write.
func scanHintSources(fset *token.FileSet, file *ast.File, rel string, found map[string][]string, unreadable *[]string) {
	ast.Inspect(file, func(node ast.Node) bool {
		composite, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		// A SLICE of hints elides the element type: `[]SubjectHint{{Source: x}}`
		// gives the inner literals a nil Type, and an earlier version of this
		// scan skipped every one of them. Production writes each hint
		// explicitly today, so nothing was missed — but "nothing was missed
		// today" is exactly the reasoning that let the previous hole through,
		// and a producer is free to write the slice form tomorrow. Found by the
		// fixture corpus below, which is the point of having one.
		if array, ok := composite.Type.(*ast.ArrayType); ok && isSubjectHintType(array.Elt) {
			for _, element := range composite.Elts {
				if inner, ok := element.(*ast.CompositeLit); ok && inner.Type == nil {
					scanHintLiteral(fset, inner, rel, found, unreadable)
				}
			}
			return true
		}
		if !isSubjectHintType(composite.Type) {
			return true
		}
		scanHintLiteral(fset, composite, rel, found, unreadable)
		return true
	})
}

// scanHintLiteral reads the Source: field of ONE subject-hint composite literal.
func scanHintLiteral(fset *token.FileSet, composite *ast.CompositeLit, rel string, found map[string][]string, unreadable *[]string) {
	for _, element := range composite.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Source" {
			continue
		}
		site := rel + ":" + strconv.Itoa(fset.Position(kv.Pos()).Line)
		literal, ok := kv.Value.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			// The ONLY acceptable non-literal is a reference into this package,
			// which is the shape a registered producer has. Anything else
			// cannot be checked, and a check that cannot answer must say so
			// rather than pass.
			if referencesThisPackage(kv.Value) {
				continue
			}
			*unreadable = append(*unreadable, site)
			continue
		}
		value, unquoteErr := strconv.Unquote(literal.Value)
		if unquoteErr != nil {
			continue
		}
		found[value] = append(found[value], site)
	}
}

// THE SCAN, RUN OVER A CORPUS THAT CONTAINS EVERY SHAPE — which production does
// not and should not. Without this, the scan's unreadable branch is code no
// test executes, and a mutation restoring the old skip survives the battery,
// which is exactly what happened.
func TestTheScanReportsEveryUnreadableSourceInAFixtureCorpus(t *testing.T) {
	const fixture = `package fixture

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"

var runtimeSource = "built_at_runtime"

func derive() string { return runtimeSource }

type cfg struct{ Source string }

func mint() []SubjectHint {
	c := cfg{}
	return []SubjectHint{
		{Source: "prior_subject_receipt"},
		{Source: "an_unregistered_literal"},
		{Source: string(hintsource.PriorSubjectReceipt)},
		{Source: hintsource.AnswerReuseAuthorizationRecheck},
		{Source: runtimeSource},
		{Source: derive()},
		{Source: c.Source},
		{Source: "prior" + "_subject_receipt"},
	}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", fixture, 0)
	if err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	found := map[string][]string{}
	var unreadable []string
	scanHintSources(fset, file, "fixture.go", found, &unreadable)
	t.Logf("found=%v unreadable=%v", found, unreadable)

	for _, value := range []string{"prior_subject_receipt", "an_unregistered_literal"} {
		if len(found[value]) != 1 {
			t.Errorf("the scan did not read the literal %q; found=%v", value, found)
		}
	}
	// FOUR shapes it cannot read: a variable, a call, a field, and a
	// concatenation of literals. Every one is a way a producer could mint an
	// unregistered source, and every one must be REPORTED rather than skipped.
	if len(unreadable) != 4 {
		t.Errorf("the scan reported %d unreadable sources, want 4 — a shape it silently skipped is a shape a "+
			"producer can mint anything through. unreadable=%v", len(unreadable), unreadable)
	}
	// The two package-constant shapes are accepted silently, so the eight
	// values split 2 read + 4 refused + 2 accepted.
	if len(found)+len(unreadable) != 6 {
		t.Errorf("the scan accounted for %d of the 8 Source: values; the two hintsource references should be "+
			"accepted silently and the rest split between found and unreadable. found=%v unreadable=%v",
			len(found)+len(unreadable), found, unreadable)
	}
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
