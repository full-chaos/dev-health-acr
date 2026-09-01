package v1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// hardcodedEvidenceEntitySegment matches a HARDCODED "acr:v1:<word>:" shape --
// the exact form the four unregistered devhealthsource segments (CHAOS-4698)
// were minted with, and the pre-fix contents of every site this ticket
// converted. It deliberately does NOT match a bare "acr:v1:" prefix (nothing
// hardcoded follows), which is what EvidenceRefID's own literal and
// source_queries.go/read_adapter.go's SQL-splice prefixes reduce to once the
// entity-type segment comes from a non-literal expression -- see
// TestNoHardcodedEvidenceRefEntitySegments' own doc comment for why those
// sites pass this check without an explicit file-path exception.
var hardcodedEvidenceEntitySegment = regexp.MustCompile(`acr:v1:[A-Za-z][A-Za-z0-9_-]*:`)

// TestNoHardcodedEvidenceRefEntitySegments is CHAOS-4698's OTHER half: the
// closed enum stops an unlabeled MEMBER from compiling, but the wire ref is
// still a plain string, so nothing in the type system stops a producer from
// bypassing EvidenceRefID entirely with "acr:v1:" + "some-new-type" + ":" +
// id -- exactly how the four devhealthsource segments (episode,
// work-item-hierarchy, project-team, work-item-team) got in unlabeled in the
// first place. This test closes that path structurally: it AST-walks every
// production .go file under internal/ (test files are exempt -- a fixture
// asserting the read-time fallback on an unregistered ref, e.g.
// "acr:v1:service:api" in context_fabric_display_labels_test.go, is the
// point of that test, not a producer) and fails on any string literal, or
// contiguous run of string literals joined by +, that contains a hardcoded
// "acr:v1:<word>:" shape.
//
// It walks runs, not single literals, because a single-literal check alone
// has an evasion: "acr:v1:" + "commit" + ":" splits the dangerous shape
// across three literals, none of which individually matches. Flattening a
// maximal run of ADJACENT string-literal operands in a +-chain (a
// non-literal operand -- a variable, a call -- breaks the run) closes that
// gap while leaving the two legitimate patterns untouched: EvidenceRefID's
// own `"acr:v1:" + string(entityType) + ":" + id` (the "acr:v1:" run ends
// with nothing hardcoded after it; string(entityType) breaks the run before
// any word could follow) and source_queries.go/read_adapter.go's SQL splice,
// which ends its own literal run at "...concat('acr:v1:" before splicing in
// string(contractsv1.ContextFabricEvidenceEntityX) and resuming with a fresh
// "..." literal for the rest of the SQL
// (same shape: the literal run ends exactly at "acr:v1:", the enum call
// breaks it). Neither needs a path-based exception -- the regex is
// structurally incapable of matching a run that ends where the entity type
// begins, which is exactly what "route through the typed constructor" means
// for a string-literal AST.
func TestNoHardcodedEvidenceRefEntitySegments(t *testing.T) {
	root := moduleRootFromThisFile(t)
	internalDir := filepath.Join(root, "internal")
	var violations []string
	err := filepath.WalkDir(internalDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, run := range adjacentStringLiteralRuns(file) {
			joined := strings.Join(run.values, "")
			if hardcodedEvidenceEntitySegment.MatchString(joined) {
				pos := fset.Position(run.pos)
				violations = append(violations, pos.String()+" ("+rel+"): "+joined)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/ for evidence-ref literal guard: %v", err)
	}
	for _, v := range violations {
		t.Errorf("hardcoded acr:v1:<entity-type>: segment outside EvidenceRefID: %s -- route this through EvidenceRefID(ContextFabricEvidenceEntityX, id) and add the member to contextFabricEvidenceEntityLabels in the same change", v)
	}
}

type literalRun struct {
	pos    token.Pos
	values []string
}

// adjacentStringLiteralRuns returns every maximal run of directly-adjacent
// string BasicLits joined by + in the file, PLUS every standalone string
// literal that isn't part of any + expression at all -- a bare
// "const q = `SELECT ...`" with no splice. read_adapter.go's
// clickHouseEvidenceQueryV1 was exactly this shape before this ticket fixed
// it, and a first version of this guard that only walked BinaryExpr nodes
// missed it entirely, caught only by running this guard red-first against
// origin/main and noticing that file was silently absent from the failures.
// A non-literal operand (identifier, call, anything else) ends the current
// run without being part of it -- see the test's own doc comment for why
// that is exactly the boundary that keeps EvidenceRefID and the SQL-splice
// sites clean without a path exception.
func adjacentStringLiteralRuns(file *ast.File) []literalRun {
	var runs []literalRun
	record := func(pos token.Pos, value string) {
		runs = append(runs, literalRun{pos: pos, values: []string{value}})
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op != token.ADD {
				// Descend: a non-ADD binary expr (e.g. "x - (a+b)") can
				// still contain a nested ADD chain worth checking.
				return true
			}
			// This is the OUTERMOST node of an ADD chain in top-down
			// traversal order (ast.Inspect visits parents before children).
			// flattenAdd recurses through every nested ADD BinaryExpr
			// itself, so returning false here stops ast.Inspect from
			// separately visiting this chain's own BasicLit children as
			// standalone literals (the *ast.BasicLit case below) or as
			// their own redundant, double-counted runs.
			operands := flattenAdd(node)
			var current literalRun
			flush := func() {
				if len(current.values) > 0 {
					runs = append(runs, current)
				}
				current = literalRun{}
			}
			for _, operand := range operands {
				lit, ok := operand.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					flush()
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					flush()
					continue
				}
				if len(current.values) == 0 {
					current.pos = lit.Pos()
				}
				current.values = append(current.values, value)
			}
			flush()
			return false
		case *ast.BasicLit:
			// Reached only for a string literal NOT inside an ADD chain --
			// any that ARE were already consumed above, whose "return
			// false" stops ast.Inspect's descent before reaching them here.
			if node.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(node.Value); err == nil {
				record(node.Pos(), value)
			}
			return true
		}
		return true
	})
	return runs
}

// flattenAdd returns the leaf operands of a +-expression chain in left-to-
// right order, recursing into nested ADD BinaryExprs so "a" + "b" + "c"
// (which parses as ("a"+"b")+"c") yields ["a","b","c"], not
// [BinaryExpr("a"+"b"), "c"].
func flattenAdd(expr ast.Expr) []ast.Expr {
	bin, ok := expr.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		return []ast.Expr{expr}
	}
	return append(flattenAdd(bin.X), flattenAdd(bin.Y)...)
}

// moduleRootFromThisFile locates the repo root by walking up from this test
// file's own source path (via runtime.Caller, stable regardless of the
// test binary's working directory) until go.mod is found.
func moduleRootFromThisFile(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("evidence-ref literal guard: could not resolve this test file's own path")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("evidence-ref literal guard: no go.mod found walking up from %s", thisFile)
		}
		dir = parent
	}
}
