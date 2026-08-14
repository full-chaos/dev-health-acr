package contextfabric

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// auditedLimitationWrites are the assignments to a result's Limitations
// that are NOT the bounded appender's output, each with the reason it is
// safe.
//
// Two-sided, like the sidecar's body-read audit: an unlisted write fails,
// and a listed entry matching nothing fails too. Keyed by enclosing
// function so ordinary edits above a site do not churn the list.
var auditedLimitationWrites = map[string]string{
	"Synthesize#cloneSlice": "an INTERMEDIATE, not a list that reaches a consumer: this is the model's own draft list entering the synthesized result, and Investigate then passes result.Limitations through appendTemporalLimitations UNCONDITIONALLY -- it is called on every axis, current included, and appendBoundedLimitations normalizes an already-over-cap input -- before Validate runs",
}

// boundedLimitationAppenders are the functions that own the cap. Anything a
// Limitations field resolves to must be one of these, or an audited
// exception.
//
// withRetrievalDegradation and appendTemporalLimitations are here rather
// than audited because each is a thin wrapper that does nothing but call
// appendBoundedLimitations with its own fixed disclosure -- they ARE the
// bounded path, under narrower names. Both were previously audited
// exemptions; resolving locals to their source (codex round-4 F2) made the
// exemptions unnecessary, which is the better outcome: a guard that
// understands the code needs fewer promises about it.
var boundedLimitationAppenders = map[string]bool{
	"appendBoundedLimitations":  true,
	"appendTemporalLimitations": true,
	"withRetrievalDegradation":  true,
}

// TestEveryLimitationAppendIsBounded closes the CLASS behind round-17
// finding 1 rather than the site.
//
// The cap was handled where the degradation disclosure was appended and
// nowhere else, so CHAOS-3781's historical disclosures were appended on
// top of a full list and the whole investigation died at validation. That
// is not a bug in either appender; it is what happens when "the cap" lives
// at a call site instead of in one function every append has to go
// through.
//
// So this pins the shape: inside this package, nothing may append to a
// composed result's Limitations except appendBoundedLimitations. Every
// assignment to a `.Limitations` field must carry a value the appender
// produced, or be an audited exception with a stated reason. A third
// append site is then a test failure at the moment it is written, not a
// defect a later round finds.
func TestEveryLimitationAppendIsBounded(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	type write struct{ function, value, position string }
	var writes []write
	matched := map[string]bool{}
	sawAppender := false

	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "appendBoundedLimitations" {
						sawAppender = true
					}
					// A raw append onto a Limitations field bypasses the
					// appender entirely and never shows up as an
					// assignment worth auditing, so it is named here.
					if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "append" && len(call.Args) > 0 {
						if selector, ok := call.Args[0].(*ast.SelectorExpr); ok && selector.Sel.Name == "Limitations" {
							t.Errorf("%s appends directly onto a Limitations field; every addition must go through appendBoundedLimitations, which owns the cap and the displacement count",
								fileSet.Position(call.Pos()))
						}
					}
				}
				// A composite literal is the OTHER way a list reaches a
				// result, and the way the selector walk below cannot see:
				// `InvestigationResult{... Limitations: x ...}` is a
				// KeyValueExpr, never an assignment to a .Limitations
				// field. terminalResult composes exactly that way, so
				// before this the whole terminal path was outside the
				// guard (codex round-4 F2).
				if literal, ok := node.(*ast.CompositeLit); ok {
					for _, element := range literal.Elts {
						field, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := field.Key.(*ast.Ident)
						if !ok || key.Name != "Limitations" {
							continue
						}
						writes = append(writes, write{
							function: enclosingFunctionName(file, field.Pos()),
							value:    resolveLimitationSource(file, field.Value, field.Pos()),
							position: fileSet.Position(field.Pos()).String(),
						})
					}
				}
				assignment, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, target := range assignment.Lhs {
					selector, ok := target.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "Limitations" {
						continue
					}
					value := "?"
					if len(assignment.Rhs) == 1 {
						value = resolveLimitationSource(file, assignment.Rhs[0], assignment.Pos())
					}
					writes = append(writes, write{
						function: enclosingFunctionName(file, assignment.Pos()),
						value:    value,
						position: fileSet.Position(assignment.Pos()).String(),
					})
				}
				return true
			})
		}
	}

	if !sawAppender {
		t.Fatal("found no call to appendBoundedLimitations at all; the walker is not reaching the engine and would pass over any unbounded append")
	}
	if len(writes) == 0 {
		t.Fatal("found no Limitations assignment at all; the walker is not reaching the composition code")
	}

	for _, w := range writes {
		if boundedLimitationAppenders[w.value] {
			continue
		}
		key := w.function + "#" + w.value
		if _, audited := auditedLimitationWrites[key]; !audited {
			t.Errorf("%s assigns Limitations from %q, which is not the bounded appender; route it through appendBoundedLimitations or add %q to auditedLimitationWrites with the reason it is already bounded",
				w.position, w.value, key)
			continue
		}
		matched[key] = true
	}
	for key := range auditedLimitationWrites {
		if !matched[key] {
			t.Errorf("auditedLimitationWrites lists %q, which matches no Limitations assignment; remove it rather than leaving an exemption that describes nothing", key)
		}
	}
}

// resolveLimitationSource names what ultimately feeds a Limitations field,
// following LOCAL VARIABLES to the expression that last wrote them.
//
// The property this guard defends is "no list reaches a result unbounded",
// not "no selector write happens". A local defeats the shallow reading
// twice over: terminalResult builds `limitations` with a raw literal, raw-
// appends to it, passes it THROUGH appendTemporalLimitations, and only then
// puts it in the result literal. Naming the immediate expression would call
// that unbounded (it is an identifier) and would equally call an un-appended
// local bounded. Neither answers the question.
//
// So an identifier resolves to the source of its latest write BEFORE the use
// site, transitively -- `limitations = temporallyLimited` resolves on to
// `appendTemporalLimitations`. What matters is the LAST thing to touch the
// list before it reaches the result: a raw append that happens earlier is
// fine precisely because the appender still normalizes it afterwards, and a
// raw append moved AFTER the appender resolves to "append" and fails.
//
// Depth is capped rather than cycle-detected; Go locals in this package do
// not chain deeply, and a runaway chain should read as unresolved (which
// fails) rather than hang.
func resolveLimitationSource(file *ast.File, expression ast.Expr, use token.Pos) string {
	for depth := 0; depth < 8; depth++ {
		identifier, ok := expression.(*ast.Ident)
		if !ok {
			return limitationWriteSource(expression)
		}
		source, pos, found := latestLocalWrite(file, identifier.Name, use)
		if !found {
			// A parameter, a package-level value, or something written
			// only after this point. Unresolvable is not safe-by-default:
			// it is reported under the identifier's own name so it must be
			// audited deliberately.
			return identifier.Name
		}
		expression, use = source, pos
	}
	return "?"
}

// latestLocalWrite finds the assignment to name closest before use, within
// the function enclosing use. Both `:=` and `=` count; for a multi-value
// right-hand side (`a, b := f()`) every target resolves to that one call,
// which is exactly how appendTemporalLimitations' two results are unpacked.
func latestLocalWrite(file *ast.File, name string, use token.Pos) (ast.Expr, token.Pos, bool) {
	var (
		best      ast.Expr
		bestPos   token.Pos
		bestFound bool
	)
	ast.Inspect(file, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || assignment.Pos() >= use {
			return true
		}
		for index, target := range assignment.Lhs {
			identifier, ok := target.(*ast.Ident)
			if !ok || identifier.Name != name {
				continue
			}
			source := assignment.Rhs[0]
			if len(assignment.Rhs) == len(assignment.Lhs) {
				source = assignment.Rhs[index]
			}
			if !bestFound || assignment.Pos() > bestPos {
				best, bestPos, bestFound = source, assignment.Pos(), true
			}
		}
		return true
	})
	return best, bestPos, bestFound
}

// limitationWriteSource names what a Limitations assignment is fed from:
// the callee for a direct call, otherwise the identifier or expression
// text, so an audited entry can be keyed on it.
func limitationWriteSource(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.CallExpr:
		if identifier, ok := value.Fun.(*ast.Ident); ok {
			return identifier.Name
		}
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			return selector.Sel.Name
		}
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	}
	return "?"
}

func enclosingFunctionName(file *ast.File, pos token.Pos) string {
	name := "?"
	ast.Inspect(file, func(node ast.Node) bool {
		function, ok := node.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if function.Pos() <= pos && pos <= function.End() {
			name = function.Name.Name
		}
		return true
	})
	return name
}
