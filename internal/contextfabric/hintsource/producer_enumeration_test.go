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
	// The local name this file gives the hintsource package, if it imports it
	// at all. r2 finding: the guard used to accept ANY selector spelled
	// `hintsource.X`, so a file importing a DIFFERENT package under that name
	// was trusted. The name is now resolved from the file's own imports.
	local := localNameForHintsource(file)

	// Variables in this file that HOLD a subject hint, so an assignment to
	// their Source field can be recognised. r2 finding: `h := SubjectHint{...}`
	// followed by `h.Source = x` set a source through no composite literal at
	// all, and the scan saw nothing. Tracking is per file and by name, which is
	// coarse — two functions may reuse a name — but it errs toward LOOKING at
	// an assignment rather than ignoring it, which is the safe direction here.
	hintVars := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, rhs := range assign.Rhs {
			composite, ok := rhs.(*ast.CompositeLit)
			if !ok || !isSubjectHintType(composite.Type) || index >= len(assign.Lhs) {
				continue
			}
			if ident, ok := assign.Lhs[index].(*ast.Ident); ok {
				hintVars[ident.Name] = true
			}
		}
		return true
	})
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, expr := range value.Values {
				composite, ok := expr.(*ast.CompositeLit)
				if ok && isSubjectHintType(composite.Type) && index < len(value.Names) {
					hintVars[value.Names[index].Name] = true
				}
			}
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			// `someHint.Source = <expr>` — no composite literal involved.
			for index, lhs := range typed.Lhs {
				selector, ok := lhs.(*ast.SelectorExpr)
				if !ok || selector.Sel == nil || selector.Sel.Name != "Source" {
					continue
				}
				ident, ok := selector.X.(*ast.Ident)
				if !ok || !hintVars[ident.Name] {
					continue
				}
				if index < len(typed.Rhs) {
					classifySourceExpr(fset, typed.Rhs[index], rel, local, found, unreadable)
				}
			}
		case *ast.CompositeLit:
			// A SLICE or MAP of hints elides the element type:
			// `[]SubjectHint{{Source: x}}` and
			// `map[string]SubjectHint{"a": {Source: x}}` both give the inner
			// literals a nil Type. Both were skipped entirely; the map form was
			// r2's finding after the slice form was r1's.
			var element ast.Expr
			switch container := typed.Type.(type) {
			case *ast.ArrayType:
				element = container.Elt
			case *ast.MapType:
				element = container.Value
			}
			if element != nil && isSubjectHintType(element) {
				for _, item := range typed.Elts {
					inner, ok := item.(*ast.CompositeLit)
					if !ok {
						// A map literal's element is `key: {…}`.
						if kv, ok := item.(*ast.KeyValueExpr); ok {
							inner, ok = kv.Value.(*ast.CompositeLit)
							if !ok {
								continue
							}
						} else {
							continue
						}
					}
					if inner.Type == nil {
						scanHintLiteral(fset, inner, rel, local, found, unreadable)
					}
				}
				return true
			}
			if isSubjectHintType(typed.Type) {
				scanHintLiteral(fset, typed, rel, local, found, unreadable)
			}
		}
		return true
	})
}

// localNameForHintsource returns the name this file uses for the hintsource
// package, or "" when it does not import it. Resolving the name from the
// imports is what stops a selector on an unrelated package that merely happens
// to be spelled `hintsource` from being trusted.
func localNameForHintsource(file *ast.File) string {
	const path = `"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"`
	for _, spec := range file.Imports {
		if spec.Path == nil || spec.Path.Value != path {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "hintsource"
	}
	return ""
}

// scanHintLiteral reads the Source: field of ONE subject-hint composite literal.
func scanHintLiteral(fset *token.FileSet, composite *ast.CompositeLit, rel, local string, found map[string][]string, unreadable *[]string) {
	for _, element := range composite.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Source" {
			continue
		}
		classifySourceExpr(fset, kv.Value, rel, local, found, unreadable)
	}
}

// classifySourceExpr is the ONE decision this scan makes about a source
// expression: read it, accept it as a reference into the registry, or REFUSE to
// classify it. Refusing is a finding; there is deliberately no fourth outcome,
// because "skip it" is the hole every round of this review has found.
func classifySourceExpr(fset *token.FileSet, expr ast.Expr, rel, local string, found map[string][]string, unreadable *[]string) {
	site := rel + ":" + strconv.Itoa(fset.Position(expr.Pos()).Line)
	if literal, ok := expr.(*ast.BasicLit); ok && literal.Kind == token.STRING {
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			*unreadable = append(*unreadable, site)
			return
		}
		found[value] = append(found[value], site)
		return
	}
	if local != "" && referencesPackage(expr, local) {
		return
	}
	*unreadable = append(*unreadable, site)
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

// r2 finding: a source set by ASSIGNMENT, through no composite literal at all.
func mintByAssignment() SubjectHint {
	h := SubjectHint{}
	h.Source = runtimeSource
	return h
}

// r2 finding: a MAP of hints elides the element type exactly as a slice does.
func mintInMap() map[string]SubjectHint {
	return map[string]SubjectHint{"a": {Source: derive()}}
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
	// SIX shapes it cannot read: a variable, a call, a field, a concatenation
	// of literals, a field ASSIGNMENT outside any composite literal, and a map
	// value with an elided element type. Every one is a way a producer could
	// mint an unregistered source, and every one must be REPORTED rather than
	// skipped. The last two were r2 findings; the scan saw neither.
	if len(unreadable) != 6 {
		t.Errorf("the scan reported %d unreadable sources, want 6 — a shape it silently skipped is a shape a "+
			"producer can mint anything through. unreadable=%v", len(unreadable), unreadable)
	}
	// The two package-constant shapes are accepted silently, so the ten values
	// split 2 read + 6 refused + 2 accepted.
	if len(found)+len(unreadable) != 8 {
		t.Errorf("the scan accounted for %d of the 10 Source: values; the two hintsource references should be "+
			"accepted silently and the rest split between found and unreadable. found=%v unreadable=%v",
			len(found)+len(unreadable), found, unreadable)
	}
}

// r2 FINDING, PERMANENT PIN: a selector is trusted because of what the FILE
// IMPORTS, never because of how the selector is spelled. A file importing an
// unrelated package under the name `hintsource` used to have its sources
// accepted unread.
func TestASelectorIsTrustedByTheImportNotBySpelling(t *testing.T) {
	const impostor = `package fixture

import hintsource "github.com/full-chaos/dev-health-acr/internal/storage"

func mint() SubjectHint {
	p := hintsource.Principal{OrgID: "not_a_registered_source"}
	return SubjectHint{Source: p.OrgID}
}

func mintDirect() SubjectHint {
	return SubjectHint{Source: string(hintsource.SomeConst)}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "impostor.go", impostor, 0)
	if err != nil {
		t.Fatalf("the fixture does not parse: %v", err)
	}
	if got := localNameForHintsource(file); got != "" {
		t.Fatalf("localNameForHintsource = %q for a file that does not import this package, want \"\"", got)
	}
	found := map[string][]string{}
	var unreadable []string
	scanHintSources(fset, file, "impostor.go", found, &unreadable)
	t.Logf("found=%v unreadable=%v", found, unreadable)
	if len(unreadable) != 2 {
		t.Errorf("the scan reported %d unreadable sources, want 2 — both are selectors on a package that is "+
			"NOT this one, merely spelled like it, and trusting the spelling accepts anything. unreadable=%v",
			len(unreadable), unreadable)
	}
	// AND THE CONTROL: the same expressions, in a file that really does import
	// this package, are accepted.
	const genuine = `package fixture

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"

func mint() SubjectHint {
	return SubjectHint{Source: string(hintsource.PriorSubjectReceipt)}
}
`
	genuineFile, err := parser.ParseFile(fset, "genuine.go", genuine, 0)
	if err != nil {
		t.Fatalf("the control fixture does not parse: %v", err)
	}
	if got := localNameForHintsource(genuineFile); got != "hintsource" {
		t.Fatalf("localNameForHintsource = %q for a file that DOES import this package, want \"hintsource\" — "+
			"without this the test above passes for the wrong reason", got)
	}
	controlFound := map[string][]string{}
	var controlUnreadable []string
	scanHintSources(fset, genuineFile, "genuine.go", controlFound, &controlUnreadable)
	if len(controlUnreadable) != 0 {
		t.Errorf("a genuine hintsource reference was refused: %v", controlUnreadable)
	}
}

// referencesPackage reports whether an expression mentions the package bound to
// local anywhere inside it — `hintsource.PriorSubjectReceipt`,
// `string(hintsource.X)`, or a concatenation containing one.
//
// local comes from the FILE'S OWN IMPORTS, never from the spelling of the
// selector. r2 finding: matching on the identifier text alone trusted any file
// that imported some other package under that name.
func referencesPackage(expr ast.Expr, local string) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == local {
			found = true
		}
		return true
	})
	return found
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
		if got := referencesPackage(expr, "hintsource"); got != testCase.wantThisModule {
			t.Errorf("%s (%s): referencesThisPackage = %v, want %v — a shape that neither reads as a literal "+
				"nor references this package must be REFUSED, not skipped", name, testCase.expr, got, testCase.wantThisModule)
		}
		if !readable && !testCase.wantThisModule && referencesPackage(expr, "hintsource") {
			t.Errorf("%s (%s): would be accepted with no way to check its value", name, testCase.expr)
		}
	}
}
