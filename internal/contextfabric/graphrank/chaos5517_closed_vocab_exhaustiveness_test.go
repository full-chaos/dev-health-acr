package graphrank

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// closedVocabSite is one literal-string assignment to a Go field this test
// knows carries a closed-vocabulary wire key.
type closedVocabSite struct {
	stage   string
	goField string
	wireKey string
	value   string
	loc     string
}

// closedVocabGoFields maps a ResolutionTraceEvent Go field name this walk
// checks to the wire key it is emitted under by tracer.go -- see this
// test's own SCOPE BOUNDARY doc comment for why only these two.
var closedVocabGoFields = map[string]string{
	"OfferPoolDisposition": "disposition",
	"Outcome":              "outcome",
}

// discoverClosedVocabLiteralSites walks every non-test .go file in this
// package's own directory (tracer.go excluded, the same convention
// discoverEmittedStages uses and for the same reason: it is the consumer,
// not a producer) and collects every (Stage, GoField, literal value) triple
// for a ResolutionTraceEvent{...} composite literal that assigns one of
// closedVocabGoFields a STRING LITERAL directly.
func discoverClosedVocabLiteralSites(t *testing.T) []closedVocabSite {
	t.Helper()
	fset := token.NewFileSet()
	var sites []closedVocabSite
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "tracer.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			// Matches an explicitly-typed ResolutionTraceEvent{...} literal,
			// OR one with an elided type (an element of an
			// already-typed outer slice/array composite) -- detected the
			// same way discoverEmittedStages' own sibling tool
			// (/tmp scratch enumsites.go, this ticket's own AST census)
			// does: this struct is the only one in-package with both a
			// "Stage" and a "RequestID" key-value field.
			matches := false
			if id, ok := lit.Type.(*ast.Ident); ok && id.Name == "ResolutionTraceEvent" {
				matches = true
			} else if lit.Type == nil {
				hasStage, hasRequestID := false, false
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok {
						if key.Name == "Stage" {
							hasStage = true
						}
						if key.Name == "RequestID" {
							hasRequestID = true
						}
					}
				}
				matches = hasStage && hasRequestID
			}
			if !matches {
				return true
			}
			var stage string
			var literals []struct{ key, value string }
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				basic, ok := kv.Value.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(basic.Value)
				if err != nil {
					continue
				}
				if key.Name == "Stage" {
					stage = value
				}
				if _, tracked := closedVocabGoFields[key.Name]; tracked {
					literals = append(literals, struct{ key, value string }{key.Name, value})
				}
			}
			if stage == "" {
				return true
			}
			for _, l := range literals {
				sites = append(sites, closedVocabSite{
					stage: stage, goField: l.key, wireKey: closedVocabGoFields[l.key], value: l.value,
					loc: fmt.Sprintf("%s:%d", name, fset.Position(lit.Pos()).Line),
				})
			}
			return true
		})
	}
	return sites
}

// eventForStage finds the eventspec.All event whose own "stage" field
// declares ClosedVocabulary == [stage].
func eventForStage(stage string) (eventspec.Event, bool) {
	for _, ev := range eventspec.All {
		for _, f := range ev.Fields {
			if f.Key == "stage" && containsString(f.ClosedVocabulary, stage) {
				return ev, true
			}
		}
	}
	return eventspec.Event{}, false
}

func fieldForKey(ev eventspec.Event, key string) (eventspec.Field, bool) {
	for _, f := range ev.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return eventspec.Field{}, false
}

// containsString is declared once for this package in resolve_test.go.

// TestEveryLiteralClosedVocabAssignmentIsDeclared is the r1 class ruling's
// own "generated exhaustiveness pin" (CHAOS-5517, team-lead's review of PR
// #509's r1 findings, 2026-09-11): findings 1-2 were the SAME class -- "a
// production construction site of a registered event was missed" -- and
// finding 1 specifically was a closed-vocabulary field (OfferPoolDisposition,
// "anchor_kind_withheld") assigned a literal value never checked against any
// declaration until this PR's own certify test ran for the first time.
//
// This closes the defect class the same way
// TestSlogResolutionTracer_CoversEveryEmittedStage (chaos3918) closes the
// "unhandled Stage" class: a go/ast walk over this package's own non-test
// source, not a hand-maintained list, so a FUTURE literal assignment is
// covered automatically. SCOPE BOUNDARY, disclosed the same way chaos3918
// discloses its own: this checks only fields assigned a STRING LITERAL
// directly inside a ResolutionTraceEvent{...} composite literal.
// OfferPoolDisposition and Outcome are covered because production code
// assigns them a raw literal at their DEFAULT/common-case sites (the ones a
// review actually found broken); a value threaded through a local variable
// or a named const (CommitGate, CommitBasis, PopulationBasis, and most
// OfferPoolDisposition/Outcome sites too) is invisible to a literal-only
// walk -- the SAME limitation chaos3918's own Stage walk already accepts,
// and the reason this is a static SUPPLEMENT to certify's own runtime
// validateFields check (which covers every field on every line an existing
// test's Certify/CertifyBoundedManyCount call actually reaches), not a
// replacement for it.
func TestEveryLiteralClosedVocabAssignmentIsDeclared(t *testing.T) {
	sites := discoverClosedVocabLiteralSites(t)
	if len(sites) == 0 {
		t.Fatal("discoverClosedVocabLiteralSites found zero sites -- the AST walk itself is broken (a false pass here would silently defeat this test's whole purpose)")
	}
	for _, s := range sites {
		s := s
		t.Run(s.stage+"/"+s.wireKey+"="+s.value, func(t *testing.T) {
			ev, ok := eventForStage(s.stage)
			if !ok {
				// Stage 3b: registration is incremental (PR3a/PR3b's own
				// explicit stacked split, PR body's "Events registered this
				// PR" table) -- a stage with no eventspec.All entry yet is a
				// deferred event, not a defect in THIS PR's own scope.
				// chaos3918's own stage-coverage test already asserts the
				// tracer itself has a case for every emitted stage
				// regardless of eventspec registration.
				t.Skipf("stage %q has no eventspec.All declaration yet -- not registered by this PR (PR3b scope)", s.stage)
			}
			field, ok := fieldForKey(ev, s.wireKey)
			if !ok || len(field.ClosedVocabulary) == 0 {
				t.Fatalf("eventspec.%s declares no closed-vocabulary field %q -- this site assigns one anyway (%s.%s = %q, %s)", ev.ID, s.wireKey, s.goField, s.wireKey, s.value, s.loc)
			}
			if !containsString(field.ClosedVocabulary, s.value) {
				t.Fatalf("%s: literal %q assigned to %s at %s is NOT in eventspec.%s's own declared closed vocabulary %v -- production emits a value its own specification does not recognize", s.wireKey, s.value, s.goField, s.loc, ev.ID, field.ClosedVocabulary)
			}
		})
	}
}
