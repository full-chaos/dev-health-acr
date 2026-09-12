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

// explicitlyDeferredStages names every producer stage this walk may
// discover that carries NO eventspec.All registration yet, where the gap is
// a NAMED, disclosed deferral rather than an oversight -- keyed by stage,
// valued by the citation proving it. A bare Skip on an unregistered stage
// reads identically for "a documented, disclosed deferral" and "a brand-new
// producer nobody enumerated" -- exactly the silent-hole class this whole
// test exists to catch. Anything reaching eventForStage's !ok branch that is
// NOT on this list fails closed (t.Fatalf) instead.
//
// CHAOS-5636: evidence_census_commit (registered as
// eventspec.EvidenceCensusCommit) is REMOVED from this map, not merely left
// off it by omission -- it is now registered, so a genuine future gap on
// this stage must fail closed like any other. Its own four literal
// Outcome="..." assignments no longer appear in this walk's own discovered
// site list at all (not "deferred", simply invisible to it): they were
// folded into ONE shared call, emitEvidenceCensusCommit(resolve.go), whose
// own ResolutionTraceEvent{...} composite literal assigns Outcome from a
// function PARAMETER -- a value this walk's own disclosed scope boundary
// already excludes (resolveIdentLiteral resolves locals and package consts,
// never a parameter, the same class as "returned from a function call").
// The real protection for this stage now lives in the runtime certify
// suite (chaos5515_spec_sweep_test.go's own domain table, executed against
// production output), not this static supplement.
var explicitlyDeferredStages = map[string]string{}

// resolveIdentLiteral resolves an *ast.Ident used as a value inside a
// ResolutionTraceEvent{...} composite literal to the string it was last
// assigned -- checking the enclosing function's own local-variable scope
// first (locals, best-effort: a single straight-line, in-order walk of
// AssignStmt nodes; it does not model branches, loops or closures, and
// invalidates a name the moment it is reassigned to anything this walk
// cannot itself resolve, rather than keep a stale guess), then this
// package's own file set's package-level `const` declarations.
//
// A value threaded through a named constant is otherwise invisible to a
// literal-only walk: chaos5422_contest_set.go's own `contestSetDisposition`
// constant is assigned to OfferPoolDisposition at resolve.go's two
// admission-refusal sites AND to Outcome at mergeCensusAttestedSatisfier's
// own third evidence_census_commit site. This does not claim to resolve
// every indirection a Go program can construct -- a value returned from a
// function call, or threaded through a struct field, still falls through to
// the `default:` case below and stays undetected, the residual, disclosed
// scope boundary this test's own doc comment states.
func resolveIdentLiteral(name string, locals, consts map[string]string) (string, bool) {
	if v, ok := locals[name]; ok {
		return v, true
	}
	if v, ok := consts[name]; ok {
		return v, true
	}
	return "", false
}

// discoverClosedVocabLiteralSites walks every non-test .go file in this
// package's own directory (tracer.go excluded, the same convention
// discoverEmittedStages uses and for the same reason: it is the consumer,
// not a producer) and collects every (Stage, GoField, resolved value)
// triple for a ResolutionTraceEvent{...} composite literal that assigns one
// of closedVocabGoFields a STRING LITERAL, a local variable, or a named
// constant resolving to one (resolveIdentLiteral).
func discoverClosedVocabLiteralSites(t *testing.T) []closedVocabSite {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	type parsedFile struct {
		name string
		file *ast.File
	}
	var parsed []parsedFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "tracer.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed = append(parsed, parsedFile{name: name, file: file})
	}

	// consts: every package-level `const Name = "literal"` across this
	// package's own non-test, non-tracer sources -- see resolveIdentLiteral's
	// own doc comment for the concrete gap this closes.
	consts := map[string]string{}
	for _, pf := range parsed {
		for _, decl := range pf.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, nameIdent := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					basic, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || basic.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(basic.Value)
					if err != nil {
						continue
					}
					consts[nameIdent.Name] = value
				}
			}
		}
	}

	var sites []closedVocabSite
	visit := func(name string, lit *ast.CompositeLit, locals map[string]string) {
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
			return
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
			var value string
			var resolved bool
			switch v := kv.Value.(type) {
			case *ast.BasicLit:
				if v.Kind != token.STRING {
					continue
				}
				unquoted, err := strconv.Unquote(v.Value)
				if err != nil {
					continue
				}
				value, resolved = unquoted, true
			case *ast.Ident:
				value, resolved = resolveIdentLiteral(v.Name, locals, consts)
			default:
				continue
			}
			if !resolved {
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
			return
		}
		for _, l := range literals {
			sites = append(sites, closedVocabSite{
				stage: stage, goField: l.key, wireKey: closedVocabGoFields[l.key], value: l.value,
				loc: fmt.Sprintf("%s:%d", name, fset.Position(lit.Pos()).Line),
			})
		}
	}

	for _, pf := range parsed {
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				// A composite literal outside any function body has no
				// local scope to offer -- consts only. None of this
				// package's real sites are shaped this way today, but this
				// keeps the walk total over the file rather than silently
				// skipping a declaration shape it does not expect.
				ast.Inspect(decl, func(n ast.Node) bool {
					if lit, ok := n.(*ast.CompositeLit); ok {
						visit(pf.name, lit, nil)
					}
					return true
				})
				continue
			}
			// locals: a best-effort, in-source-order map of this function's
			// own string-literal-valued local variables, rebuilt fresh per
			// function (never carried across functions) and invalidated the
			// moment a name is reassigned to anything this walk cannot
			// itself resolve to a literal -- see resolveIdentLiteral's own
			// doc comment for the precise, disclosed limits.
			locals := map[string]string{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.AssignStmt:
					for i, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok {
							continue
						}
						if i < len(node.Rhs) {
							if basic, ok := node.Rhs[i].(*ast.BasicLit); ok && basic.Kind == token.STRING {
								if value, err := strconv.Unquote(basic.Value); err == nil {
									locals[id.Name] = value
									continue
								}
							}
						}
						delete(locals, id.Name)
					}
				case *ast.CompositeLit:
					visit(pf.name, node, locals)
				}
				return true
			})
		}
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
// discloses its own: this checks fields assigned a STRING LITERAL directly,
// a local variable, or a named constant, inside a ResolutionTraceEvent{...}
// composite literal (resolveIdentLiteral). OfferPoolDisposition and Outcome
// are covered because production code assigns them this way at their
// DEFAULT/common-case sites; a value returned from a function call or read
// off a struct field is still invisible to this walk -- the residual
// limitation this is a static SUPPLEMENT to certify's own runtime
// validateFields check (which covers every field on every line an existing
// test's Certify/CertifyBoundedManyCount call actually reaches), not a
// replacement for it.
//
// An unregistered stage this walk discovers FAILS CLOSED unless it is on
// explicitlyDeferredStages' own explicit, cited allowlist -- a bare Skip
// would read identically for "a named, disclosed deferral" and "a brand-new
// producer nobody enumerated at all".
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
				reason, deferred := explicitlyDeferredStages[s.stage]
				if !deferred {
					t.Fatalf("stage %q has no eventspec.All declaration and is not on explicitlyDeferredStages' own allowlist -- an unregistered stage this walk discovers must be either registered or named here as a disclosed deferral, never silently skipped (%s.%s = %q, %s)", s.stage, s.goField, s.wireKey, s.value, s.loc)
				}
				t.Skipf("stage %q has no eventspec.All declaration yet -- explicitly deferred: %s", s.stage, reason)
				return
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
