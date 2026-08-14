package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// auditedLimitationWrites are the raw writes to a limitations-destined value
// that are NOT the bounded appender's output, each with the reason it is
// safe.
//
// Two-sided, like the sidecar's body-read audit: an unlisted raw write
// fails, and a listed entry matching nothing fails too. Keyed by enclosing
// function plus what the write is fed from, so ordinary edits above a site
// do not churn the list.
//
// This list is meant to stay SHORT. Every entry is a place the cap is not
// mechanically enforced, i.e. a place round-17 finding 1 could recur.
var auditedLimitationWrites = map[string]string{
	"terminalResult#composite literal": "the SEED, not an addition: a one-element list holding the single fixed terminal disclosure resolveTerminalStatus chose. Every list has to start somewhere, and everything added after it goes through the bounded appender, which also normalizes an already-over-cap input -- so the seed cannot be the write that overflows the contract",
	"Synthesize#cloneSlice":            "an INTERMEDIATE, not a list that reaches a consumer: this is the model's own draft list entering the synthesized result, and Investigate then passes result.Limitations through appendTemporalLimitations UNCONDITIONALLY -- it is called on every axis, current included, and appendBoundedLimitations normalizes an already-over-cap input -- before Validate runs",
}

// boundedLimitationPrimitive owns the cap. It is the only function allowed
// to append to a limitations list without itself going through something
// else, and it is exempt from the wrapper body check below for that reason.
const boundedLimitationPrimitive = "appendBoundedLimitations"

// boundedLimitationWrappers are the narrower names the bounded path travels
// under. Membership is NOT taken on trust: verifyBoundedWrappers proves each
// body is nothing but one call to the primitive (codex round-5 F1c). A
// wrapper that grew a raw append after its bounded call would otherwise
// launder that append behind a whitelisted name.
var boundedLimitationWrappers = map[string]bool{
	"appendTemporalLimitations": true,
	"withRetrievalDegradation":  true,
}

func isBoundedLimitationSource(name string) bool {
	return name == boundedLimitationPrimitive || boundedLimitationWrappers[name]
}

// TestEveryLimitationAppendIsBounded closes the CLASS behind round-17
// finding 1 rather than the site.
//
// The cap was handled where the degradation disclosure was appended and
// nowhere else, so CHAOS-3781's historical disclosures were appended on top
// of a full list and the whole investigation died at validation. That is not
// a bug in either appender; it is what happens when "the cap" lives at a
// call site instead of in one function every append has to go through.
//
// The shape it pins: inside this package, a list that reaches a result's
// Limitations must have been produced by the bounded appender, and every
// write to any local that flows there must be the appender's output too.
//
// It is deliberately CONSERVATIVE (codex round-5 F1). An earlier version
// tried to be exact -- it followed a local to its LEXICALLY latest write and
// judged only that one. Three ways that was wrong: lexical order is not
// execution order, so a raw append inside an if-branch was excused by a
// bounded write below it; the search ran over the whole file rather than the
// enclosing function, so a same-named local in another function could answer
// for this one; and wrapper names were trusted without reading their bodies.
//
// So it no longer decides which write "wins". ANY raw write to a
// limitations-destined local is a violation, wherever it sits in the
// function. Over-approximation is the point: a legitimate pattern flagged
// here becomes a visible, two-sided audit entry with a stated reason, which
// is the mechanism working rather than failing.
func TestEveryLimitationAppendIsBounded(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	var (
		writes       []limitationWrite
		sawPrimitive bool
		functions    = map[string]*ast.FuncDecl{}
	)
	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				functions[function.Name.Name] = function
				if callsFunction(function.Body, boundedLimitationPrimitive) {
					sawPrimitive = true
				}
				writes = append(writes, limitationWritesIn(fileSet, function)...)
			}
		}
	}

	if !sawPrimitive {
		t.Fatalf("found no call to %s at all; the walker is not reaching the engine and would pass over any unbounded append", boundedLimitationPrimitive)
	}
	if len(writes) == 0 {
		t.Fatal("found no Limitations write at all; the walker is not reaching the composition code")
	}

	verifyBoundedWrappers(t, functions)

	matched := map[string]bool{}
	for _, w := range writes {
		if isBoundedLimitationSource(w.value) {
			continue
		}
		key := w.function + "#" + w.value
		if _, audited := auditedLimitationWrites[key]; !audited {
			t.Errorf("%s writes a limitations-destined value from %q, which is not the bounded appender; route it through %s or add %q to auditedLimitationWrites with the reason it is already bounded",
				w.position, w.value, boundedLimitationPrimitive, key)
			continue
		}
		matched[key] = true
	}
	for key := range auditedLimitationWrites {
		if !matched[key] {
			t.Errorf("auditedLimitationWrites lists %q, which matches no limitations write; remove it rather than leaving an exemption that describes nothing", key)
		}
	}
}

// verifyBoundedWrappers proves each whitelisted wrapper is what its
// membership claims: a body that does nothing but call the primitive once.
//
// Without this, the whitelist is a promise about names. A wrapper that
// appended its own disclosure after the bounded call would still be trusted
// everywhere it appears, which would launder exactly the write this test
// exists to catch.
func verifyBoundedWrappers(t *testing.T, functions map[string]*ast.FuncDecl) {
	t.Helper()
	for name := range boundedLimitationWrappers {
		function, ok := functions[name]
		if !ok {
			t.Errorf("boundedLimitationWrappers lists %q, which is not a function in this package; remove it rather than whitelisting a name nothing answers to", name)
			continue
		}
		if calls := countCalls(function.Body, boundedLimitationPrimitive); calls != 1 {
			t.Errorf("%s is whitelisted as a bounded wrapper but calls %s %d times, want exactly 1; a wrapper that is not a single delegation cannot be trusted by name",
				name, boundedLimitationPrimitive, calls)
		}
		if calls := countCalls(function.Body, "append"); calls != 0 {
			t.Errorf("%s is whitelisted as a bounded wrapper but contains %d raw append call(s); an addition made inside a whitelisted wrapper bypasses the cap while still looking bounded at every call site",
				name, calls)
		}
	}
}

type limitationWrite struct{ function, value, position string }

// limitationWritesIn reports every write, inside ONE function, to a value
// that reaches a result's Limitations.
//
// Two steps. First find the destined values: anything assigned into a
// `.Limitations` field or named by a `Limitations:` field in a composite
// literal. A destined value fed from a local makes that local destined too,
// transitively, so `result.Limitations = composed` reaches back to whatever
// wrote `composed`. Then report EVERY write to any destined local, with no
// attempt to decide which one wins.
func limitationWritesIn(fileSet *token.FileSet, function *ast.FuncDecl) []limitationWrite {
	var (
		writes   []limitationWrite
		destined = map[string]bool{}
		direct   []limitationWrite
	)
	record := func(value ast.Expr, pos token.Pos) {
		if identifier, ok := value.(*ast.Ident); ok {
			destined[identifier.Name] = true
			return
		}
		direct = append(direct, limitationWrite{
			function: function.Name.Name,
			value:    limitationWriteSource(value),
			position: fileSet.Position(pos).String(),
		})
	}

	ast.Inspect(function.Body, func(node ast.Node) bool {
		// `InvestigationResult{... Limitations: x ...}` -- a KeyValueExpr,
		// never an assignment to a .Limitations field, which is how the
		// whole terminal path once sat outside this guard.
		if literal, ok := node.(*ast.CompositeLit); ok {
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := field.Key.(*ast.Ident); ok && key.Name == "Limitations" {
					record(field.Value, field.Pos())
				}
			}
		}
		if assignment, ok := node.(*ast.AssignStmt); ok {
			for index, target := range assignment.Lhs {
				selector, ok := target.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Limitations" {
					continue
				}
				record(assignmentSource(assignment, index), assignment.Pos())
			}
		}
		// `var r = InvestigationResult{...}` reaches the composite-literal
		// branch above on its own, so nothing is needed for the FIELD side
		// here; what ValueSpec adds is the LOCAL side, handled by
		// localWritesIn below.
		// A raw append straight onto a Limitations field never appears as a
		// value worth resolving, so it is named on sight.
		if call, ok := node.(*ast.CallExpr); ok {
			if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "append" && len(call.Args) > 0 {
				if selector, ok := call.Args[0].(*ast.SelectorExpr); ok && selector.Sel.Name == "Limitations" {
					direct = append(direct, limitationWrite{
						function: function.Name.Name,
						value:    "append",
						position: fileSet.Position(call.Pos()).String(),
					})
				}
			}
		}
		return true
	})
	writes = append(writes, direct...)

	// Every way a local is written, gathered ONCE and reused for both the
	// closure and the reporting below, so the two can never disagree about
	// what counts as a write.
	locals := localWritesIn(function)

	// Transitive closure over locals, to a fixpoint. Bounded by the number
	// of writes, so a chain cannot loop.
	for changed := true; changed; {
		changed = false
		for _, local := range locals {
			if !destined[local.name] {
				continue
			}
			if named, ok := local.source.(*ast.Ident); ok && !destined[named.Name] {
				destined[named.Name] = true
				changed = true
			}
		}
	}

	// Every write to every destined local, judged individually.
	for _, local := range locals {
		if !destined[local.name] {
			continue
		}
		// A destined local fed from another destined local is not a write
		// of new content; the source's own writes are judged where they
		// appear.
		if named, ok := local.source.(*ast.Ident); ok && destined[named.Name] {
			continue
		}
		writes = append(writes, limitationWrite{
			function: function.Name.Name,
			value:    limitationWriteSource(local.source),
			position: fileSet.Position(local.pos).String(),
		})
	}
	return writes
}

// localWrite is one place a named local receives a value.
type localWrite struct {
	name   string
	source ast.Expr
	pos    token.Pos
}

// localWritesIn gathers every write to a named local inside one function,
// in BOTH forms Go offers.
//
// `x := e` and `x = e` are *ast.AssignStmt. `var x = e` is an *ast.ValueSpec
// and is a different node entirely -- scanning only assignments (codex
// round-6 P1) let a raw append hide in a var line, including via a helper
// whose result the caller never writes to a field. Both forms are the same
// thing to this guard, so both are collected here rather than at the two
// places that consume them.
//
// A valueless `var x []string` is deliberately not a write: it declares an
// empty list, which cannot be the write that overflows a cap. Anything later
// added to it arrives as an assignment and is judged then.
//
// WRITE FORMS THIS WALKER DOES NOT SEE, and why each is acceptable:
//
//   - mutation through a pointer or a helper that takes &x. An AST walk
//     cannot follow it, and nothing in this package composes limitations
//     that way. This is the one real blind spot.
//   - copy(x, raw) and element assignment x[i] = s. Both mutate in place
//     without producing a value; neither can grow a list past the cap,
//     which is the property being defended.
//   - a package-level var feeding a field. Reachable only by being named in
//     a field write or a local write, both of which ARE seen; the value it
//     was built from is out of function scope by construction.
//
// Covered without needing a special case, contrary to what they look like:
// a composite literal inside a return statement (the literal walk reaches
// every node in the body, return statements included), and a function call
// flowing straight into the field (recorded as a direct write named for its
// callee, which is not a bounded appender unless it is one).
func localWritesIn(function *ast.FuncDecl) []localWrite {
	var writes []localWrite
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.AssignStmt:
			for index, target := range declaration.Lhs {
				if identifier, ok := target.(*ast.Ident); ok {
					writes = append(writes, localWrite{identifier.Name, assignmentSource(declaration, index), declaration.Pos()})
				}
			}
		case *ast.ValueSpec:
			if len(declaration.Values) == 0 {
				return true
			}
			for index, name := range declaration.Names {
				source := declaration.Values[0]
				if len(declaration.Values) == len(declaration.Names) {
					source = declaration.Values[index]
				}
				writes = append(writes, localWrite{name.Name, source, declaration.Pos()})
			}
		}
		return true
	})
	return writes
}

// assignmentSource picks the expression feeding one target of an
// assignment. For a multi-value right-hand side (`a, b := f()`) every target
// resolves to that one call, which is how the appenders' two results are
// unpacked.
func assignmentSource(assignment *ast.AssignStmt, index int) ast.Expr {
	if len(assignment.Rhs) == len(assignment.Lhs) {
		return assignment.Rhs[index]
	}
	return assignment.Rhs[0]
}

func callsFunction(body *ast.BlockStmt, name string) bool {
	return countCalls(body, name) > 0
}

func countCalls(body *ast.BlockStmt, name string) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == name {
			count++
		}
		return true
	})
	return count
}

// limitationWriteSource names what a limitations write is fed from: the
// callee for a direct call, the shape for a literal, otherwise the
// identifier or expression text, so an audited entry can be keyed on it.
func limitationWriteSource(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.CallExpr:
		if identifier, ok := value.Fun.(*ast.Ident); ok {
			return identifier.Name
		}
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			return selector.Sel.Name
		}
	case *ast.CompositeLit:
		return "composite literal"
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	}
	return "?"
}
