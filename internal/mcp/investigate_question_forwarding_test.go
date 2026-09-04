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

// forwardedFields maps each hosted-request DESTINATION field to the `input`
// field forwarded into it -- `{"Question": "Question", "ParentResultID":
// "ParentResultID", ...}` -- by reading the keyed elements of a
// `…ContextFabricInvestigationRequest{…}` composite literal.
//
// IT RETURNS A MAPPING, NOT A SET, and that is codex round 1's finding. The
// set version recorded only which `input.Foo` selectors appeared SOMEWHERE in
// the literal, so transposing two mappings
// (`Question: input.ParentResultID, ParentResultID: input.Question`) left every
// source name present and the test green while both fields carried the wrong
// value. Measured, not argued: that exact transposition passed. A check that
// cannot tell a forward from a swap is not checking forwarding.
//
// The value is a SLICE because a translated destination legitimately draws on
// several input fields (`Options` is built from three), and collapsing them
// would make a dropped one invisible.
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
func forwardedFields(t *testing.T, filename string) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}
	found := map[string][]string{}
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
			dest, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			ast.Inspect(kv.Value, func(v ast.Node) bool {
				sel, ok := v.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "input" {
					// EVERY source under this destination is recorded, not
					// just the last. A translated destination legitimately
					// draws on several input fields (Options is built from
					// three), and collapsing them would make a dropped one
					// invisible.
					found[dest.Name] = append(found[dest.Name], sel.Sel.Name)
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

// sourcesFor reports the input fields forwarded into one destination key.
func sourcesFor(forwarded map[string][]string, dest string) []string { return forwarded[dest] }

// forwardsFrom reports whether dest is populated from the named input field.
func forwardsFrom(forwarded map[string][]string, dest, source string) bool {
	for _, got := range forwarded[dest] {
		if got == source {
			return true
		}
	}
	return false
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

	// translatedTo names, per field, the hosted destination key it is folded
	// into. These are forwarded but not straight through, so identity is the
	// wrong assertion; presence under the declared key is the right one.
	translatedTo := map[string]string{
		"EvidenceWindow":         "TimeContext",
		"AllowClarification":     "Options",
		"WindowConfirmationMode": "Options",
	}

	deliberatelyNotForwarded := map[string]string{
		"IncludeFullResult": "response-shaping for this surface only; never part of the investigation request",
		"Budget":            "translated into InvestigationOptions rather than copied",
		"Scope":             "translated into RequestedScope rather than copied",
	}

	forwarded := forwardedFields(t, "investigate_question.go")

	// Salted positive: a field known to be forwarded must be found, or the
	// parse/walk is broken and every result below is meaningless rather than
	// reassuring.
	if !forwardsFrom(forwarded, "Question", "Question") {
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
		// TRANSLATED fields reach the hosted request under a DIFFERENT
		// destination key, so identity cannot be asserted for them -- but
		// presence under the DECLARED key still can, which is strictly more
		// than the old set check proved. Naming the destination makes the
		// translation a reviewed decision rather than an unexplained
		// exception, exactly as deliberatelyNotForwarded does for the fields
		// that are not forwarded at all.
		if dest, translated := translatedTo[field.Name]; translated {
			if !forwardsFrom(forwarded, dest, field.Name) {
				t.Errorf("MCPInvestigateQuestionRequest.%s is declared as translated into hosted field %s, but no input.%s selector reaches that key (it draws on %v): the declaration and the code disagree", field.Name, dest, field.Name, sourcesFor(forwarded, dest))
			}
			continue
		}
		sources := sourcesFor(forwarded, field.Name)
		if len(sources) == 0 {
			t.Errorf("MCPInvestigateQuestionRequest.%s never reaches the hosted request literal in the MCP-to-engine conversion: a caller can send it, it can validate, and it is silently dropped before the engine ever sees it", field.Name)
			continue
		}
		// IDENTITY, not mere presence. A straight-through field must be read
		// from the SAME name it is written to; anything else is a
		// transposition, which reaches the engine carrying another field's
		// value and which no presence check can detect.
		if len(sources) != 1 || sources[0] != field.Name {
			t.Errorf("hosted request field %s is populated from %v, not input.%s: the value reaches the engine under the wrong name", field.Name, sources, field.Name)
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

	if !forwardsFrom(found, "Question", "Forwarded") {
		t.Errorf("the walk mapped Question <- %v, want [Forwarded]: it is under-matching or losing the destination key, so its negatives mean nothing", sourcesFor(found, "Question"))
	}
	for dest, srcs := range found {
		for _, src := range srcs {
			if src == "Mentioned" {
				t.Errorf("the walk reported input.Mentioned (as %s), which appears ONLY in a comment: a dropped mapping left mentioned in prose would pass the forwarding test, which is the false green this walk replaced", dest)
			}
			if src == "BareRead" {
				t.Errorf("the walk reported input.BareRead (as %s), which is read and discarded: proving a field is READ is not proving it is FORWARDED", dest)
			}
		}
	}
}

// TestForwardedFields_DetectsATransposition is the NEGATIVE CONTROL for the
// mapping half, added after codex round 1 measured the set version passing a
// straight swap of two fields.
//
// Without it, "the mapping is asserted" rests on reading the code rather than
// on the check having been shown able to fail for that reason.
func TestForwardedFields_DetectsATransposition(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := dir + "/transposed.go"
	source := `package sample

type ContextFabricInvestigationRequest struct{ Question, ParentResultID string }

type in struct{ Question, ParentResultID string }

func convert(input in) ContextFabricInvestigationRequest {
	return ContextFabricInvestigationRequest{
		Question:       input.ParentResultID,
		ParentResultID: input.Question,
	}
}
`
	if err := writeFile(path, source); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	found := forwardedFields(t, path)

	// Both source names are PRESENT -- which is exactly why a set could not
	// see the defect. The mapping is what distinguishes them.
	if !forwardsFrom(found, "Question", "ParentResultID") || !forwardsFrom(found, "ParentResultID", "Question") {
		t.Fatalf("the walk read the transposition as %#v, want Question<-ParentResultID and ParentResultID<-Question: it cannot detect a swap, which is the whole point of returning a mapping", found)
	}
	for dest, srcs := range found {
		for _, src := range srcs {
			if dest == src {
				t.Errorf("the walk reported %s <- %s as an identity mapping in a file where both fields are transposed", dest, src)
			}
		}
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}
