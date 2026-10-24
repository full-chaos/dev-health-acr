// Package rowsclose is the static guard that every ClickHouse query result is closed.
//
// A row type is any interface or named type whose method set has Next() bool, Scan(...any) error and Close() error
// (dev-health-go's clickhouse.RowScanner, contextpacket.ClickHouseRowScanner, readers.RowScanner). database/sql rows are
// out of scope: the Postgres stores close them by hand before the next statement of a transaction.
// A site is a call whose first result is a row type. The rule for an assigned result `rows, err := x.Query(...)` in a
// block: the statement after the assignment may be the `if err != nil { ... }` check, and the statement after that must
// be `defer rows.Close()` or a `defer func() { ...; rows.Close(); ... }()` with the Close a top-level statement of the closure or the init of a top-level `if`
// (a Close inside an if body, loop or switch can be skipped, so it is close-not-deferred). A statement before the
// Close that can return, goto or panic makes the closure not count either. A site breaks the rule as one kind:
//
//	no-close            the result is discarded (blank or expression statement), or rows.Close is never called
//	close-not-deferred  rows.Close is called but never deferred
//	return-before-defer a deferred rows.Close exists in the function but not at the position the rule requires, so a
//	                    statement between the Query and the defer can leave without closing
//	rows-escapes        the row value is returned, stored in a composite literal, assigned to another variable, sent on
//	                    a channel, captured by a go statement, or the call is not assigned at all (returned directly or
//	                    nested in an argument): the closer is another function, and the allow row names it
//
// Every other site needs no row. An exception needs a row in allow.tsv (file, symbol, kind, count, reason). An unlisted
// site, a count that differs and a row that matches nothing FAIL.
//
// The rule assumes the Go contract that a Query returning a non-nil error returns no rows to close: the `if err != nil`
// exit before the defer is accepted, and a callee that returns rows together with an error is not judged here.
//
// NOT covered, stated: a Close reached only through another interface or helper (it is a no-close site with an allow
// row); path-sensitivity beyond the rule; the dependencies' own code outside the module; a production file outside every
// release build (pinned by name, the test fails when that set changes).
package rowsclose

import (
	"bufio"
	"fmt"
	"go/ast"
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
	modulePath   = "github.com/full-chaos/dev-health-acr"
	fixturesBase = modulePath + "/internal/cigates/rowsclose/testdata/planted"
	allowFile    = "allow.tsv"
)

var pinnedOutOfScope = []string{
	"cmd/acr-mcp/lifecycle_compiled_fixture.go",
	"internal/contracts/v1/answer_bound_race.go",
	"internal/sidecar/credential_keyring_compiled_lifecycle_fixture.go",
	"internal/sidecar/credential_lifecycle_lock_compiled_fixture_unix.go",
	"internal/sidecar/credential_lifecycle_lock_compiled_fixture_unsupported.go",
}

const (
	kindNoClose          = "no-close"
	kindCloseNotDeferred = "close-not-deferred"
	kindReturnBefore     = "return-before-defer"
	kindEscapes          = "rows-escapes"
)

type site struct{ file, symbol, kind string }

type allowRow struct {
	count  int
	reason string
}

func TestEveryClickHouseQueryResultIsClosed(t *testing.T) {
	root := repopath.Root(t)
	found, problems, loaded := scan(t, root, []string{"./..."})
	problems = append(problems, compare(found, readAllow(t, allowFile))...)
	problems = append(problems, scopeProblems(outsideBuild(t, root, loaded), pinnedOutOfScope)...)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(problems) > 0 {
		t.Log("fix: `defer rows.Close()` right after the err check that follows the Query; a deliberate hand-off gets an allow.tsv row naming who closes")
	}
	t.Logf("exception sites: %d, in %d distinct (file, symbol, kind) keys; all listed in allow.tsv", sum(found), len(found))
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

// sitesIn is the violation kind of every row-producing call in declaration; a site that follows the rule is absent.
func sitesIn(info *types.Info, declaration ast.Decl) []string {
	var kinds []string
	var stack []ast.Node
	ast.Inspect(declaration, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		call, ok := node.(*ast.CallExpr)
		if !ok || !producesRows(info, call) {
			return true
		}
		if kind := judge(info, stack); kind != "" {
			kinds = append(kinds, kind)
		}
		return true
	})
	return kinds
}

// producesRows: the first result of the call is a row type.
func producesRows(info *types.Info, call *ast.CallExpr) bool {
	tv, ok := info.Types[call]
	if !ok || tv.IsType() {
		return false
	}
	t := tv.Type
	if tuple, isTuple := t.(*types.Tuple); isTuple {
		if tuple.Len() == 0 {
			return false
		}
		t = tuple.At(0).Type()
	}
	return isRowType(t)
}

func isRowType(t types.Type) bool {
	if t == nil {
		return false
	}
	if named, ok := types.Unalias(t).(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "database/sql" {
		return false
	}
	if pointer, ok := t.(*types.Pointer); ok {
		if named, isNamed := pointer.Elem().(*types.Named); isNamed && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "database/sql" {
			return false
		}
	}
	set := types.NewMethodSet(t)
	if _, isPointer := t.(*types.Pointer); !isPointer {
		if _, isInterface := t.Underlying().(*types.Interface); !isInterface {
			set = types.NewMethodSet(types.NewPointer(t))
		}
	}
	return hasMethod(set, "Next", 0, "bool") && hasMethod(set, "Scan", -1, "error") && hasMethod(set, "Close", 0, "error")
}

func hasMethod(set *types.MethodSet, name string, params int, result string) bool {
	for i := 0; i < set.Len(); i++ {
		fn, ok := set.At(i).Obj().(*types.Func)
		if !ok || fn.Name() != name {
			continue
		}
		sig := fn.Type().(*types.Signature)
		if params >= 0 && sig.Params().Len() != params {
			return false
		}
		return sig.Results().Len() == 1 && sig.Results().At(0).Type().String() == result
	}
	return false
}

// judge is the violation kind of the call at the top of stack, or "" when it follows the rule.
func judge(info *types.Info, stack []ast.Node) string {
	call := stack[len(stack)-1].(*ast.CallExpr)
	if len(stack) < 2 {
		return kindEscapes
	}
	parent := stack[len(stack)-2]
	switch p := parent.(type) {
	case *ast.ExprStmt:
		return kindNoClose
	case *ast.AssignStmt:
		if len(p.Rhs) != 1 || p.Rhs[0] != call || len(stack) < 3 {
			return kindEscapes
		}
		return judgeAssigned(info, stack[:len(stack)-1], p, p.Lhs)
	case *ast.ValueSpec:
		if len(p.Values) != 1 || len(stack) < 4 {
			return kindEscapes
		}
		decl, ok := stack[len(stack)-3].(*ast.GenDecl)
		if !ok {
			return kindEscapes
		}
		block, ok := stack[len(stack)-4].(*ast.DeclStmt)
		if !ok || block.Decl != decl {
			return kindEscapes
		}
		names := make([]ast.Expr, len(p.Names))
		for i, name := range p.Names {
			names[i] = name
		}
		return judgeAssigned(info, stack[:len(stack)-3], block, names)
	}
	return kindEscapes
}

// judgeAssigned: statement holds the call; stack ends at statement and names the blocks around it.
func judgeAssigned(info *types.Info, stack []ast.Node, statement ast.Stmt, targets []ast.Expr) string {
	ident, ok := targets[0].(*ast.Ident)
	if !ok {
		return kindEscapes
	}
	if ident.Name == "_" {
		return kindNoClose
	}
	obj := info.ObjectOf(ident)
	if obj == nil {
		return kindNoClose
	}
	var list []ast.Stmt
	switch b := stack[len(stack)-2].(type) {
	case *ast.BlockStmt:
		list = b.List
	case *ast.CaseClause:
		list = b.Body
	case *ast.CommClause:
		list = b.Body
	default:
		return kindEscapes
	}
	at := -1
	for i, s := range list {
		if s == statement {
			at = i
		}
	}
	if at < 0 {
		return kindEscapes
	}
	next := at + 1
	if next < len(list) && isErrCheck(info, list[next], targets) {
		next++
	}
	if next < len(list) && closesInDefer(info, list[next], obj) {
		return ""
	}
	rest := list[at+1:]
	if escapes(info, rest, obj) {
		return kindEscapes
	}
	deferred, closed := false, false
	for _, s := range rest {
		d, c := closeUses(info, s, obj)
		deferred = deferred || d
		closed = closed || c
	}
	switch {
	case deferred:
		return kindReturnBefore
	case closed:
		return kindCloseNotDeferred
	}
	return kindNoClose
}

// isErrCheck: `if err != nil { ... }` on an error variable the Query assignment itself assigns (not an earlier one), with
// no init statement.
func isErrCheck(info *types.Info, candidate ast.Stmt, targets []ast.Expr) bool {
	stmt, ok := candidate.(*ast.IfStmt)
	if !ok || stmt.Init != nil || stmt.Else != nil {
		return false
	}
	binary, ok := stmt.Cond.(*ast.BinaryExpr)
	if !ok || binary.Op != token.NEQ {
		return false
	}
	name, ok := binary.X.(*ast.Ident)
	if !ok {
		return false
	}
	if nilIdent, isIdent := binary.Y.(*ast.Ident); !isIdent || nilIdent.Name != "nil" {
		return false
	}
	obj := info.ObjectOf(name)
	if obj == nil {
		return false
	}
	for _, target := range targets[1:] {
		if id, isIdent := target.(*ast.Ident); isIdent && info.ObjectOf(id) == obj {
			return true
		}
	}
	return false
}

func isCloseOf(info *types.Info, call *ast.CallExpr, obj types.Object) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Close" {
		return false
	}
	id, ok := selector.X.(*ast.Ident)
	return ok && info.ObjectOf(id) == obj
}

// closesInDefer: `defer rows.Close()` or `defer func() { ...; rows.Close(); ... }()` with the Close a top-level statement of the closure or the init of a top-level `if`
// (a Close inside an if body, loop or switch can be skipped, so it is close-not-deferred). A statement before the
// Close that can return, goto or panic makes the closure not count either.
func closesInDefer(info *types.Info, candidate ast.Stmt, obj types.Object) bool {
	stmt, ok := candidate.(*ast.DeferStmt)
	if !ok {
		return false
	}
	if isCloseOf(info, stmt.Call, obj) {
		return true
	}
	lit, ok := stmt.Call.Fun.(*ast.FuncLit)
	if !ok {
		return false
	}
	for _, inner := range lit.Body.List {
		if canLeave(info, inner) {
			return false
		}
		if ifStmt, isIf := inner.(*ast.IfStmt); isIf && ifStmt.Init != nil {
			inner = ifStmt.Init
		}
		if runsClose(info, inner, obj) {
			return true
		}
	}
	return false
}

// canLeave: the statement can return from the closure, jump away or panic before a later Close runs. A function literal that is only defined (stored,
// passed) runs later or never and is not entered; one that is called on the spot, or started with go or defer, is.
func canLeave(info *types.Info, statement ast.Stmt) bool {
	leaves := false
	invoked := map[*ast.FuncLit]bool{}
	ast.Inspect(statement, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncLit:
			return invoked[n]
		case *ast.ReturnStmt:
			leaves = true
		case *ast.BranchStmt:
			if n.Tok == token.GOTO {
				leaves = true
			}
		case *ast.CallExpr:
			if lit, isLit := n.Fun.(*ast.FuncLit); isLit {
				invoked[lit] = true
			}
			if isBuiltinCall(info, n) && n.Fun.(*ast.Ident).Name == "panic" {
				leaves = true
			}
			if fn := calledFunc(info, n); fn != nil && fn.Pkg() != nil && fn.Pkg().Path() == "runtime" && fn.Name() == "Goexit" {
				leaves = true
			}
		}
		return !leaves
	})
	return leaves
}

func isBuiltinCall(info *types.Info, call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	_, builtin := info.Uses[ident].(*types.Builtin)
	return builtin
}

func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, _ := info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		fn, _ := info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}

// runsClose: the statement itself calls obj.Close (`rows.Close()`, `_ = rows.Close()`, `err = rows.Close()`).
func runsClose(info *types.Info, statement ast.Stmt, obj types.Object) bool {
	switch n := statement.(type) {
	case *ast.ExprStmt:
		call, isCall := n.X.(*ast.CallExpr)
		return isCall && isCloseOf(info, call, obj)
	case *ast.AssignStmt:
		if len(n.Rhs) == 1 {
			call, isCall := n.Rhs[0].(*ast.CallExpr)
			return isCall && isCloseOf(info, call, obj)
		}
	}
	return false
}

// closeUses: whether a defer of obj.Close, and whether any obj.Close call, occurs inside statement.
func closeUses(info *types.Info, statement ast.Stmt, obj types.Object) (deferred, closed bool) {
	ast.Inspect(statement, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.DeferStmt:
			if closesInDefer(info, n, obj) {
				deferred = true
			}
		case *ast.CallExpr:
			if isCloseOf(info, n, obj) {
				closed = true
			}
		}
		return true
	})
	return deferred, closed
}

func mentions(info *types.Info, root ast.Node, obj types.Object) bool {
	found := false
	ast.Inspect(root, func(node ast.Node) bool {
		if id, ok := node.(*ast.Ident); ok && info.ObjectOf(id) == obj {
			found = true
		}
		return !found
	})
	return found
}

// usedAsValue: obj appears in root other than as the receiver of a method call or field selection (rows.Close() reads
// it; wrap(rows) hands it on).
func usedAsValue(info *types.Info, root ast.Node, obj types.Object) bool {
	found := false
	ast.Inspect(root, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && info.ObjectOf(id) == obj {
				return false
			}
		case *ast.Ident:
			if info.ObjectOf(n) == obj {
				found = true
			}
		}
		return !found
	})
	return found
}

// escapes: the value of obj leaves the function's own scope of control in the statements after the Query.
func escapes(info *types.Info, statements []ast.Stmt, obj types.Object) bool {
	out := false
	for _, statement := range statements {
		ast.Inspect(statement, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.ReturnStmt:
				for _, result := range n.Results {
					if usedAsValue(info, result, obj) {
						out = true
					}
				}
			case *ast.CompositeLit:
				for _, element := range n.Elts {
					if kv, ok := element.(*ast.KeyValueExpr); ok {
						element = kv.Value
					}
					if id, ok := element.(*ast.Ident); ok && info.ObjectOf(id) == obj {
						out = true
					}
				}
			case *ast.AssignStmt:
				for _, right := range n.Rhs {
					if id, ok := right.(*ast.Ident); ok && info.ObjectOf(id) == obj {
						out = true
					}
				}
			case *ast.SendStmt:
				if id, ok := n.Value.(*ast.Ident); ok && info.ObjectOf(id) == obj {
					out = true
				}
			case *ast.GoStmt:
				if mentions(info, n, obj) {
					out = true
				}
			}
			return !out
		})
	}
	return out
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
	}
	return "?"
}

func compare(found map[site]int, rows map[site]allowRow) []string {
	var problems []string
	for key, count := range found {
		row, ok := rows[key]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("UNLISTED site: %s %s %s (x%d)", key.file, key.symbol, key.kind, count))
		case row.count != count:
			problems = append(problems, fmt.Sprintf("COUNT differs: %s %s %s: allow.tsv %d, code %d", key.file, key.symbol, key.kind, row.count, count))
		case strings.TrimSpace(row.reason) == "":
			problems = append(problems, fmt.Sprintf("NO REASON: %s %s %s", key.file, key.symbol, key.kind))
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

func readAllow(t *testing.T, name string) map[site]allowRow {
	t.Helper()
	handle, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	rows := map[site]allowRow{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 5 {
			t.Fatalf("%s:%d: want 5 tab-separated fields (file, symbol, kind, count, reason), got %d", name, line, len(fields))
		}
		count, err := strconv.Atoi(fields[3])
		if err != nil || count < 1 {
			t.Fatalf("%s:%d: count %q is not a positive integer", name, line, fields[3])
		}
		key := site{fields[0], fields[1], fields[2]}
		if _, dup := rows[key]; dup {
			t.Fatalf("%s:%d: duplicate row %v", name, line, key)
		}
		rows[key] = allowRow{count: count, reason: fields[4]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
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
