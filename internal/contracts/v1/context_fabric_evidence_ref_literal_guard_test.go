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

// evidenceRefLiteralAllowlist is the SHORT, closed list of files allowed to
// mention "acr:v1" in a string literal at all -- the only places a producer
// may construct part of an acr:v1:<entity-type>:<id> ref.
//
// This is CHAOS-4698's structural close of a problem two codex rounds
// exposed while this guard chased individual construction IDIOMS instead:
// round 2 found a hardcoded literal AND a Sprintf format string evading an
// idiom-specific check; round 3 found strings.Join evading the fix for
// those. Each fix closed the ONE shape raised, and each new round found
// another, because Go string assembly has no finite list of shapes
// (bytes.Buffer, strings.Builder, a loop, text/template -- all still
// possible after three rounds of idiom-chasing). The lane coordinator's
// ruling: stop enumerating idioms, close by INVARIANT instead. The
// invariant is simple and idiom-independent -- ANY mechanism that ever
// mints an acr:v1:<entity-type>:<id> ref must have the literal substring
// "acr:v1" appear SOMEWHERE in that mechanism's own source as a string
// literal, regardless of what combines it with anything else at runtime.
// So instead of asking "does this specific AST shape look dangerous,"
// TestNoAcrV1LiteralOutsideAllowlist asks the idiom-independent question:
// "does ANY string literal in this file mention acr:v1 at all" -- and if
// the file isn't one of these three, that is a violation regardless of
// idiom, full stop. The idiom-enumeration problem disappears because
// nothing is enumerated.
//
// The one thing this does NOT close, stated plainly rather than claimed
// away a fourth time: a literal split BELOW the substring "acr:v1" itself
// -- "acr" + ":v1", or "a"+"cr:v1" -- still evades a literal-content scan.
// That remains code-review territory a static check cannot replace; it is
// also a shape no codex round has found and no producer in this codebase
// has ever come close to (every real site either concatenates or splices
// at a natural word boundary, never mid-token).
var evidenceRefLiteralAllowlist = map[string]bool{
	"internal/contracts/v1/context_fabric_types.go": true, // EvidenceRefID itself
	"internal/contextpacket/source_queries.go":      true, // SQL catalog, enum-spliced
	"internal/contextpacket/read_adapter.go":        true, // SQL catalog, enum-spliced
}

// TestNoAcrV1LiteralOutsideAllowlist is layer 1, the invariant check: it
// AST-walks every production (non-`_test.go`) .go file under internal/
// OTHER than the three files in evidenceRefLiteralAllowlist (test files are
// exempt -- a fixture asserting the read-time fallback on an unregistered
// ref, e.g. "acr:v1:service:api" in context_fabric_display_labels_test.go,
// is the point of that test, not a producer) and fails if ANY string
// literal contains the substring "acr:v1", regardless of what AST shape
// surrounds it -- a bare literal, a +-chain, a Sprintf format string, a
// strings.Join slice element, a strings.Builder.WriteString argument, a
// loop body, anything. See evidenceRefLiteralAllowlist's own doc comment
// for why this closes the idiom-enumeration problem two prior codex rounds
// exposed, and what it still cannot close.
func TestNoAcrV1LiteralOutsideAllowlist(t *testing.T) {
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
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if evidenceRefLiteralAllowlist[rel] {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			if strings.Contains(value, "acr:v1") {
				pos := fset.Position(lit.Pos())
				violations = append(violations, pos.String()+" ("+rel+"): "+value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/ for the acr:v1 literal allowlist guard: %v", err)
	}
	for _, v := range violations {
		t.Errorf("acr:v1 literal outside the allowlist: %s -- route this through EvidenceRefID(ContextFabricEvidenceEntityX, id) instead of embedding the prefix directly, and add the member to contextFabricEvidenceEntityLabels in the same change (or, if this file genuinely needs to mention the prefix, add it to evidenceRefLiteralAllowlist and verify it under TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary)", v)
	}
}

// hardcodedEvidenceEntitySegment matches "acr:v1:" followed by ANY further
// character in the SAME literal or literal run -- not just a hardcoded
// word. Used only by layer 2, TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary,
// to verify that even the three files layer 1 exempts don't hardcode an
// entity-type segment themselves.
var hardcodedEvidenceEntitySegment = regexp.MustCompile(`acr:v1:[\s\S]`)

// TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary is layer 2: within the
// three files evidenceRefLiteralAllowlist exempts from layer 1, verify each
// one's own acr:v1: literal (or +-chain / strings.Join run of literals)
// ends EXACTLY at the prefix boundary -- nothing hardcoded follows before a
// non-literal operand (string(entityType), an enum-derived splice) takes
// over. This is the SAME check an earlier, idiom-enumerating version of
// this guard ran across the whole tree; layer 1 replaced that job
// tree-wide, so this layer's only remaining purpose is making sure the
// three TRUSTED files stay trustworthy -- a future edit to
// source_queries.go that hardcoded a segment inline would otherwise sail
// through layer 1 (the file is allowlisted) with nothing else to catch it.
func TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary(t *testing.T) {
	root := moduleRootFromThisFile(t)
	var violations []string
	for rel := range evidenceRefLiteralAllowlist {
		path := filepath.Join(root, filepath.FromSlash(rel))
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse allowlisted file %s: %v", rel, parseErr)
		}
		for _, run := range adjacentStringLiteralRuns(file) {
			joined := strings.Join(run.values, "")
			if hardcodedEvidenceEntitySegment.MatchString(joined) {
				pos := fset.Position(run.pos)
				violations = append(violations, pos.String()+" ("+rel+"): "+joined)
			}
		}
	}
	for _, v := range violations {
		t.Errorf("allowlisted file's acr:v1: literal run does not end exactly at the prefix boundary -- an entity-type segment is hardcoded even inside a reviewed producer file: %s", v)
	}
}

type literalRun struct {
	pos    token.Pos
	values []string
}

// adjacentStringLiteralRuns returns every maximal run of directly-adjacent
// string BasicLits joined by + in the file, every standalone string literal
// that isn't part of any + expression at all, and every strings.Join
// composite-literal's string elements -- the three shapes codex rounds 2
// and 3 found evading an earlier, narrower version of this function (kept
// here for layer 2's use; layer 1 does not need this level of shape
// awareness at all, which is the whole point of closing by invariant).
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
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Join" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "strings" || len(node.Args) == 0 {
				return true
			}
			composite, ok := node.Args[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			var current literalRun
			flush := func() {
				if len(current.values) > 0 {
					runs = append(runs, current)
				}
				current = literalRun{}
			}
			for _, elt := range composite.Elts {
				lit, ok := elt.(*ast.BasicLit)
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
