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
//	switch          a tagged switch over a class value: a decision even when
//	                it names every member and has no default (a class added
//	                with its own case still changes what the switch does)
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
//	transport       a class value converted to any other type: explicitly
//	                (string, an interface, another named type such as
//	                `type Wrapped GrantClass`, another class), or implicitly
//	                to an interface as a call argument, composite element
//	                (slice or array element, map key or value, struct
//	                field), assignment, variable declaration, channel send
//	                or return
//	collection      a composite literal whose element, map key or map value
//	                type is a class ([]GrantClass{...}, map[GrantClass]X,
//	                map[string]GrantClass{...}), and a call argument whose
//	                type is a slice, array or map of a class (the vocabulary
//	                and membership checks such as slices.Contains)
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
// A site inside a function literal is keyed to its enclosing declaration.
//
// decisions.tsv also pins each class type's constant values (a #vocab line).
// When a class gains, loses or renames a constant, the test fails and lists
// every row of that class: each was decided for the old set and is decided
// again, then the pin is updated (CHAOS-12107: a new class without a decision
// at every site turns the gate red, for comparisons as much as for switches).
//
// NOT covered, stated: a class value inside a generic function, where it is a
// type parameter and its comparisons are not class-typed; a named struct that
// carries a class field, transported whole (the struct passed as an
// interface, its field read elsewhere); reflection and unsafe; a decision made by reading RepositoryScopes directly outside the
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
	kindSwitch        = "switch"
	kindSwitchPartial = "switch-partial"
	kindSwitchDefault = "switch-default"
	kindDerivedEscape = "derived-escape"
	kindMethodValue   = "method-value"
	kindTransport     = "transport"
	kindClassify      = "classify"
	kindLookup        = "lookup"
	kindCollection    = "collection"
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

// hit is one site in a declaration and the class type it concerns, keyed
// "<package path>.<type name>".
type hit struct{ kind, class string }

// authGrantClass is the class a raw-grant classifier decides for: it reads
// the grant list the grant class is derived from.
const authGrantClass = modulePath + "/internal/auth.GrantClass"

func TestEveryGrantClassDecisionIsRecorded(t *testing.T) {
	root := repopath.Root(t)
	result := scan(t, root, []string{"./..."})
	if path := os.Getenv("GRANTCLASS_DUMP"); path != "" {
		dump(t, path, result)
	}
	rows, pins := readDecisions(t, decisionsFile)
	problems := append([]string{}, result.problems...)
	problems = append(problems, compare(result.counts, rows)...)
	problems = append(problems, vocabularyProblems(result.vocabulary, pins, result.classes)...)
	problems = append(problems, scopeProblems(outsideBuild(t, root, result.loaded), pinnedOutOfScope)...)
	for _, problem := range problems {
		t.Error(problem)
	}
	if len(problems) > 0 {
		t.Log("fix: give the site a decisions.tsv row (r-sub, proj, deny, leave, consent or vocab) that says what project_only does there; see docs/design/context-fabric-project-only-scope.md")
	}
	t.Logf("grant-class sites: %d, in %d distinct (file, symbol, kind) keys; all decided in decisions.tsv", sum(result.counts), len(result.counts))
}

func sum(counts map[site]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

// scanResult is the union of every release target's scan.
type scanResult struct {
	counts map[site]int
	// classes is the set of class types each site concerns.
	classes map[site]map[string]bool
	// vocabulary is each class type's live constant values, sorted.
	vocabulary map[string][]string
	problems   []string
	loaded     map[string]bool
}

func scan(t *testing.T, root string, patterns []string) scanResult {
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
	out := scanResult{counts: map[site]int{}, classes: map[site]map[string]bool{}, vocabulary: map[string][]string{}, loaded: map[string]bool{}}
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("load: %v", result.err)
		}
		out.problems = append(out.problems, result.problems...)
		for name := range result.loaded {
			out.loaded[name] = true
		}
		for key, count := range result.counts {
			if count > out.counts[key] {
				out.counts[key] = count
			}
		}
		for key, classes := range result.classes {
			if out.classes[key] == nil {
				out.classes[key] = map[string]bool{}
			}
			for class := range classes {
				out.classes[key][class] = true
			}
		}
		for class, values := range result.vocabulary {
			out.vocabulary[class] = values
		}
	}
	sort.Strings(out.problems)
	out.problems = uniqueStrings(out.problems)
	return out
}

type targetScan struct {
	counts     map[site]int
	classes    map[site]map[string]bool
	vocabulary map[string][]string
	problems   []string
	loaded     map[string]bool
	err        error
}

func scanTarget(root string, patterns []string, target [2]string) targetScan {
	loader, err := sourceload.Load(root, patterns, target[0], target[1])
	if err != nil {
		return targetScan{err: err}
	}
	result := targetScan{counts: map[site]int{}, classes: map[site]map[string]bool{}, vocabulary: map[string][]string{}, loaded: map[string]bool{}}
	result.problems = loader.Problems(target[0], target[1])
	for _, pkg := range loader.Roots {
		unit := loader.Units[pkg.ID]
		for _, name := range pkg.GoFiles {
			result.loaded[name] = true
		}
		if unit.Types != nil {
			scope := unit.Types.Scope()
			for _, name := range scope.Names() {
				if typeName, ok := scope.Lookup(name).(*types.TypeName); ok {
					if named := className(typeName.Type()); named != nil && named.Obj() == typeName {
						result.vocabulary[classKey(named)] = classVocabulary(named)
					}
				}
			}
		}
		for _, file := range unit.Files {
			rel, relErr := filepath.Rel(root, loader.Fset.Position(file.Pos()).Filename)
			if relErr != nil {
				return targetScan{err: relErr}
			}
			rel = filepath.ToSlash(rel)
			for _, declaration := range file.Decls {
				symbol := declSymbol(declaration)
				for _, h := range sitesIn(unit.Info, declaration) {
					key := site{rel, symbol, h.kind}
					result.counts[key]++
					if result.classes[key] == nil {
						result.classes[key] = map[string]bool{}
					}
					if h.class != "" {
						result.classes[key][h.class] = true
					}
				}
			}
		}
	}
	return result
}

// sitesIn is every grant-class site in declaration. A site inside a function
// literal belongs to the declaration that encloses it.
func sitesIn(info *types.Info, declaration ast.Decl) []hit {
	derived := derivedLocals(info, declaration)
	var hits []hit
	var stack []ast.Node
	ast.Inspect(declaration, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		hits = append(hits, siteHits(info, stack, derived)...)
		return true
	})
	return hits
}

// derivedLocals is every local variable of declaration assigned a class
// comparison or a classifier result, with the class it was decided over: a
// use of one that escapes is a derived bool (or class) leaving the deciding
// function.
func derivedLocals(info *types.Info, declaration ast.Decl) map[types.Object]string {
	derived := map[types.Object]string{}
	ast.Inspect(declaration, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != len(assign.Rhs) {
			return true
		}
		for i, rhs := range assign.Rhs {
			ident, isIdent := assign.Lhs[i].(*ast.Ident)
			if !isIdent {
				continue
			}
			if class, decided := decisionClass(info, rhs); decided {
				if obj := info.ObjectOf(ident); obj != nil {
					derived[obj] = class
				}
			}
		}
		return true
	})
	return derived
}

// decisionClass: expr is a class comparison or a classifier call, possibly
// negated or parenthesized, and the class it decides over.
func decisionClass(info *types.Info, expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return decisionClass(info, e.X)
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			return decisionClass(info, e.X)
		}
	case *ast.BinaryExpr:
		if e.Op == token.EQL || e.Op == token.NEQ {
			if class := operandClass(info, e); class != "" {
				return class, true
			}
		}
	case *ast.CallExpr:
		if fn := calledFunc(info, e); fn != nil && classifiers[funcKey(fn)] {
			return signatureClass(fn.Type().(*types.Signature)), true
		}
	}
	return "", false
}

// rawGrantClass is the class a raw-grant classifier decides for: the grant
// class (a planted classifier decides for its own package's class).
func rawGrantClass(fn *types.Func) string {
	if fn.Pkg() != nil && strings.HasPrefix(fn.Pkg().Path(), fixturesBase+"/") {
		return fn.Pkg().Path() + ".Class"
	}
	return authGrantClass
}

func operandClass(info *types.Info, e *ast.BinaryExpr) string {
	if class := typeClass(info.TypeOf(e.X)); class != "" {
		return class
	}
	return typeClass(info.TypeOf(e.Y))
}

func siteHits(info *types.Info, stack []ast.Node, derived map[types.Object]string) []hit {
	node := stack[len(stack)-1]
	var hits []hit
	switch n := node.(type) {
	case *ast.IndexExpr:
		if m, ok := types.Unalias(info.TypeOf(n.X)).Underlying().(*types.Map); ok && isClass(m.Key()) {
			hits = append(hits, hit{kindLookup, typeClass(m.Key())})
		}
	case *ast.BinaryExpr:
		if n.Op == token.EQL || n.Op == token.NEQ {
			if class := operandClass(info, n); class != "" {
				hits = append(hits, hit{kindCompare, class})
				if escapes(stack) {
					hits = append(hits, hit{kindDerivedEscape, class})
				}
			}
		}
	case *ast.SwitchStmt:
		if n.Tag != nil {
			if named := className(info.TypeOf(n.Tag)); named != nil {
				class := classKey(named)
				hits = append(hits, hit{kindSwitch, class})
				partial, hasDefault := switchCoverage(info, n, named)
				if partial {
					hits = append(hits, hit{kindSwitchPartial, class})
				}
				if hasDefault {
					hits = append(hits, hit{kindSwitchDefault, class})
				}
			}
		}
	case *ast.CallExpr:
		if tv, ok := info.Types[n.Fun]; ok && tv.IsType() {
			if len(n.Args) == 1 && isClass(info.TypeOf(n.Args[0])) && !types.Identical(tv.Type, info.TypeOf(n.Args[0])) {
				hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(n.Args[0]))})
			}
			return hits
		}
		if fn := calledFunc(info, n); fn != nil {
			switch {
			case classifiers[funcKey(fn)]:
				class := signatureClass(fn.Type().(*types.Signature))
				hits = append(hits, hit{kindClassify, class})
				if escapes(stack) {
					hits = append(hits, hit{kindDerivedEscape, class})
				}
			case rawGrantClassifiers[funcKey(fn)]:
				hits = append(hits, hit{kindRawGrant, rawGrantClass(fn)})
			}
		}
		hits = append(hits, implicitTransports(info, n)...)
		for _, arg := range n.Args {
			if class := collectionClass(info.TypeOf(arg)); class != "" {
				hits = append(hits, hit{kindCollection, class})
			}
		}
	case *ast.CompositeLit:
		if class := collectionClass(info.TypeOf(n)); class != "" {
			hits = append(hits, hit{kindCollection, class})
		}
		hits = append(hits, compositeTransports(info, n)...)
	case *ast.ReturnStmt:
		hits = append(hits, returnTransports(info, n, stack)...)
	case *ast.SendStmt:
		if ch, ok := types.Unalias(info.TypeOf(n.Chan)).Underlying().(*types.Chan); ok && isClass(info.TypeOf(n.Value)) && isInterface(ch.Elem()) {
			hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(n.Value))})
		}
	case *ast.AssignStmt:
		for i, rhs := range n.Rhs {
			if i < len(n.Lhs) && len(n.Lhs) == len(n.Rhs) && isClass(info.TypeOf(rhs)) && isInterface(info.TypeOf(n.Lhs[i])) {
				hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(rhs))})
			}
		}
	case *ast.ValueSpec:
		for i, value := range n.Values {
			if i >= len(n.Names) || !isClass(info.TypeOf(value)) {
				continue
			}
			if obj := info.Defs[n.Names[i]]; obj != nil && isInterface(obj.Type()) {
				hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(value))})
			}
		}
	case *ast.SelectorExpr:
		if class := methodValueClass(info, n, stack); class != "" {
			hits = append(hits, hit{kindMethodValue, class})
		}
	case *ast.Ident:
		if parent, ok := parentOf(stack).(*ast.SelectorExpr); ok && parent.Sel == n {
			return hits
		}
		if class := methodValueClass(info, n, stack); class != "" {
			hits = append(hits, hit{kindMethodValue, class})
		}
		if obj := info.Uses[n]; obj != nil {
			if class, ok := derived[obj]; ok && escapes(stack) {
				hits = append(hits, hit{kindDerivedEscape, class})
			}
		}
	}
	return hits
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

// methodValueClass: expr names a function or method whose signature touches
// a class type, and it is not the callee of a call; the class it touches.
func methodValueClass(info *types.Info, expr ast.Expr, stack []ast.Node) string {
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
	if !ok {
		return ""
	}
	class := signatureClass(fn.Type().(*types.Signature))
	if class == "" {
		return ""
	}
	if call, isCall := parentOf(stack).(*ast.CallExpr); isCall && call.Fun == expr {
		return ""
	}
	return class
}

// signatureClass is the first class type a signature touches: its results,
// then its receiver, then its parameters ("" when none).
func signatureClass(sig *types.Signature) string {
	for i := 0; i < sig.Results().Len(); i++ {
		if class := typeClass(sig.Results().At(i).Type()); class != "" {
			return class
		}
	}
	if recv := sig.Recv(); recv != nil {
		if class := typeClass(recv.Type()); class != "" {
			return class
		}
	}
	for i := 0; i < sig.Params().Len(); i++ {
		if class := typeClass(sig.Params().At(i).Type()); class != "" {
			return class
		}
	}
	return ""
}

// implicitTransports: a class-typed argument passed to an interface-typed
// parameter (slog attributes, fmt verbs, eventspec emitters).
func implicitTransports(info *types.Info, call *ast.CallExpr) []hit {
	sig, ok := types.Unalias(info.TypeOf(call.Fun)).Underlying().(*types.Signature)
	if !ok {
		return nil
	}
	var hits []hit
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
			hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(arg))})
		}
	}
	return hits
}

// compositeTransports: a class value stored as an interface in a composite
// literal: a slice or array element, a map key or value, or a struct field of
// interface type (a map[string]any of attributes, a map[any]int).
func compositeTransports(info *types.Info, lit *ast.CompositeLit) []hit {
	var hits []hit
	add := func(slot types.Type, value ast.Expr) {
		if slot != nil && isInterface(slot) && isClass(info.TypeOf(value)) {
			hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(value))})
		}
	}
	switch u := types.Unalias(info.TypeOf(lit)).Underlying().(type) {
	case *types.Struct:
		for i, elt := range lit.Elts {
			value, field := elt, (*types.Var)(nil)
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				value = kv.Value
				if key, isIdent := kv.Key.(*ast.Ident); isIdent {
					field, _ = info.Uses[key].(*types.Var)
					if field == nil {
						field, _ = info.ObjectOf(key).(*types.Var)
					}
				}
			} else if i < u.NumFields() {
				field = u.Field(i)
			}
			if field != nil {
				add(field.Type(), value)
			}
		}
	case *types.Map:
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				add(u.Key(), kv.Key)
				add(u.Elem(), kv.Value)
			}
		}
	case *types.Slice:
		for _, elt := range lit.Elts {
			add(u.Elem(), elementValue(elt))
		}
	case *types.Array:
		for _, elt := range lit.Elts {
			add(u.Elem(), elementValue(elt))
		}
	}
	return hits
}

func elementValue(elt ast.Expr) ast.Expr {
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		return kv.Value
	}
	return elt
}

// returnTransports: a class value returned where the enclosing function's
// result type is an interface (any, error, fmt.Stringer).
func returnTransports(info *types.Info, ret *ast.ReturnStmt, stack []ast.Node) []hit {
	var sig *types.Signature
	for i := len(stack) - 2; i >= 0 && sig == nil; i-- {
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			sig, _ = info.TypeOf(fn).(*types.Signature)
		case *ast.FuncDecl:
			if obj, ok := info.Defs[fn.Name].(*types.Func); ok {
				sig, _ = obj.Type().(*types.Signature)
			}
		}
	}
	if sig == nil || sig.Results().Len() != len(ret.Results) {
		return nil
	}
	var hits []hit
	for i, result := range ret.Results {
		if isClass(info.TypeOf(result)) && isInterface(sig.Results().At(i).Type()) {
			hits = append(hits, hit{kindTransport, typeClass(info.TypeOf(result))})
		}
	}
	return hits
}

// collectionClass: the class of a slice, array or map whose element, key or
// value type is a class ("" when none).
func collectionClass(t types.Type) string {
	if t == nil {
		return ""
	}
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Slice:
		return typeClass(u.Elem())
	case *types.Array:
		return typeClass(u.Elem())
	case *types.Map:
		if class := typeClass(u.Key()); class != "" {
			return class
		}
		return typeClass(u.Elem())
	}
	return ""
}

// switchCoverage: whether the tagged switch names every constant of the
// class type, and whether it has a default clause.
func switchCoverage(info *types.Info, stmt *ast.SwitchStmt, named *types.Named) (partial, hasDefault bool) {
	want := map[string]bool{}
	for _, value := range classVocabulary(named) {
		want[value] = true
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
				seen[constant.StringVal(tv.Value)] = true
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

// classKey is "<package path>.<type name>".
func classKey(named *types.Named) string {
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}

// typeClass is the class key of t ("" when t is not a class type).
func typeClass(t types.Type) string {
	if named := className(t); named != nil {
		return classKey(named)
	}
	return ""
}

// classVocabulary is the sorted string values of the constants of the class
// type declared in its own package.
func classVocabulary(named *types.Named) []string {
	var values []string
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		if c, ok := scope.Lookup(name).(*types.Const); ok && types.Identical(c.Type(), named) && c.Val().Kind() == constant.String {
			values = append(values, constant.StringVal(c.Val()))
		}
	}
	sort.Strings(values)
	return values
}

func isInterface(t types.Type) bool {
	if t == nil {
		return false
	}
	_, ok := types.Unalias(t).Underlying().(*types.Interface)
	return ok
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

// vocabularyPinPrefix starts a decisions.tsv line that pins one class
// type's constant values: #vocab<TAB><package path>.<type><TAB><sorted
// values, comma-separated>.
const vocabularyPinPrefix = "#vocab\t"

func readDecisions(t *testing.T, name string) (map[site]decisionRow, map[string][]string) {
	t.Helper()
	handle, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	rows := map[site]decisionRow{}
	pins := map[string][]string{}
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if strings.HasPrefix(text, vocabularyPinPrefix) {
			fields := strings.Split(text, "\t")
			if len(fields) != 3 || fields[1] == "" || fields[2] == "" {
				t.Fatalf("%s:%d: a vocabulary pin is #vocab<TAB>type<TAB>values", name, line)
			}
			if _, dup := pins[fields[1]]; dup {
				t.Fatalf("%s:%d: duplicate vocabulary pin %s", name, line, fields[1])
			}
			pins[fields[1]] = strings.Split(fields[2], ",")
			continue
		}
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
	return rows, pins
}

// vocabularyProblems compares each class type's live constant set with its
// pin. A class that gained, lost or renamed a constant has decisions that
// were taken for the old set: every site of that type is listed to be
// decided again (CHAOS-12107: a new class without a decision at every site
// turns the gate red), and the pin is updated after.
func vocabularyProblems(live, pins map[string][]string, classes map[site]map[string]bool) []string {
	var problems []string
	for class, values := range live {
		pin, ok := pins[class]
		if ok && strings.Join(pin, ",") == strings.Join(values, ",") {
			continue
		}
		var rows []string
		for key, siteClasses := range classes {
			if siteClasses[class] {
				rows = append(rows, fmt.Sprintf("%s %s %s", key.file, key.symbol, key.kind))
			}
		}
		sort.Strings(rows)
		if !ok {
			problems = append(problems, fmt.Sprintf("NO VOCABULARY PIN: %s has %s; add %s%s\t%s, and decide every row of it: %s", class, strings.Join(values, ","), vocabularyPinPrefix, class, strings.Join(values, ","), strings.Join(rows, "; ")))
			continue
		}
		problems = append(problems, fmt.Sprintf("class vocabulary changed: %s pinned %s, live %s: re-decide these rows, then update the pin: %s", class, strings.Join(pin, ","), strings.Join(values, ","), strings.Join(rows, "; ")))
	}
	for class := range pins {
		if _, ok := live[class]; !ok {
			problems = append(problems, fmt.Sprintf("STALE vocabulary pin: %s is no longer a loaded class type", class))
		}
	}
	sort.Strings(problems)
	return problems
}

// dump writes the found sites as decisions.tsv skeleton rows (decision open)
// when GRANTCLASS_DUMP names a file: the starting point for a new row.
func dump(t *testing.T, path string, result scanResult) {
	t.Helper()
	var pins, lines []string
	for class, values := range result.vocabulary {
		pins = append(pins, vocabularyPinPrefix+class+"\t"+strings.Join(values, ","))
	}
	sort.Strings(pins)
	for key, count := range result.counts {
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%d\topen\t", key.file, key.symbol, key.kind, count))
	}
	sort.Strings(lines)
	lines = append(pins, lines...)
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

// releaseTargets is every distinct (GOOS, GOARCH) pair of the release
// matrix: a file built only for one architecture (an arm64 build tag) is
// scanned too.
func releaseTargets() [][2]string {
	seen := map[[2]string]bool{}
	var targets [][2]string
	for _, target := range releasebuild.Matrix() {
		pair := [2]string{target.GOOS, target.GOARCH}
		if !seen[pair] {
			seen[pair] = true
			targets = append(targets, pair)
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
