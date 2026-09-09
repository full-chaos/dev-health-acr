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
				literal, ok := kv.Value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					// A non-literal value is a constant reference — which is
					// what a registered producer looks like after this change.
					continue
				}
				value, unquoteErr := strconv.Unquote(literal.Value)
				if unquoteErr != nil {
					return true
				}
				rel, _ := filepath.Rel(root, path)
				found[value] = append(found[value], rel+":"+strconv.Itoa(fset.Position(literal.Pos()).Line))
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
	t.Logf("registered=%v raw production Source: literals=%v", hintsource.All(), found)
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
