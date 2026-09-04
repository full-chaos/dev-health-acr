package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// forwardedFields returns every field name read off an `input`-rooted selector
// in EXECUTABLE code in the given file -- `input.Foo`, and nothing else.
//
// An AST walk rather than a text search, and the difference is the whole
// point. The first version of this test grepped the source for `input.<Field>`,
// which a COMMENT satisfies just as well as a mapping does. A check that
// documentation can satisfy is not a check: a field could be dropped from the
// conversion and left mentioned in a doc comment, and the test would pass while
// the field silently never reached the engine -- the exact defect this file
// exists to prevent, reintroduced by the instrument meant to prevent it.
//
// go/parser discards comments unless asked for them, so a mention in prose
// contributes no selector and cannot be mistaken for a use.
func forwardedFields(t *testing.T, filename string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}
	found := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "input" {
			found[sel.Sel.Name] = true
		}
		return true
	})
	return found
}

// TestInvestigateQuestion_EveryRequestFieldIsForwarded closes a defect CLASS
// rather than the instance that prompted it.
//
// A field can sit on the tool schema, on MCPInvestigateQuestionRequest, and in
// validation, and still never reach the engine -- nothing structurally
// connects "the type has a field" to "the conversion copies it". This has
// happened twice: codex caught prior_candidate_receipts dropped this way in
// August, it was fixed per-instance, and parent_result_id was dropped the same
// way here. A per-instance fix is what let it recur.
//
// The contract-parity gate cannot catch it either: that compares published
// schemas to Go wire types, and a mapping function is neither.
//
// If a future field is genuinely surface-only and must NOT be forwarded, add
// it to deliberatelyNotForwarded with the reason. Making that an explicit,
// reviewed decision is the point.
func TestInvestigateQuestion_EveryRequestFieldIsForwarded(t *testing.T) {
	t.Parallel()

	deliberatelyNotForwarded := map[string]string{
		"IncludeFullResult": "response-shaping for this surface only; never part of the investigation request",
		"Budget":            "translated into InvestigationOptions rather than copied",
		"Scope":             "translated into RequestedScope rather than copied",
	}

	forwarded := forwardedFields(t, "investigate_question.go")

	// Salted positive: a field known to be forwarded must be found, or the
	// parse/walk is broken and every result below is meaningless rather than
	// reassuring.
	if !forwarded["Question"] {
		t.Fatal("the AST walk found no input.Question selector -- the parse or the walk is broken, so every result below proves nothing")
	}

	requestType := reflect.TypeOf(contractsv1.MCPInvestigateQuestionRequest{})
	checked := 0
	for i := 0; i < requestType.NumField(); i++ {
		field := requestType.Field(i)
		if reason, exempt := deliberatelyNotForwarded[field.Name]; exempt {
			t.Logf("exempt: %s (%s)", field.Name, reason)
			continue
		}
		checked++
		if !forwarded[field.Name] {
			t.Errorf("MCPInvestigateQuestionRequest.%s is never read in the MCP-to-engine conversion: a caller can send it, it can validate, and it is silently dropped before the engine ever sees it", field.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no fields were checked -- the reflection walk found nothing, so this test proves nothing")
	}
	t.Logf("checked %d forwarded fields via AST selectors", checked)
}

// TestForwardedFields_IgnoresCommentMentions is the NEGATIVE CONTROL for the
// check above, and it is what makes the AST walk worth doing.
//
// Without it, "the test passes" is equally consistent with "the AST walk is
// correct" and "the walk matches anything at all". This feeds it a file whose
// only mention of a field is inside a comment and requires the walk to report
// nothing -- the precise false positive the previous text-search version had.
func TestForwardedFields_IgnoresCommentMentions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/commented.go"
	source := `package sample

func convert(input struct{ Real, Mentioned string }) string {
	// input.Mentioned is deliberately only named here, in prose, exactly as a
	// stale doc comment would name a field whose mapping had been deleted.
	return input.Real
}
`
	if err := writeFile(path, source); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	found := forwardedFields(t, path)

	if !found["Real"] {
		t.Error("the walk missed input.Real, which IS used in executable code -- it is under-matching, so its negatives mean nothing")
	}
	if found["Mentioned"] {
		t.Error("the walk reported input.Mentioned, which appears ONLY in a comment: a dropped mapping left mentioned in prose would pass the forwarding test, which is the false green this walk replaced")
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}
