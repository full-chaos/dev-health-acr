// Package grantclass is the static guard that every decision on a caller's
// grant class is recorded (CHAOS-12297, the c1 step of CHAOS-12228; design:
// docs/design/context-fabric-project-only-scope.md).
//
// A class type is one of the grant vocabularies: auth.GrantClass,
// gatevocab.PrincipalClass (re-exported by directread),
// contextfabric.StoredResultPrincipalScope and directread.CallerClass. A
// fifth class added to auth.GrantClass reaches every gate only through one of
// these, so a site that reads one of them is a decision the new class needs.
// A site is one of:
//
//	compare         ==/!= with a class-typed operand (every package: a
//	                decision inside the type's own package is still one)
//	switch-partial  a tagged switch over a class value that does not name
//	                every member of the vocabulary
//	switch-default  a tagged switch over a class value with a default clause
//	derived-escape  a class comparison or a classifier result that leaves
//	                its expression as a return value, a struct field or a
//	                composite-literal element (a derived bool another
//	                function reads), directly or through a local variable
//	                it was assigned to
//	lookup          an index into a map keyed by a class type (a decision
//	                by table: a class missing from the map reads the zero
//	                value)
//	method-value    a function or method whose signature takes, returns or
//	                has a receiver of a class type, used as a value instead
//	                of being called
//	transport       a class value converted to a string or an interface
//	                (explicitly, or as a call argument, composite element,
//	                assignment or return of interface type)
//	classify        a call of a classifier or a vocabulary mapping
//	raw-grant       a call of a classifier that reads the raw repository
//	                grant list instead of a class (c2's reserved sentinel
//	                keeps these fail closed; they are decisions too)
//
// Every site needs a row in decisions.tsv (file, symbol, kind, count,
// decision, note). The decision is r-sub (the restricted path is the right
// one for project_only), proj (project-aware logic is due, CHAOS-12299 or
// CHAOS-12301), deny (project_only is refused here), leave (repository-only
// logic the reserved sentinel keeps fail closed), consent (the original
// consent grant, never the effective scope) or vocab (the mapping, a
// vocabulary or telemetry). A row decided "open" FAILS: no site ships
// undecided. An unlisted site, a count that differs, a row without a note
// and a row that matches nothing FAIL too.
//
// NOT covered, stated: a class value carried through a variable of a
// non-class type other than string or an interface (a named string type of
// its own); a decision made by reading RepositoryScopes directly outside the
// raw-grant classifiers (c2's sentinel covers those); generated code outside
// every release build (pinned by name, as in rowsclose).
package grantclass

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/sourceload"
	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const (
	modulePath    = "github.com/full-chaos/dev-health-acr"
	fixturesBase  = modulePath + "/internal/cigates/grantclass/testdata/planted"
	decisionsFile = "decisions.tsv"
)

// The class types, by package path and type name.
var classTypes = map[string]bool{
	modulePath + "/internal/auth.GrantClass":                                   true,
	modulePath + "/internal/contextfabric/directread/gatevocab.PrincipalClass": true,
	modulePath + "/internal/contextfabric.StoredResultPrincipalScope":          true,
	modulePath + "/internal/contextfabric/directread.CallerClass":              true,
}

// The classifiers and vocabulary mappings (kind classify), and the raw-grant
// classifiers (kind raw-grant), by package path and function name. The
// planted fixtures declare their own copies under the same names.
var classifiers = map[string]bool{
	modulePath + "/internal/auth.ClassifyGrant":                               true,
	modulePath + "/internal/contextfabric/directread.ClassifyPrincipal":       true,
	modulePath + "/internal/contextfabric/directread.PrincipalClassForGrant":  true,
	modulePath + "/internal/contextfabric/directread.CallerClassFor":          true,
	modulePath + "/internal/contextfabric/directread.CallerClassForGrant":     true,
	modulePath + "/internal/contextfabric.classifyStoredResultPrincipalScope": true,
	modulePath + "/internal/contextfabric.storedResultScopeForGrant":          true,
	fixturesBase + "/classify.Classify":                                       true,
}

var rawGrantClassifiers = map[string]bool{
	modulePath + "/internal/contextfabric/graphrank.scopesUnrestricted":          true,
	modulePath + "/internal/contextfabric/falkorgraph.needsProjectReach":         true,
	modulePath + "/internal/contextfabric/devhealthfacts.orgWideFigureAllowed":   true,
	modulePath + "/internal/contextfabric/devhealthfacts.sourceHealthRestricted": true,
	modulePath + "/internal/contextfabric.workItemRepositoryRestricted":          true,
	modulePath + "/internal/contextfabric/sourcerow.admitsWildcard":              true,
	fixturesBase + "/classify.RawGrant":                                          true,
}

// pinnedOutOfScope is the set of production files outside every release
// build, the same set rowsclose pins.
var pinnedOutOfScope = []string{
	"cmd/acr-mcp/lifecycle_compiled_fixture.go",
	"internal/contracts/v1/answer_bound_race.go",
	"internal/sidecar/credential_keyring_compiled_lifecycle_fixture.go",
	"internal/sidecar/credential_lifecycle_lock_compiled_fixture_unix.go",
	"internal/sidecar/credential_lifecycle_lock_compiled_fixture_unsupported.go",
}

const (
	kindCompare       = "compare"
	kindSwitchPartial = "switch-partial"
	kindSwitchDefault = "switch-default"
	kindDerivedEscape = "derived-escape"
	kindMethodValue   = "method-value"
	kindTransport     = "transport"
	kindClassify      = "classify"
	kindLookup        = "lookup"
	kindRawGrant      = "raw-grant"
)

// decisions is the closed set of row decisions; "open" is refused.
var decisions = map[string]bool{"r-sub": true, "proj": true, "deny": true, "leave": true, "consent": true, "vocab": true}

type site struct{ file, symbol, kind string }

type decisionRow struct {
	count    int
	decision string
	note     string
}

func TestEveryGrantClassDecisionIsRecorded(t *testing.T) {
	root := repopath.Root(t)
	found, problems, loaded := scan(t, root, []string{"./..."})
	if path := os.Getenv("GRANTCLASS_DUMP"); path != "" {
		dump(t, path, found)
	}
	problems = append(problems, compare(found, readDecisions(t, decisionsFile))...)
	problems = append(problems, scopeProblems(outsideBuild(t, root, loaded), pinnedOutOfScope)...)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(problems) > 0 {
		t.Log("fix: give the site a decisions.tsv row (r-sub, proj, deny, leave, consent or vocab) that says what project_only does there; see docs/design/context-fabric-project-only-scope.md")
	}
	t.Logf("grant-class sites: %d, in %d distinct (file, symbol, kind) keys; all decided in decisions.tsv", sum(found), len(found))
}

func sum(counts map[site]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func scan(t *testing.T, root string, patterns []string) (map[site]int, []string, map[string]bool) {
	t.Helper()
	targets := releaseTargets()
	results := make([]targetScan, len(targets))
	var wait sync.WaitGroup
	for i, target := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i] = scanTarget(root, patterns, target)
		}()
	}
	wait.Wait()
	found := map[site]int{}
	var problems []string
	loaded := map[string]bool{}
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("load: %v", result.err)
		}
		problems = append(problems, result.problems...)
		for name := range result.loaded {
			loaded[name] = true
		}
		for key, count := range result.counts {
			if count > found[key] {
				found[key] = count
			}
		}
	}
	sort.Strings(problems)
	return found, uniqueStrings(problems), loaded
}

type targetScan struct {
	counts   map[site]int
	problems []string
	loaded   map[string]bool
	err      error
}

func scanTarget(root string, patterns []string, target [2]string) targetScan {
	loader, err := sourceload.Load(root, patterns, target[0], target[1])
	if err != nil {
		return targetScan{err: err}
	}
	result := targetScan{counts: map[site]int{}, loaded: map[string]bool{}}
	result.problems = loader.Problems(target[0], target[1])
	for _, pkg := range loader.Roots {
		unit := loader.Units[pkg.ID]
		for _, name := range pkg.GoFiles {
			result.loaded[name] = true
		}
		for _, file := range unit.Files {
			rel, relErr := filepath.Rel(root, loader.Fset.Position(file.Pos()).Filename)
			if relErr != nil {
				return targetScan{err: relErr}
			}
			rel = filepath.ToSlash(rel)
			for _, declaration := range file.Decls {
				symbol := declSymbol(declaration)
				for _, kind := range sitesIn(unit.Info, declaration) {
					result.counts[site{rel, symbol, kind}]++
				}
			}
		}
	}
	return result
}

// sitesIn is the kind of every grant-class site in declaration.
func sitesIn(info *types.Info, declaration ast.Decl) []string {
	derived := derivedLocals(info, declaration)
	var kinds []string
	var stack []ast.Node
	ast.Inspect(declaration, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		kinds = append(kinds, siteKinds(info, stack, derived)...)
		return true
	})
	return kinds
}

// derivedLocals is every local variable of declaration assigned a class
// comparison or a classifier result: a use of one that escapes is a derived
// bool (or class) leaving the deciding function.
func derivedLocals(info *types.Info, declaration ast.Decl) map[types.Object]bool {
	derived := map[types.Object]bool{}
	ast.Inspect(declaration, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for i, rhs := range assign.Rhs {
			ident, isIdent := assign.Lhs[i].(*ast.Ident)
			if !isIdent || !isDecision(info, rhs) {
				continue
			}
			if obj := info.ObjectOf(ident); obj != nil {
				derived[obj] = true
			}
		}
		return true
	})
	return derived
}

// isDecision: expr is a class comparison or a classifier call, possibly
// negated or parenthesized.
func isDecision(info *types.Info, expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return isDecision(info, e.X)
	case *ast.UnaryExpr:
		return e.Op == token.NOT && isDecision(info, e.X)
	case *ast.BinaryExpr:
		return (e.Op == token.EQL || e.Op == token.NEQ) && (isClass(info.TypeOf(e.X)) || isClass(info.TypeOf(e.Y)))
	case *ast.CallExpr:
		fn := calledFunc(info, e)
		return fn != nil && classifiers[funcKey(fn)]
	}
	return false
}

func siteKinds(info *types.Info, stack []ast.Node, derived map[types.Object]bool) []string {
	node := stack[len(stack)-1]
	var kinds []string
	switch n := node.(type) {
	case *ast.IndexExpr:
		if m, ok := types.Unalias(info.TypeOf(n.X)).Underlying().(*types.Map); ok && isClass(m.Key()) {
			kinds = append(kinds, kindLookup)
		}
	case *ast.BinaryExpr:
		if (n.Op == token.EQL || n.Op == token.NEQ) && (isClass(info.TypeOf(n.X)) || isClass(info.TypeOf(n.Y))) {
			kinds = append(kinds, kindCompare)
			if escapes(stack) {
				kinds = append(kinds, kindDerivedEscape)
			}
		}
	case *ast.SwitchStmt:
		if n.Tag != nil {
			if named := className(info.TypeOf(n.Tag)); named != nil {
				partial, hasDefault := switchCoverage(info, n, named)
				if partial {
					kinds = append(kinds, kindSwitchPartial)
				}
				if hasDefault {
					kinds = append(kinds, kindSwitchDefault)
				}
			}
		}
	case *ast.CallExpr:
		if tv, ok := info.Types[n.Fun]; ok && tv.IsType() {
			if len(n.Args) == 1 && isClass(info.TypeOf(n.Args[0])) && isStringOrInterface(tv.Type) {
				kinds = append(kinds, kindTransport)
			}
			return kinds
		}
		if fn := calledFunc(info, n); fn != nil {
			switch {
			case classifiers[funcKey(fn)]:
				kinds = append(kinds, kindClassify)
				if escapes(stack) {
					kinds = append(kinds, kindDerivedEscape)
				}
			case rawGrantClassifiers[funcKey(fn)]:
				kinds = append(kinds, kindRawGrant)
			}
		}
		kinds = append(kinds, implicitTransports(info, n)...)
	case *ast.CompositeLit:
		kinds = append(kinds, compositeTransports(info, n)...)
	case *ast.AssignStmt:
		for i, rhs := range n.Rhs {
			if i < len(n.Lhs) && len(n.Lhs) == len(n.Rhs) && isClass(info.TypeOf(rhs)) && isInterface(info.TypeOf(n.Lhs[i])) {
				kinds = append(kinds, kindTransport)
			}
		}
	case *ast.SelectorExpr:
		if isMethodValue(info, n, stack) {
			kinds = append(kinds, kindMethodValue)
		}
	case *ast.Ident:
		if parent, ok := parentOf(stack).(*ast.SelectorExpr); ok && parent.Sel == n {
			return kinds
		}
		if isMethodValue(info, n, stack) {
			kinds = append(kinds, kindMethodValue)
		}
		if obj := info.Uses[n]; obj != nil && derived[obj] && escapes(stack) {
			kinds = append(kinds, kindDerivedEscape)
		}
	}
	return kinds
}

// escapes: the expression at the top of stack (a comparison or a classifier
// call, possibly negated or parenthesized) is returned, stored in a field or
// a composite literal.
func escapes(stack []ast.Node) bool {
	child := stack[len(stack)-1]
	for i := len(stack) - 2; i >= 0; i-- {
		switch parent := stack[i].(type) {
		case *ast.ParenExpr:
			child = parent
			continue
		case *ast.UnaryExpr:
			if parent.Op == token.NOT {
				child = parent
				continue
			}
			return false
		case *ast.ReturnStmt:
			return true
		case *ast.KeyValueExpr:
			return parent.Value == child
		case *ast.CompositeLit:
			return true
		case *ast.AssignStmt:
			for j, rhs := range parent.Rhs {
				if rhs == child && j < len(parent.Lhs) {
					switch parent.Lhs[j].(type) {
					case *ast.SelectorExpr, *ast.IndexExpr, *ast.StarExpr:
						return true
					}
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

func parentOf(stack []ast.Node) ast.Node {
	if len(stack) < 2 {
		return nil
	}
	return stack[len(stack)-2]
}

// isMethodValue: expr names a function or method whose signature touches a
// class type, and it is not the callee of a call.
func isMethodValue(info *types.Info, expr ast.Expr, stack []ast.Node) bool {
	var obj types.Object
	switch e := expr.(type) {
	case *ast.Ident:
		obj = info.Uses[e]
	case *ast.SelectorExpr:
		if selection, ok := info.Selections[e]; ok {
			obj = selection.Obj()
		} else {
			obj = info.Uses[e.Sel]
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok || !signatureTouchesClass(fn.Type().(*types.Signature)) {
		return false
	}
	if call, isCall := parentOf(stack).(*ast.CallExpr); isCall && call.Fun == expr {
		return false
	}
	return true
}

func signatureTouchesClass(sig *types.Signature) bool {
	if recv := sig.Recv(); recv != nil && isClass(recv.Type()) {
		return true
	}
	for _, tuple := range []*types.Tuple{sig.Params(), sig.Results()} {
		for i := 0; i < tuple.Len(); i++ {
			if isClass(tuple.At(i).Type()) {
				return true
			}
		}
	}
	return false
}

// implicitTransports: a class-typed argument passed to an interface-typed
// parameter (slog attributes, fmt verbs, eventspec emitters).
func implicitTransports(info *types.Info, call *ast.CallExpr) []string {
	sig, ok := types.Unalias(info.TypeOf(call.Fun)).Underlying().(*types.Signature)
	if !ok {
		return nil
	}
	var kinds []string
	params := sig.Params()
	for i, arg := range call.Args {
		if !isClass(info.TypeOf(arg)) {
			continue
		}
		var paramType types.Type
		switch {
		case sig.Variadic() && i >= params.Len()-1:
			last := params.At(params.Len() - 1).Type()
			if call.Ellipsis.IsValid() {
				paramType = last
			} else if slice, isSlice := last.Underlying().(*types.Slice); isSlice {
				paramType = slice.Elem()
			}
		case i < params.Len():
			paramType = params.At(i).Type()
		}
		if paramType != nil && isInterface(paramType) {
			kinds = append(kinds, kindTransport)
		}
	}
	return kinds
}

// compositeTransports: a class value as an element of a composite literal
// whose element type is an interface (a map[string]any of attributes).
func compositeTransports(info *types.Info, lit *ast.CompositeLit) []string {
	var element types.Type
	switch u := types.Unalias(info.TypeOf(lit)).Underlying().(type) {
	case *types.Map:
		element = u.Elem()
	case *types.Slice:
		element = u.Elem()
	case *types.Array:
		element = u.Elem()
	default:
		return nil
	}
	if !isInterface(element) {
		return nil
	}
	var kinds []string
	for _, elt := range lit.Elts {
		value := elt
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			value = kv.Value
		}
		if isClass(info.TypeOf(value)) {
			kinds = append(kinds, kindTransport)
		}
	}
	return kinds
}

// switchCoverage: whether the tagged switch names every constant of the
// class type, and whether it has a default clause.
func switchCoverage(info *types.Info, stmt *ast.SwitchStmt, named *types.Named) (partial, hasDefault bool) {
	want := map[string]bool{}
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		if c, ok := scope.Lookup(name).(*types.Const); ok && types.Identical(c.Type(), named) {
			want[c.Val().ExactString()] = true
		}
	}
	seen := map[string]bool{}
	for _, clause := range stmt.Body.List {
		cc, ok := clause.(*ast.CaseClause)
		if !ok {
			continue
		}
		if cc.List == nil {
			hasDefault = true
			continue
		}
		for _, expr := range cc.List {
			if tv, ok := info.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
				seen[tv.Value.ExactString()] = true
			}
		}
	}
	for value := range want {
		if !seen[value] {
			return true, hasDefault
		}
	}
	return false, hasDefault
}

func className(t types.Type) *types.Named {
	if t == nil {
		return nil
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return nil
	}
	if classTypes[named.Obj().Pkg().Path()+"."+named.Obj().Name()] {
		return named
	}
	if strings.HasPrefix(named.Obj().Pkg().Path(), fixturesBase+"/") && named.Obj().Name() == "Class" {
		return named
	}
	return nil
}

func isClass(t types.Type) bool { return className(t) != nil }

func isInterface(t types.Type) bool {
	if t == nil {
		return false
	}
	_, ok := types.Unalias(t).Underlying().(*types.Interface)
	return ok
}

func isStringOrInterface(t types.Type) bool {
	if isInterface(t) {
		return true
	}
	basic, ok := types.Unalias(t).(*types.Basic)
	return ok && basic.Kind() == types.String
}

func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, _ := info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if selection, ok := info.Selections[fun]; ok {
			fn, _ := selection.Obj().(*types.Func)
			return fn
		}
		fn, _ := info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}

func funcKey(fn *types.Func) string {
	if fn.Pkg() == nil {
		return fn.Name()
	}
	return fn.Pkg().Path() + "." + fn.Name()
}

func declSymbol(declaration ast.Decl) string {
	switch d := declaration.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) == 1 {
			return receiverName(d.Recv.List[0].Type) + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		if len(d.Specs) > 0 {
			switch spec := d.Specs[0].(type) {
			case *ast.ValueSpec:
				if len(spec.Names) > 0 {
					return "var " + spec.Names[0].Name
				}
			case *ast.TypeSpec:
				return "type " + spec.Name.Name
			}
		}
	}
	return "package-level"
}

func receiverName(expression ast.Expr) string {
	switch e := expression.(type) {
	case *ast.StarExpr:
		return receiverName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	}
	return "?"
}

func compare(found map[site]int, rows map[site]decisionRow) []string {
	var problems []string
	for key, count := range found {
		row, ok := rows[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("UNLISTED site: %s %s %s (x%d)", key.file, key.symbol, key.kind, count))
		case row.count != count:
			problems = append(problems, fmt.Sprintf("COUNT differs: %s %s %s: decisions.tsv %d, code %d", key.file, key.symbol, key.kind, row.count, count))
		case !decisions[row.decision]:
			problems = append(problems, fmt.Sprintf("UNDECIDED site: %s %s %s: decision %q (want r-sub, proj, deny, leave, consent or vocab)", key.file, key.symbol, key.kind, row.decision))
		case strings.TrimSpace(row.note) == "":
			problems = append(problems, fmt.Sprintf("NO NOTE: %s %s %s", key.file, key.symbol, key.kind))
		}
	}
	for key := range rows {
		if _, ok := found[key]; !ok {
			problems = append(problems, fmt.Sprintf("STALE row: %s %s %s", key.file, key.symbol, key.kind))
		}
	}
	sort.Strings(problems)
	return problems
}

func readDecisions(t *testing.T, name string) map[site]decisionRow {
	t.Helper()
	handle, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	rows := map[site]decisionRow{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 6 {
			t.Fatalf("%s:%d: want 6 tab-separated fields (file, symbol, kind, count, decision, note), got %d", name, line, len(fields))
		}
		count, err := strconv.Atoi(fields[3])
		if err != nil || count < 1 {
			t.Fatalf("%s:%d: count %q is not a positive integer", name, line, fields[3])
		}
		key := site{fields[0], fields[1], fields[2]}
		if _, dup := rows[key]; dup {
			t.Fatalf("%s:%d: duplicate row %v", name, line, key)
		}
		rows[key] = decisionRow{count: count, decision: fields[4], note: fields[5]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// dump writes the found sites as decisions.tsv skeleton rows (decision open)
// when GRANTCLASS_DUMP names a file: the starting point for a new row.
func dump(t *testing.T, path string, found map[site]int) {
	t.Helper()
	var lines []string
	for key, count := range found {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%d\topen\t", key.file, key.symbol, key.kind, count))
	}
	sort.Strings(lines)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func outsideBuild(t *testing.T, root string, loaded map[string]bool) []string {
	t.Helper()
	var outside []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !loaded[path] {
			rel, _ := filepath.Rel(root, path)
			outside = append(outside, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(outside)
	return outside
}

func scopeProblems(outside, pinned []string) []string {
	if strings.Join(outside, " ") != strings.Join(pinned, " ") {
		return []string{fmt.Sprintf("SCOPE LIMIT changed: production files outside every release build are %v, pinned %v (add the file with its reason, or move it into a build)", outside, pinned)}
	}
	return nil
}

func releaseTargets() [][2]string {
	seen := map[string]bool{}
	var targets [][2]string
	for _, target := range releasebuild.Matrix() {
		if !seen[target.GOOS] {
			seen[target.GOOS] = true
			targets = append(targets, [2]string{target.GOOS, target.GOARCH})
		}
	}
	return targets
}

func uniqueStrings(values []string) []string {
	var out []string
	for i, value := range values {
		if i == 0 || value != values[i-1] {
			out = append(out, value)
		}
	}
	return out
}
