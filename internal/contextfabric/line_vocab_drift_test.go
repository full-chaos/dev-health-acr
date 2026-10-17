package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// constantsOfType returns the string values of every package-level constant
// declared with the given type name in the non-test files of this package.
func constantsOfType(t *testing.T, typeName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var values []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.CONST {
					continue
				}
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					ident, ok := vs.Type.(*ast.Ident)
					if !ok || ident.Name != typeName {
						continue
					}
					for _, value := range vs.Values {
						if lit, ok := value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							unquoted, _ := strconv.Unquote(lit.Value)
							values = append(values, unquoted)
						}
					}
				}
			}
		}
	}
	return values
}

// TestLineVocabulariesNameEveryConstantOfTheirType fails when a constant is
// added to a type whose members a log line declares as a closed set and the
// vocabulary function was not extended with it.
func TestLineVocabulariesNameEveryConstantOfTheirType(t *testing.T) {
	t.Parallel()
	for typeName, vocabulary := range map[string][]string{
		"WindowCarryOutcome":                WindowCarryOutcomeVocabulary(),
		"KindCarryOutcome":                  KindCarryOutcomeVocabulary(),
		"StatedWindowAxisOutcome":           StatedWindowAxisOutcomeVocabulary(),
		"WindowCanonicalizationOutcome":     WindowCanonicalizationOutcomeVocabulary(),
		"WindowBindReason":                  WindowBindReasonVocabulary(),
		"CarrySeedSource":                   CarrySeedSourceVocabulary(),
		"ConfirmedNeedLedgerWindowDecision": ConfirmedNeedLedgerWindowDecisionVocabulary(),
		"AnswerReuseBypassReason":           AnswerReuseBypassReasonVocabulary(),
		"SynthesisStatusOverrideReason":     SynthesisStatusOverrideReasonVocabulary(),
		"StoredAnswerabilityStep":           StoredAnswerabilityStepVocabulary(),
		"StoredAnswerabilityDetermination":  append(StoredAnswerabilityDeterminationVocabulary(), string(StoredAnswerabilityNotApplicable)),
	} {
		declared := constantsOfType(t, typeName)
		var members []string
		for _, value := range declared {
			if value != "" {
				members = append(members, value)
			}
		}
		sort.Strings(members)
		want := append([]string(nil), vocabulary...)
		sort.Strings(want)
		if strings.Join(members, "|") != strings.Join(want, "|") {
			t.Errorf("%s: constants %v, vocabulary %v", typeName, members, want)
		}
	}
}
