package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The QUANTIFIER test. Everything else in this package proves a property of the
// exits someone enumerated; this proves a property of ALL of them.
//
// WHY IT EXISTS, precisely. The first version of this guard asserted at five
// exits, each proven by execution, and reported "five, proven by EXECUTION,
// zero by construction". That was a true statement about the five and a FALSE
// one about the population: the five came from enumerating callers of the
// display-label composer, when the correct key is every RETURN in Investigate
// that serves a result -- which is ten, and includes the reuse serve and a
// Save-race terminal reached from three different places.
//
// A coverage test that ranges over a stage vocabulary cannot catch that, because
// it can only ever discover exits the vocabulary already names. Executing every
// member of a set does not repair a set built from the wrong key. So the
// guarantee has to be structural, and these are the two structural facts:
//
//	(1) the late writer can only run inside the finalizer, and
//	(2) nothing can be persisted that has not been through the finalizer.
//
// Together they make an eleventh serving path impossible to add silently rather
// than merely unenumerated -- which is what "seven defeats of the same shape"
// finally buys.

func parsePackageForQuantifier(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ParseComments)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		files = append(files, parsed)
	}
	if len(files) == 0 {
		t.Fatal("parsed zero non-test files: the walk is looking in the wrong place and would pass vacuously")
	}
	return fset, files
}

// enclosingFunc returns the name of the function declaration containing pos.
func enclosingFunc(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if pos >= fn.Body.Pos() && pos <= fn.Body.End() {
			return fn.Name.Name
		}
	}
	return ""
}

// calleeName returns the identifier being called, for both `f(...)` and
// `x.f(...)`.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

// TestLateWritersRunOnlyInsideTheFinalizer is structural fact (1).
//
// `stampAnswerPlan` is the late writer that defeated the previous version of
// this guard: it ran in the CALLER, adding the plan object to the document after
// the callee had already measured and persisted it. If it can be called from
// anywhere else again, the same defeat is one edit away.
func TestLateWritersRunOnlyInsideTheFinalizer(t *testing.T) {
	t.Parallel()
	fset, files := parsePackageForQuantifier(t)

	// Each late writer maps to the ONE function permitted to call it.
	lateWriters := map[string]string{
		"stampAnswerPlan":  "finalizeServed",
		"assertFitsBudget": "finalizeServed",
	}

	seen := map[string]int{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call)
			permitted, tracked := lateWriters[name]
			if !tracked {
				return true
			}
			seen[name]++
			if within := enclosingFunc(file, call.Pos()); within != permitted {
				t.Errorf("%s: %s is called from %q; only %q may call it. A writer that runs outside the finalizer writes after the measurement, which is the defect this guard exists to prevent.",
					fset.Position(call.Pos()), name, within, permitted)
			}
			return true
		})
	}

	// Inputs quantified as carefully as outputs: a walk that found nothing
	// passes for the wrong reason.
	for name := range lateWriters {
		if seen[name] == 0 {
			t.Errorf("found zero calls to %s: this walk proved nothing about it (renamed? moved out of the package?)", name)
		}
	}
}

// TestNothingIsPersistedWithoutBeingFinalized is structural fact (2).
//
// Every function that persists a served result must have run the finalizer
// FIRST, in the same function, at an earlier position. This is the half that
// generalises: a new serving path added tomorrow either goes through
// finalizeServed or fails here, whether or not anyone remembered to add it to a
// vocabulary.
func TestNothingIsPersistedWithoutBeingFinalized(t *testing.T) {
	t.Parallel()
	fset, files := parsePackageForQuantifier(t)

	type site struct {
		file *ast.File
		fn   *ast.FuncDecl
	}
	persisting := map[string]site{}
	finalizedAt := map[string]token.Pos{}
	savedAt := map[string]token.Pos{}

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var save, finalize token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel {
					return true
				}
				// e.results.Save(...) -- the persistence sink.
				if sel.Sel.Name == "Save" {
					if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "results" {
						if !save.IsValid() || call.Pos() < save {
							save = call.Pos()
						}
					}
				}
				if sel.Sel.Name == "finalizeServed" {
					if !finalize.IsValid() || call.Pos() < finalize {
						finalize = call.Pos()
					}
				}
				return true
			})
			if save.IsValid() {
				persisting[fn.Name.Name] = site{file: file, fn: fn}
				savedAt[fn.Name.Name] = save
				finalizedAt[fn.Name.Name] = finalize
			}
		}
	}

	if len(persisting) == 0 {
		t.Fatal("found zero functions persisting a result: the walk is not seeing the persistence sink, so it proves nothing")
	}

	for name := range persisting {
		finalize := finalizedAt[name]
		save := savedAt[name]
		if !finalize.IsValid() {
			t.Errorf("%s persists a result at %s but never calls finalizeServed: it can store and serve a document that was never measured in its final form",
				name, fset.Position(save))
			continue
		}
		if finalize > save {
			t.Errorf("%s calls finalizeServed at %s, AFTER it persists at %s: the stored document was measured too late",
				name, fset.Position(finalize), fset.Position(save))
		}
	}
	t.Logf("quantified over %d persisting functions", len(persisting))
}
