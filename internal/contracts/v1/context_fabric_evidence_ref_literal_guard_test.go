package v1

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
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
// Const resolution is bounded to SAME-FILE, top-level, string consts -- not
// a general dataflow analysis, not vars, not consts from an import, not
// anything requiring more than one file's own type-checked AST. That is a
// tractable, closed extension appropriate for three small, already fully
// reviewed files; it does not reopen the unbounded-idiom problem the
// coordinator's invariant ruling closed for the rest of the tree.
//
// Resolution is OBJECT-IDENTITY-based (CHAOS-4721, replacing the original
// NAME-based version): go/types type-checks each allowlisted file's own
// package (via golang.org/x/tools/go/packages, real importer, real
// dependency resolution) and resolveStringOperand compares
// types.Info.Uses[ident] against the SPECIFIC types.Object of the tracked
// top-level const of that name -- not the name alone. A same-named local
// (a parameter, a `:=`, a local `var`, or even a local `const`) that
// shadows the top-level const resolves via go/types to a DIFFERENT object,
// so it is never mistaken for the const it shadows (verification round P2,
// EXECUTED-confirmed by planting the shadow; chris ruled fail-closed
// 2026-09-01 07:55 PDT while this fix was tracked as CHAOS-4721). That
// shadow case is reported as a violation -- CERTAIN, not ambiguous, because
// go/types has already resolved exactly which declaration the identifier
// means; it is no longer the file-wide "this name appears as a local
// ANYWHERE" heuristic the interim fix used, so an unrelated same-named
// local elsewhere in the file that is never used in one of these
// expressions no longer produces a spurious fail.
func TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary(t *testing.T) {
	root := moduleRootFromThisFile(t)
	modulePath := moduleImportPath(t, root)

	importPaths := map[string]bool{}
	for rel := range evidenceRefLiteralAllowlist {
		importPaths[modulePath+"/"+filepath.ToSlash(filepath.Dir(rel))] = true
	}
	patterns := make([]string, 0, len(importPaths))
	for importPath := range importPaths {
		patterns = append(patterns, importPath)
	}

	cfg := &packages.Config{
		Dir:  root,
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("packages.Load(%v): %v", patterns, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("packages.Load(%v) reported %d package error(s) (printed above) -- an allowlisted package did not type-check cleanly", patterns, n)
	}
	pkgByPath := make(map[string]*packages.Package, len(pkgs))
	for _, p := range pkgs {
		pkgByPath[p.PkgPath] = p
	}

	var violations []string
	for rel := range evidenceRefLiteralAllowlist {
		importPath := modulePath + "/" + filepath.ToSlash(filepath.Dir(rel))
		pkg, ok := pkgByPath[importPath]
		if !ok || pkg.TypesInfo == nil || pkg.Types == nil {
			t.Fatalf("packages.Load did not return usable type info for %s (needed for allowlisted file %s)", importPath, rel)
		}
		absPath := filepath.Join(root, filepath.FromSlash(rel))
		target, statErr := os.Stat(absPath)
		if statErr != nil {
			t.Fatalf("stat allowlisted file %s: %v", rel, statErr)
		}
		file := findSyntaxFile(t, pkg, target)
		fileConsts := topLevelStringConstsDeclaredInFile(pkg, target)
		runs, shadows := adjacentStringLiteralRuns(file, pkg.TypesInfo, fileConsts)
		for _, s := range shadows {
			pos := pkg.Fset.Position(s.pos)
			violations = append(violations, pos.String()+" ("+rel+"): identifier \""+s.name+"\" shadows the top-level const of the same name and is used in a string-building expression in this trusted file -- go/types confirms it refers to a DIFFERENT declaration than the tracked const, which is exactly the shape a hardcoded-segment evasion would take; rename to remove the shadow")
		}
		for _, run := range runs {
			joined := strings.Join(run.values, "")
			if hardcodedEvidenceEntitySegment.MatchString(joined) {
				pos := pkg.Fset.Position(run.pos)
				violations = append(violations, pos.String()+" ("+rel+"): "+joined)
			}
		}
	}
	for _, v := range violations {
		t.Errorf("allowlisted file's acr:v1: literal run does not end exactly at the prefix boundary -- an entity-type segment is hardcoded even inside a reviewed producer file: %s", v)
	}
}

// moduleImportPath reads the module directive from go.mod at root, so the
// allowlist's repo-relative file paths (the single source of truth for
// which files/packages this guard type-checks) can be turned into import
// paths for packages.Load without a second, separately-maintained mapping
// that could drift from evidenceRefLiteralAllowlist.
func moduleImportPath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	t.Fatalf("go.mod at %s has no module directive", root)
	return ""
}

// findSyntaxFile returns the *ast.File packages.Load parsed for target out
// of pkg.Syntax -- the SAME AST nodes pkg.TypesInfo.Uses/Defs key on, unlike
// a fresh go/parser.ParseFile of the same path, whose *ast.Ident nodes would
// not appear in the type-checked package's Uses map at all. Matched via
// os.SameFile rather than a raw path-string comparison, robust to symlinks
// and any path normalization the go/packages driver applies.
func findSyntaxFile(t *testing.T, pkg *packages.Package, target os.FileInfo) *ast.File {
	t.Helper()
	for _, f := range pkg.Syntax {
		filename := pkg.Fset.Position(f.Pos()).Filename
		info, err := os.Stat(filename)
		if err != nil {
			continue
		}
		if os.SameFile(target, info) {
			return f
		}
	}
	t.Fatalf("packages.Load(%s) did not include syntax for the requested file", pkg.PkgPath)
	return nil
}

// topLevelStringConstsDeclaredInFile returns the package-scope (top-level)
// string consts whose declaration lives in target, keyed by name, as their
// go/types objects -- NOT their values; resolveStringOperand reads the
// value straight off the *types.Const via go/constant, so there is only one
// place (the type checker itself) that ever decides what a const's value
// is. Restricted to consts declared in target (matched via os.SameFile, not
// a path string) rather than any const visible from the package scope,
// preserving the deliberate SAME-FILE bound TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary's
// own doc comment explains -- a const declared in a sibling file of the
// same package is out of scope here even though go/types would resolve it
// fine, because this check is not a general dataflow analysis.
func topLevelStringConstsDeclaredInFile(pkg *packages.Package, target os.FileInfo) map[string]types.Object {
	consts := map[string]types.Object{}
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		constObj, ok := scope.Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		basic, ok := constObj.Type().Underlying().(*types.Basic)
		if !ok || basic.Info()&types.IsString == 0 {
			continue
		}
		declFilename := pkg.Fset.Position(constObj.Pos()).Filename
		declInfo, err := os.Stat(declFilename)
		if err != nil || !os.SameFile(target, declInfo) {
			continue
		}
		consts[name] = constObj
	}
	return consts
}

// resolveStringOperand returns expr's string value if it is a string
// BasicLit, OR an *ast.Ident that go/types resolves (via info.Uses) to
// EXACTLY the types.Object of a same-file top-level string const named in
// fileConsts -- otherwise resolved is false and the caller treats expr as a
// run-breaking non-literal operand.
//
// OBJECT-IDENTITY-based (CHAOS-4721, replacing the interim NAME-based
// version): info comes from a real go/types check of the file's actual
// package, with a real importer resolving its actual imports (via
// golang.org/x/tools/go/packages -- see
// TestAllowlistedAcrV1LiteralsEndAtThePrefixBoundary), so info.Uses[e] is
// the SPECIFIC declaration e refers to, not a name to pattern-match. When
// e's name matches a tracked top-level const but info.Uses[e] is a
// DIFFERENT object -- a parameter, a `:=`, a local var, or even a local
// `const` of the same name -- that is a certain, not ambiguous, shadow:
// go/types has already told us this reference does NOT mean the tracked
// const, so resolveStringOperand does not resolve it (shadows=true) rather
// than risk resolving a shadowed reference to the wrong declaration, the
// exact bug the interim, name-based version had (a planted local shadow of
// a top-level const silently substituted the CONST's value for the
// reference, verification round P2).
func resolveStringOperand(expr ast.Expr, info *types.Info, fileConsts map[string]types.Object) (value string, resolved bool, shadows bool) {
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
		tracked, isTracked := fileConsts[e.Name]
		if !isTracked {
			// Not a name this file declares as a top-level string const at
			// all -- an ordinary parameter/local like "id" or "entityType".
			return "", false, false
		}
		if obj := info.Uses[e]; obj == tracked {
			constObj := obj.(*types.Const)
			return constant.StringVal(constObj.Val()), true, false
		}
		// The name matches a tracked top-level const, but go/types resolved
		// THIS specific reference to a different object -- a local shadow.
		return "", false, true
	default:
		return "", false, false
	}
}

type literalRun struct {
	pos    token.Pos
	values []string
}

type shadowRef struct {
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
// fileConsts via go/types object identity (see resolveStringOperand) -- the
// merge-gate round's P2 finding. Any identifier resolveStringOperand
// reports as SHADOWING a tracked const is returned separately, not folded
// into a run, so the caller can fail the check on it (verification round
// P2, now object-identity-certain rather than name-ambiguous).
func adjacentStringLiteralRuns(file *ast.File, info *types.Info, fileConsts map[string]types.Object) ([]literalRun, []shadowRef) {
	var runs []literalRun
	var shadowed []shadowRef
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
			value, resolved, shadows := resolveStringOperand(operand, info, fileConsts)
			if shadows {
				flush()
				if ident, ok := operand.(*ast.Ident); ok {
					shadowed = append(shadowed, shadowRef{pos: operand.Pos(), name: ident.Name})
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
	return runs, shadowed
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
