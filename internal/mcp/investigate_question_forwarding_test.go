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

// hostedRequestTypeName is the composite literal this check anchors on: the
// hosted request the MCP surface builds. Named once so the walk and its
// negative-control fixtures cannot drift apart.
const hostedRequestTypeName = "ContextFabricInvestigationRequest"

// forwardedFields returns every field name that an `input`-rooted selector
// FORWARDS INTO the hosted request literal in the given file -- that is, every
// `input.Foo` appearing as (or inside) the VALUE of a keyed element of a
// `…ContextFabricInvestigationRequest{…}` composite literal.
//
// TWO THINGS IT DELIBERATELY REFUSES TO COUNT, each because a weaker version
// of this walk counted them and was wrong:
//
//  1. A MENTION IN A COMMENT. The first version grepped the source for
//     `input.<Field>`, which a comment satisfies just as well as a mapping
//     does, so a field could be dropped from the conversion and left named in
//     a doc comment while the test stayed green -- the exact defect this file
//     exists to prevent, reintroduced by the instrument meant to prevent it.
//     go/parser discards comments unless asked for them, so prose contributes
//     no selector.
//
//  2. A BARE READ (codex r3, LOW). The second version collected every
//     `input.Foo` selector anywhere in the file, which proves the field is
//     READ, not that it is FORWARDED. `_ = input.ParentResultID`, a log line,
//     a length check, or a validation branch all satisfy "read" while the
//     hosted request never receives the value -- and "silently dropped before
//     the engine ever sees it" is precisely the failure being tested for. So
//     the walk now anchors on the hosted request literal and collects only
//     selectors that reach one of its keyed values.
//
// The value subtree is searched rather than matched exactly, so a wrapped or
// converted forward (`someTranslation(input.Foo)`, `&input.Foo`) still counts:
// the property under test is that the value reaches the literal, not that it
// arrives untouched.
func forwardedFields(t *testing.T, filename string) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}
	found := map[string]bool{}
	literals := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isHostedRequestLiteral(lit.Type) {
			return true
		}
		literals++
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				// A positional literal cannot be attributed to a field name by
				// this walk. Fail loudly rather than under-report: an
				// unattributable element is an unproven one.
				t.Fatalf("%s: %s literal has a positional element; this walk attributes forwards by field name and cannot read one", filename, hostedRequestTypeName)
			}
			ast.Inspect(kv.Value, func(v ast.Node) bool {
				sel, ok := v.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "input" {
					found[sel.Sel.Name] = true
				}
				return true
			})
		}
		return true
	})
	if literals == 0 {
		t.Fatalf("%s: found no %s composite literal -- the anchor this walk depends on is absent, so an empty result means the walk is broken, not that nothing is forwarded", filename, hostedRequestTypeName)
	}
	return found
}

// isHostedRequestLiteral matches both the qualified spelling this package uses
// (`contractsv1.ContextFabricInvestigationRequest`) and a bare one, so the walk
// survives an import-alias change without silently reporting zero.
func isHostedRequestLiteral(expr ast.Expr) bool {
	switch typ := expr.(type) {
	case *ast.SelectorExpr:
		return typ.Sel.Name == hostedRequestTypeName
	case *ast.Ident:
		return typ.Name == hostedRequestTypeName
	default:
		return false
	}
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
		t.Fatal("the AST walk found no input.Question forwarded into the hosted request literal -- the parse or the walk is broken, so every result below proves nothing")
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
			t.Errorf("MCPInvestigateQuestionRequest.%s never reaches the hosted request literal in the MCP-to-engine conversion: a caller can send it, it can validate, and it is silently dropped before the engine ever sees it", field.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no fields were checked -- the reflection walk found nothing, so this test proves nothing")
	}
	t.Logf("checked %d forwarded fields via AST selectors", checked)
}

// TestForwardedFields_RejectsMentionsAndBareReads is the NEGATIVE CONTROL for
// the check above, and it is what makes the AST walk worth doing.
//
// Without it, "the test passes" is equally consistent with "the AST walk is
// correct" and "the walk matches anything at all". This feeds it one file
// containing all three shapes at once and requires the walk to separate them:
//
//	Forwarded -- reaches a keyed value of the hosted literal.       MUST be found.
//	Mentioned -- appears only inside a comment.                     MUST NOT be found.
//	BareRead  -- read in executable code, never reaching the literal. MUST NOT be found.
//
// The third row is the one added after codex r3: the previous walk reported
// it, which meant the forwarding test could be satisfied by a field that was
// read and thrown away.
func TestForwardedFields_RejectsMentionsAndBareReads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/commented.go"
	source := `package sample

type ContextFabricInvestigationRequest struct{ Question string }

type in struct{ Forwarded, Mentioned, BareRead string }

func convert(input in) ContextFabricInvestigationRequest {
	// input.Mentioned is deliberately only named here, in prose, exactly as a
	// stale doc comment would name a field whose mapping had been deleted.
	_ = input.BareRead
	return ContextFabricInvestigationRequest{
		Question: input.Forwarded,
	}
}
`
	if err := writeFile(path, source); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	found := forwardedFields(t, path)

	if !found["Forwarded"] {
		t.Error("the walk missed input.Forwarded, which DOES populate the hosted request literal -- it is under-matching, so its negatives mean nothing")
	}
	if found["Mentioned"] {
		t.Error("the walk reported input.Mentioned, which appears ONLY in a comment: a dropped mapping left mentioned in prose would pass the forwarding test, which is the false green this walk replaced")
	}
	if found["BareRead"] {
		t.Error("the walk reported input.BareRead, which is read and discarded: proving a field is READ is not proving it is FORWARDED, and `_ = input.Field` satisfying this check is the codex r3 finding")
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}
