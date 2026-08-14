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
	"Investigate#composed":          "the retrieval-degradation appender's own output, unpacked one line above from appendBoundedLimitations",
	"Investigate#temporallyLimited": "the historical-disclosure appender's own output, unpacked one line above from appendTemporalLimitations, which delegates to appendBoundedLimitations",
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
						value = limitationWriteSource(assignment.Rhs[0])
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
		if w.value == "appendBoundedLimitations" || w.value == "appendTemporalLimitations" {
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
