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
// What this does NOT close, stated plainly rather than claimed away a
// fourth time -- the merge-gate round (P2, ARGUED, no live producer) found
// TWO distinct residuals, and the first one shows the invariant above is
// not universally true, only true for constructing a NEW ref from nothing:
//
//  1. Constructing a ref legitimately via EvidenceRefID, then POST-
//     PROCESSING the resulting string -- e.g.
//     strings.Replace(EvidenceRefID(ContextFabricEvidenceEntityCommit, id),
//     "commit", runtimeType, 1). No "acr:v1" literal is required anywhere
//     for this to mint an arbitrary segment, because the invariant this
//     file relies on only holds for MINTING a ref, not for TRANSFORMING an
//     already-valid one. The same applies to []byte/rune assembly, an
//     env-var-sourced prefix, or an embedded-file constant -- any mechanism
//     where the string "acr:v1" is never itself Go source text. This is a
//     deliberately obfuscated, actively-adversarial construction with no
//     legitimate resemblance to how any real producer in this codebase
//     builds a ref; closing it with more static analysis is not a bounded
//     problem (the next round would find regexp.ReplaceAll, unsafe pointer
//     tricks, reflection) the way the const-indirection fix below was.
//     Ordinary code review is the actual, and only, defense here -- a
//     reviewer seeing ANY post-processing of an EvidenceRefID result, or a
//     ref built from a non-literal source, should treat it as an
//     automatic hold. Chris's ruling on this exact residual: accepted --
//     this file's promise is MINT-TIME closure, not post-construction
//     integrity, and RecordEvidenceLabelFallback is the runtime backstop:
//     a stored ref built this way still carries an unregistered segment,
//     so a nonzero fallback count for an acr-minted ref is an INCIDENT
//     SIGNAL to investigate, not noise to ignore.
//  2. A literal split BELOW the substring "acr:v1" itself ("acr" + ":v1")
//     still evades a literal-content scan -- same code-review-territory
//     conclusion, and (like #1) no producer in this codebase has ever come
//     close to it.
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
// one's own acr:v1: literal (or +-chain / strings.Join run of literals, with
// same-file top-level string const identifiers resolved back to their
// values first) ends EXACTLY at the prefix boundary -- nothing hardcoded
// follows before a non-literal operand (string(entityType), an
// enum-derived splice) takes over. This is the SAME check an earlier,
// idiom-enumerating version of this guard ran across the whole tree; layer
// 1 replaced that job tree-wide, so this layer's only remaining purpose is
// making sure the three TRUSTED files stay trustworthy -- a future edit to
// source_queries.go that hardcoded a segment inline (directly, or one
// const hop away: `const prefix = "acr:v1:"; const segment = "service"`,
// the merge-gate round's P2 ARGUED finding, EXECUTED-confirmed by planting
// it) would otherwise sail through layer 1 (the file is allowlisted) with
// nothing else to catch it.
//
// Const resolution is bounded to SAME-FILE, top-level, single-value string
// consts -- not a general dataflow analysis, not vars, not consts from an
// import, not anything requiring more than one file's AST. That is a
// tractable, closed extension appropriate for three small, already fully
// reviewed files; it does not reopen the unbounded-idiom problem the
// coordinator's invariant ruling closed for the rest of the tree.
//
// Also fails on any AMBIGUOUS identifier -- a name resolveStringOperand
// would otherwise resolve to a top-level const, but that is ALSO declared
// as a local (a parameter, a `:=`, a local `var`) SOMEWHERE in the file
// (verification round P2, EXECUTED-confirmed by planting the shadow, chris
// ruled fail-closed 2026-09-01 07:55 PDT): resolveStringOperand's own doc
// comment explains why this check is deliberately NAME-based, not
// object-identity-based, and fails closed rather than risk resolving a
// shadowed reference to the wrong declaration.
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
		consts := fileStringConsts(file)
		shadowed := shadowedLocalNames(file)
		runs, ambiguous := adjacentStringLiteralRuns(file, consts, shadowed)
		for _, a := range ambiguous {
			pos := fset.Position(a.pos)
			violations = append(violations, pos.String()+" ("+rel+"): identifier \""+a.name+"\" is used in a string-building expression AND is ALSO declared as a local elsewhere in this file -- ambiguous without proper scope resolution, failing closed for human review; rename to remove the shadow")
		}
		for _, run := range runs {
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

// shadowedLocalNames returns the set of identifier names declared as a
// LOCAL anywhere in the file -- a function parameter or named result, a
// `:=` short variable declaration, a local `var`, or a range variable.
// Used by resolveStringOperand (via TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary)
// to fail closed whenever a name that ALSO exists as a top-level const
// could plausibly refer to a shadowing local instead -- see that function's
// own doc comment for why "anywhere in the file", not just the enclosing
// function, is the deliberately conservative scope chris ruled for.
func shadowedLocalNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	add := func(ident *ast.Ident) {
		if ident != nil && ident.Name != "" && ident.Name != "_" {
			names[ident.Name] = true
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if node.Tok == token.DEFINE {
				for _, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok {
						add(ident)
					}
				}
			}
		case *ast.GenDecl:
			if node.Tok == token.VAR {
				for _, spec := range node.Specs {
					if valueSpec, ok := spec.(*ast.ValueSpec); ok {
						for _, ident := range valueSpec.Names {
							add(ident)
						}
					}
				}
			}
		case *ast.Field:
			// Covers function parameters AND named results -- both appear
			// as *ast.Field under a *ast.FuncType's Params/Results.
			for _, ident := range node.Names {
				add(ident)
			}
		case *ast.RangeStmt:
			if node.Tok == token.DEFINE {
				if ident, ok := node.Key.(*ast.Ident); ok {
					add(ident)
				}
				if ident, ok := node.Value.(*ast.Ident); ok {
					add(ident)
				}
			}
		}
		return true
	})
	return names
}

// fileStringConsts collects every top-level, single-value string const
// declaration in the file (const NAME = "literal") into a name -> value
// map, so adjacentStringLiteralRuns can resolve an *ast.Ident operand back
// to the literal it names -- see TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary's
// own doc comment for the scope and reasoning.
func fileStringConsts(file *ast.File) map[string]string {
	consts := map[string]string{}
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 {
				continue
			}
			lit, ok := valueSpec.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			consts[valueSpec.Names[0].Name] = value
		}
	}
	return consts
}

// resolveStringOperand returns expr's string value if it is a string
// BasicLit, OR an *ast.Ident naming a same-file top-level string const (per
// fileStringConsts) -- otherwise resolved is false and the caller treats
// expr as a run-breaking non-literal operand.
//
// Deliberately NAME-based, not object-identity-based (verification round
// P2, EXECUTED-confirmed, chris ruled 2026-09-01 07:55 PDT): go/ast alone
// does not resolve which DECLARATION an *ast.Ident refers to -- that needs
// go/types, a real type-checker, with an importer for the file's actual
// imports. Rather than build that for three small files, this resolves by
// NAME and, when a name is ambiguous (it also appears as a shadowedLocalNames
// entry -- a parameter, a `:=`, a local var, ANYWHERE in the file, not just
// the enclosing function), returns ambiguous=true instead of silently
// picking either interpretation. A caller that gets ambiguous=true must
// FAIL the check, not skip it -- resolving the wrong way is what let a
// planted local shadow of a top-level const slip past both layers
// undetected (the exact bug this fail-closed behavior fixes). The correct,
// known endpoint is object-identity resolution via go/types; tracked as a
// follow-up (see the Linear ticket cited in this file's own commit and in
// .codex-review-context.md) rather than built here now.
func resolveStringOperand(expr ast.Expr, consts map[string]string, shadowed map[string]bool) (value string, resolved bool, ambiguous bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false, false
		}
		v, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false, false
		}
		return v, true, false
	case *ast.Ident:
		v, isConst := consts[e.Name]
		if !isConst {
			// Not a top-level const at all -- an ordinary parameter/local
			// like "id" or "entityType". No ambiguity: this is simply a
			// non-literal operand, the same as before consts existed.
			return "", false, false
		}
		if shadowed[e.Name] {
			// IS a top-level const, but the SAME NAME is also declared as
			// a local somewhere in the file -- cannot tell by name alone
			// which declaration this reference means. Fail closed.
			return "", false, true
		}
		return v, true, false
	default:
		return "", false, false
	}
}

type literalRun struct {
	pos    token.Pos
	values []string
}

type ambiguousRef struct {
	pos  token.Pos
	name string
}

// adjacentStringLiteralRuns returns every maximal run of directly-adjacent
// string operands joined by + in the file, every standalone string literal
// that isn't part of any + expression at all, and every strings.Join
// composite-literal's string elements -- the three shapes codex rounds 2
// and 3 found evading an earlier, narrower version of this function (kept
// here for layer 2's use; layer 1 does not need this level of shape
// awareness at all, which is the whole point of closing by invariant). An
// operand may be a literal directly, or an *ast.Ident resolved through
// consts (see fileStringConsts / resolveStringOperand) -- the merge-gate
// round's P2 finding. Any AMBIGUOUS identifier (shadowed per shadowedLocalNames)
// is returned separately, not folded into a run, so the caller can fail
// the check unconditionally on it (verification round P2).
func adjacentStringLiteralRuns(file *ast.File, consts map[string]string, shadowed map[string]bool) ([]literalRun, []ambiguousRef) {
	var runs []literalRun
	var ambiguous []ambiguousRef
	record := func(pos token.Pos, value string) {
		runs = append(runs, literalRun{pos: pos, values: []string{value}})
	}
	appendOperands := func(operands []ast.Expr) {
		var current literalRun
		flush := func() {
			if len(current.values) > 0 {
				runs = append(runs, current)
			}
			current = literalRun{}
		}
		for _, operand := range operands {
			value, resolved, isAmbiguous := resolveStringOperand(operand, consts, shadowed)
			if isAmbiguous {
				flush()
				if ident, ok := operand.(*ast.Ident); ok {
					ambiguous = append(ambiguous, ambiguousRef{pos: operand.Pos(), name: ident.Name})
				}
				continue
			}
			if !resolved {
				flush()
				continue
			}
			if len(current.values) == 0 {
				current.pos = operand.Pos()
			}
			current.values = append(current.values, value)
		}
		flush()
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
			appendOperands(flattenAdd(node))
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
			operands := make([]ast.Expr, len(composite.Elts))
			copy(operands, composite.Elts)
			appendOperands(operands)
			return true
		}
		return true
	})
	return runs, ambiguous
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
