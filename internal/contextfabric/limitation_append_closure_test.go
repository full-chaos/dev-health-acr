package contextfabric

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"
)

// auditedLimitationWrites are the raw writes to a limitations-destined value
// that are NOT the bounded appender's output, each with the reason it is
// safe.
//
// Two-sided, like the sidecar's body-read audit: an unlisted raw write
// fails, and a listed entry matching nothing fails too.
//
// Keyed by enclosing function, what the write is fed from, and WHICH
// occurrence of that shape it is -- the trailing index is what makes an
// entry cover one site rather than every write that resembles it. Edits
// elsewhere in the file do not churn these keys; see the matching loop for
// why an index beats a line number or an offset.
//
// This list is meant to stay SHORT. Every entry is a place the cap is not
// mechanically enforced, i.e. a place round-17 finding 1 could recur.
type limitationAudit struct {
	// sameShapedTotal is how many writes of this exact function#value shape
	// existed in that function when the exemption was written. Recorded so
	// the POPULATION is pinned, not just this member of it: renumbering
	// only happens when that population changes, and a changed population
	// is exactly when a human should look again.
	//
	// Together with the content now carried in the key, this closes the
	// inheritance route in both directions. Add a same-shaped write and the
	// total moves, so the entry is named alongside the new unaudited site;
	// remove the audited one and the total moves again, or the entry
	// matches nothing at all.
	//
	// ONE residual, stated rather than papered over: a SINGLE edit that
	// deletes the audited write and adds a TEXTUALLY IDENTICAL one
	// elsewhere in the same function restores the total, and the survivor
	// inherits the exemption silently. That is tolerable only because the
	// two writes are the same expression -- the recorded reason is as true
	// of one as of the other. It stops being tolerable if the reason ever
	// depends on WHERE the write sits rather than what it is, so a
	// position-dependent reason does not belong in this list.
	sameShapedTotal int
	reason          string
}

var auditedLimitationWrites = map[string]limitationAudit{
	"terminalResult#composite literal []string{limitation}#0": {
		sameShapedTotal: 1,
		reason:          "the SEED, not an addition: a one-element list holding the single fixed terminal disclosure resolveTerminalStatus chose. Every list has to start somewhere, and everything added after it goes through the bounded appender, which also normalizes an already-over-cap input -- so the seed cannot be the write that overflows the contract",
	},
	"Synthesize#cloneSlice#0": {
		sameShapedTotal: 1,
		reason:          "an INTERMEDIATE, not a list that reaches a consumer: this is the model's own draft list entering the synthesized result, and Investigate then passes result.Limitations through appendTemporalLimitations UNCONDITIONALLY -- it is called on every axis, current included, and appendBoundedLimitations normalizes an already-over-cap input -- before Validate runs",
	},
	"windowVetoResult#composite literal []string{limitation}#0": {
		sameShapedTotal: 1,
		reason:          "the SEED, not an addition: a one-element list holding the single fixed window-veto disclosure windowVetoLimitation chose (CHAOS-3900 W1) -- identical shape and reasoning to terminalResult's own seed write above. Nothing is ever appended to this list afterward: a window-veto result is composed once and saved, never passed through a further limitation appender",
	},
}

// boundedLimitationPrimitive owns the cap. It is the only function allowed
// to append to a limitations list without itself going through something
// else, and it is exempt from the wrapper body check below for that reason.
const boundedLimitationPrimitive = "appendBoundedLimitations"

// boundedLimitationWrappers are the narrower names the bounded path travels
// under. Membership is NOT taken on trust: verifyBoundedWrappers proves each
// body is nothing but one call to the primitive (codex round-5 F1c). A
// wrapper that grew a raw append after its bounded call would otherwise
// launder that append behind a whitelisted name.
var boundedLimitationWrappers = map[string]bool{
	"appendTemporalLimitations": true,
	"withRetrievalDegradation":  true,
}

func isBoundedLimitationSource(name string) bool {
	return name == boundedLimitationPrimitive || boundedLimitationWrappers[name]
}

// TestEveryLimitationAppendIsBounded closes the CLASS behind round-17
// finding 1 rather than the site.
//
// The cap was handled where the degradation disclosure was appended and
// nowhere else, so CHAOS-3781's historical disclosures were appended on top
// of a full list and the whole investigation died at validation. That is not
// a bug in either appender; it is what happens when "the cap" lives at a
// call site instead of in one function every append has to go through.
//
// The shape it pins: inside this package, a list that reaches a result's
// Limitations must have been produced by the bounded appender, and every
// write to any local that flows there must be the appender's output too.
//
// It is deliberately CONSERVATIVE (codex round-5 F1). An earlier version
// tried to be exact -- it followed a local to its LEXICALLY latest write and
// judged only that one. Three ways that was wrong: lexical order is not
// execution order, so a raw append inside an if-branch was excused by a
// bounded write below it; the search ran over the whole file rather than the
// enclosing function, so a same-named local in another function could answer
// for this one; and wrapper names were trusted without reading their bodies.
//
// So it no longer decides which write "wins". ANY raw write to a
// limitations-destined local is a violation, wherever it sits in the
// function. Over-approximation is the point: a legitimate pattern flagged
// here becomes a visible, two-sided audit entry with a stated reason, which
// is the mechanism working rather than failing.
func TestEveryLimitationAppendIsBounded(t *testing.T) {
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(fileSet, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package directory: %v", err)
	}

	var (
		writes       []limitationWrite
		sawPrimitive bool
		functions    = map[string]*ast.FuncDecl{}
	)
	for _, pkg := range packages {
		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				functions[function.Name.Name] = function
				if callsFunction(function.Body, boundedLimitationPrimitive) {
					sawPrimitive = true
				}
				writes = append(writes, limitationWritesIn(t, fileSet, function)...)
			}
		}
	}

	if !sawPrimitive {
		t.Fatalf("found no call to %s at all; the walker is not reaching the engine and would pass over any unbounded append", boundedLimitationPrimitive)
	}
	if len(writes) == 0 {
		t.Fatal("found no Limitations write at all; the walker is not reaching the composition code")
	}

	verifyBoundedWrappers(t, functions)

	// Source order, so the occurrence index below is deterministic:
	// ParseDir hands back files in map order, and writes are gathered
	// field-side before local-side within a function.
	sort.Slice(writes, func(i, j int) bool {
		left, right := fileSet.Position(writes[i].pos), fileSet.Position(writes[j].pos)
		if left.Filename != right.Filename {
			return left.Filename < right.Filename
		}
		return left.Offset < right.Offset
	})

	// How many writes share each shape, counted before any matching.
	sameShapedTotals := map[string]int{}
	for _, w := range writes {
		if !isBoundedLimitationSource(w.value) {
			sameShapedTotals[w.function+"#"+w.value]++
		}
	}

	matched := map[string]bool{}
	occurrence := map[string]int{}
	for _, w := range writes {
		if isBoundedLimitationSource(w.value) {
			continue
		}
		// SITE-UNIQUE (codex round-9 P1). function#value alone matched
		// every write that happened to share a shape: the audited seed
		// exempted a second raw []string literal in the same function, and
		// the position was captured for the message but thrown away at
		// matching time. The trailing index is which occurrence of that
		// shape this is, so an exemption can never cover more than the one
		// site it was written for.
		//
		// An index rather than a line number or a byte offset because it is
		// what survives edits ELSEWHERE: adding a comment, a statement, or
		// a whole function above the site shifts every line and offset in
		// the file but leaves the occurrence count untouched. Only adding
		// another write OF THE SAME SHAPE to the same function renumbers
		// anything, and that is precisely when the exemption should be
		// re-examined rather than silently carried.
		//
		// The index alone still let an exemption be INHERITED (codex
		// round-10 P1): delete the audited write and a later same-shaped
		// one renumbers into its place, matching the entry it was never
		// written for, while the stale-entry check stays quiet because a
		// same-shaped write does still exist. So an entry also pins the
		// POPULATION it was written against. Either direction of change --
		// one added, one removed -- moves the total and forces the entry to
		// be re-examined rather than silently carried onto a different
		// site.
		base := w.function + "#" + w.value
		key := fmt.Sprintf("%s#%d", base, occurrence[base])
		occurrence[base]++
		audit, audited := auditedLimitationWrites[key]
		if !audited {
			t.Errorf("%s writes a limitations-destined value from %q, which is not the bounded appender; route it through %s or add %q to auditedLimitationWrites with the reason it is already bounded (recording sameShapedTotal: %d)",
				w.position, w.value, boundedLimitationPrimitive, key, sameShapedTotals[base])
			continue
		}
		matched[key] = true
		if audit.sameShapedTotal != sameShapedTotals[base] {
			t.Errorf("%s: %q was audited when %d write(s) of that shape existed in %s, and there are now %d; the exemption may have been inherited by a different site, so re-examine it and update sameShapedTotal deliberately",
				w.position, key, audit.sameShapedTotal, w.function, sameShapedTotals[base])
		}
	}
	for key := range auditedLimitationWrites {
		if !matched[key] {
			t.Errorf("auditedLimitationWrites lists %q, which matches no limitations write; remove it rather than leaving an exemption that describes nothing", key)
		}
	}
}

// verifyBoundedWrappers proves each whitelisted wrapper is what its
// membership claims: a body that does nothing but call the primitive once.
//
// Without this, the whitelist is a promise about names. A wrapper that
// appended its own disclosure after the bounded call would still be trusted
// everywhere it appears, which would launder exactly the write this test
// exists to catch.
func verifyBoundedWrappers(t *testing.T, functions map[string]*ast.FuncDecl) {
	t.Helper()
	for name := range boundedLimitationWrappers {
		function, ok := functions[name]
		if !ok {
			t.Errorf("boundedLimitationWrappers lists %q, which is not a function in this package; remove it rather than whitelisting a name nothing answers to", name)
			continue
		}
		if calls := countCalls(function.Body, boundedLimitationPrimitive); calls != 1 {
			t.Errorf("%s is whitelisted as a bounded wrapper but calls %s %d times, want exactly 1; a wrapper that is not a single delegation cannot be trusted by name",
				name, boundedLimitationPrimitive, calls)
		}
		if calls := countCalls(function.Body, "append"); calls != 0 {
			t.Errorf("%s is whitelisted as a bounded wrapper but contains %d raw append call(s); an addition made inside a whitelisted wrapper bypasses the cap while still looking bounded at every call site",
				name, calls)
		}
	}
}

type limitationWrite struct {
	function, value, position string
	// pos orders writes deterministically so the occurrence index below is
	// stable; the printed position string is for humans.
	pos token.Pos
}

// limitationWritesIn reports every write, inside ONE function, to a value
// that reaches a result's Limitations.
//
// Two steps. First find the destined values: anything assigned into a
// `.Limitations` field or named by a `Limitations:` field in a composite
// literal. A destined value fed from a local makes that local destined too,
// transitively, so `result.Limitations = composed` reaches back to whatever
// wrote `composed`. Then report EVERY write to any destined local, with no
// attempt to decide which one wins.
//
// An identifier that is NEVER WRITTEN inside this function -- a package-level
// slice, a parameter, anything from outside -- is judged at the point it is
// read (codex round-7 F1). Deferring to "its own writes are judged where they
// appear" is only sound when such writes exist; when they do not, the alias
// read IS the write, and skipping it let `var local = packageLimitations`
// reach a result with nothing reported anywhere.
// Identity is the DECLARATION, never the spelling (codex round-8 P1).
// Keying by name let shadowing evade the guard: in
// `for k, m := range m { … result.Limitations = m }` the inner binding and
// the outer variable are different entities that happen to share a spelling,
// and one map entry for "m" made each alias path defer to the other.
// go/parser's object resolution gives each declaration its own *ast.Object,
// so the two are simply not the same key.
func limitationWritesIn(t *testing.T, fileSet *token.FileSet, function *ast.FuncDecl) []limitationWrite {
	t.Helper()
	var (
		writes   []limitationWrite
		destined = map[*ast.Object]bool{}
		direct   []limitationWrite
	)
	// Gathered FIRST: whether an identifier is written in this function
	// decides how a read of it is treated below.
	locals := localWritesIn(t, fileSet, function)
	writtenLocally := map[*ast.Object]bool{}
	for _, local := range locals {
		writtenLocally[local.object] = true
	}
	record := func(value ast.Expr, pos token.Pos) {
		if identifier, ok := value.(*ast.Ident); ok {
			// A nil Obj means the identifier is not declared in this
			// function at all -- package-level, imported, or a builtin. It
			// is deliberately NOT entered into the destined set: every such
			// identifier would collide on the nil key. It is reported at
			// this read instead, which is the round-7 F1 rule.
			if identifier.Obj != nil {
				destined[identifier.Obj] = true
				if writtenLocally[identifier.Obj] {
					return
				}
			}
			// Nothing in this function ever wrote it, so there is no later
			// site that will answer for it.
			value = identifier
		}
		direct = append(direct, limitationWrite{
			function: function.Name.Name,
			value:    limitationWriteSource(value),
			position: fileSet.Position(pos).String(),
			pos:      pos,
		})
	}

	ast.Inspect(function.Body, func(node ast.Node) bool {
		// `InvestigationResult{... Limitations: x ...}` -- a KeyValueExpr,
		// never an assignment to a .Limitations field, which is how the
		// whole terminal path once sat outside this guard.
		if literal, ok := node.(*ast.CompositeLit); ok {
			for _, element := range literal.Elts {
				field, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := field.Key.(*ast.Ident); ok && key.Name == "Limitations" {
					record(field.Value, field.Pos())
				}
			}
		}
		if assignment, ok := node.(*ast.AssignStmt); ok {
			for index, target := range assignment.Lhs {
				selector, ok := target.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Limitations" {
					continue
				}
				record(assignmentSource(assignment, index), assignment.Pos())
			}
		}
		// `var r = InvestigationResult{...}` reaches the composite-literal
		// branch above on its own, so nothing is needed for the FIELD side
		// here; what ValueSpec adds is the LOCAL side, handled by
		// localWritesIn below.
		// A raw append straight onto a Limitations field never appears as a
		// value worth resolving, so it is named on sight.
		if call, ok := node.(*ast.CallExpr); ok {
			if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == "append" && len(call.Args) > 0 {
				if selector, ok := call.Args[0].(*ast.SelectorExpr); ok && selector.Sel.Name == "Limitations" {
					direct = append(direct, limitationWrite{
						function: function.Name.Name,
						value:    "append",
						position: fileSet.Position(call.Pos()).String(),
						pos:      call.Pos(),
					})
				}
			}
		}
		return true
	})
	writes = append(writes, direct...)

	// Transitive closure over locals, to a fixpoint. Bounded by the number
	// of writes, so a chain cannot loop.
	for changed := true; changed; {
		changed = false
		for _, local := range locals {
			if !destined[local.object] {
				continue
			}
			if named, ok := local.source.(*ast.Ident); ok && named.Obj != nil && !destined[named.Obj] {
				destined[named.Obj] = true
				changed = true
			}
		}
	}

	// Every write to every destined local, judged individually.
	for _, local := range locals {
		if !destined[local.object] {
			continue
		}
		// A destined local fed from another local is not a write of new
		// content -- but ONLY when that other local is itself written in
		// this function, so there really is a later site to judge. An
		// identifier from outside the function has no such site, so the
		// read is reported here under its own name.
		//
		// Compared by DECLARATION: a shadowing binding that reads the
		// variable it shadows is two entities, and only the outer one's own
		// writes can answer for it.
		if named, ok := local.source.(*ast.Ident); ok && named.Obj != nil && writtenLocally[named.Obj] {
			continue
		}
		writes = append(writes, limitationWrite{
			function: function.Name.Name,
			value:    limitationWriteSource(local.source),
			position: fileSet.Position(local.pos).String(),
			pos:      local.pos,
		})
	}
	return writes
}

// localWrite is one place a local receives a value.
//
// object is the identity: two bindings that share a spelling but not a
// declaration are different locals, and the guard must not confuse them.
// name is carried for human-readable messages only, never for lookups.
type localWrite struct {
	object *ast.Object
	name   string
	source ast.Expr
	pos    token.Pos
}

// localWritesIn gathers every write to a named local inside one function.
//
// THE COMPLETENESS CRITERION, which is this guard's closure argument:
// the walker covers every write form the AST CAN see -- assignments,
// declaration initializers, range bindings, and alias reads of identifiers
// the function never writes. What the AST cannot see is named out of scope
// below, with the reason. After that, a new evasion has to be either a
// member of the named AST-invisible class or a bug in this walker; it
// cannot be an unenumerated statement kind.
//
// The forms, and the node each one actually is:
//
//   - `x := e`, `x = e`               *ast.AssignStmt
//   - `var x = e`, `var ( x = e )`    *ast.ValueSpec (a DIFFERENT node --
//     scanning only assignments, codex round-6 P1, let a raw append hide
//     in a var line)
//   - `for _, x := range e`           *ast.RangeStmt (neither of the above,
//     codex round-7 F2)
//   - a read of an identifier written nowhere in this function -- handled
//     by limitationWritesIn, which reports it at the read.
//
// A valueless `var x []string` is deliberately not a write: it declares an
// empty list, which cannot be the write that overflows a cap. Anything later
// added to it arrives as an assignment and is judged then.
//
// EVERY OTHER go/ast statement and spec kind, swept once against the
// criterion and dismissed with the reason it cannot carry a write:
//
//   - BadStmt: only in code that does not compile.
//   - EmptyStmt, BranchStmt (break/continue/goto): no value, no target.
//   - IncDecStmt: numeric only; cannot hold a slice.
//   - SendStmt (ch <- x): writes to a channel, not to a name. The matching
//     receive is an AssignStmt and is seen there.
//   - ExprStmt: evaluates and discards. A bare `append(x, …)` writes
//     nothing; `mutate(&x)` is the AST-invisible class below.
//   - ImportSpec, TypeSpec: bind packages and types, not values.
//   - DeclStmt, BlockStmt, LabeledStmt, IfStmt, ForStmt, SwitchStmt,
//     CaseClause, SelectStmt, CommClause, GoStmt, DeferStmt: containers or
//     wrappers. ast.Inspect descends into all of them, so any assignment,
//     initializer or range binding inside -- including in an if-init, a
//     for-post, a select's comm clause, or a deferred closure body -- is
//     collected by the cases above.
//   - TypeSwitchStmt (`switch v := x.(type)`): its Assign field IS an
//     AssignStmt, so the binding is already collected.
//   - ReturnStmt: returns values rather than naming them. A composite
//     literal inside one is reached by the literal walk; a returned list
//     is judged at the caller, as that call's result.
//
// WRITE FORMS THE AST CANNOT SEE, out of scope with the reason:
//
//   - mutation through a pointer, or a helper taking &x. An AST walk cannot
//     follow it. This is the one real blind spot, and nothing in this
//     package composes limitations that way.
//   - copy(x, raw) and element assignment x[i] = s. Both mutate in place
//     without producing a value, so neither can grow a list past the cap,
//     which is the property being defended.
func localWritesIn(t *testing.T, fileSet *token.FileSet, function *ast.FuncDecl) []localWrite {
	t.Helper()
	var writes []localWrite
	// Object resolution is what makes shadowing visible, so its absence is
	// a silent unsoundness rather than a missing nicety: every binding
	// would collapse onto a nil key. Fail loudly instead.
	add := func(identifier *ast.Ident, source ast.Expr, pos token.Pos) {
		if identifier.Name == "_" {
			return
		}
		if identifier.Obj == nil {
			t.Fatalf("%s: local %q has no resolved declaration; this guard distinguishes shadowed bindings by *ast.Object, so unresolved parsing would silently merge them",
				fileSet.Position(identifier.Pos()), identifier.Name)
		}
		writes = append(writes, localWrite{identifier.Obj, identifier.Name, source, pos})
	}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.AssignStmt:
			for index, target := range declaration.Lhs {
				if identifier, ok := target.(*ast.Ident); ok {
					add(identifier, assignmentSource(declaration, index), declaration.Pos())
				}
			}
		case *ast.ValueSpec:
			if len(declaration.Values) == 0 {
				return true
			}
			for index, name := range declaration.Names {
				source := declaration.Values[0]
				if len(declaration.Values) == len(declaration.Names) {
					source = declaration.Values[index]
				}
				add(name, source, declaration.Pos())
			}
		case *ast.RangeStmt:
			// Both bindings take their value FROM the ranged expression, so
			// that expression is the source: ranging a slice of raw lists
			// and assigning the element into a field is a write of whatever
			// built those lists.
			for _, target := range []ast.Expr{declaration.Key, declaration.Value} {
				if identifier, ok := target.(*ast.Ident); ok {
					add(identifier, declaration.X, declaration.Pos())
				}
			}
		}
		return true
	})
	return writes
}

// assignmentSource picks the expression feeding one target of an
// assignment. For a multi-value right-hand side (`a, b := f()`) every target
// resolves to that one call, which is how the appenders' two results are
// unpacked.
func assignmentSource(assignment *ast.AssignStmt, index int) ast.Expr {
	if len(assignment.Rhs) == len(assignment.Lhs) {
		return assignment.Rhs[index]
	}
	return assignment.Rhs[0]
}

func callsFunction(body *ast.BlockStmt, name string) bool {
	return countCalls(body, name) > 0
}

func countCalls(body *ast.BlockStmt, name string) int {
	count := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if identifier, ok := call.Fun.(*ast.Ident); ok && identifier.Name == name {
			count++
		}
		return true
	})
	return count
}

// limitationWriteSource names what a limitations write is fed from: the
// callee for a direct call, the shape for a literal, otherwise the
// identifier or expression text, so an audited entry can be keyed on it.
func limitationWriteSource(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.CallExpr:
		if identifier, ok := value.Fun.(*ast.Ident); ok {
			return identifier.Name
		}
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			return selector.Sel.Name
		}
	case *ast.CompositeLit:
		// Typed, not bare. The audit list is keyed by function#value, and a
		// bare "composite literal" made ONE exemption cover EVERY literal in
		// that function: the audited `[]string{limitation}` seed silently
		// exempted an unrelated raw `[][]string{…}` a few lines away. Found
		// while mutation-testing round 8 -- the mutation passed for that
		// reason rather than the one under test.
		// The literal's OWN TEXT, not just its type. Typing it (round 8)
		// stopped `[]string` and `[][]string` colliding, but left two
		// different []string literals in one function sharing a shape --
		// and a shape shared is a key inherited when one of them is
		// deleted (round 10). Content makes two unrelated literals
		// different writes; two textually identical ones remain
		// interchangeable, which is correct, since they are the same
		// statement.
		return "composite literal " + truncateForKey(renderCompositeLit(value))
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	case *ast.TypeAssertExpr:
		// `switch v := x.(type)` and `v := x.([]string)`.
		return "type assertion on " + limitationWriteSource(value.X)
	case *ast.UnaryExpr:
		if value.Op == token.ARROW {
			// `v := <-ch`, including a select comm clause.
			return "channel receive from " + limitationWriteSource(value.X)
		}
	case *ast.IndexExpr:
		return "element of " + limitationWriteSource(value.X)
	}
	// Named shapes matter beyond diagnostics: the audit list is keyed by
	// function#value, so two different unresolved writes in one function
	// sharing a bare "?" would let auditing one silently exempt the other.
	return "?"
}

// truncateForKey keeps an expression's text short enough to read in an audit
// key while staying deterministic. Long enough that two literals in one
// function differ in the prefix; a tail that matters is a sign the write
// should be a named helper anyway.
func truncateForKey(text string) string {
	const limit = 60
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

// renderCompositeLit prints a composite literal WITH its elements.
// types.ExprString elides them to `{…}`, which is precisely the detail that
// distinguishes two same-typed literals in one function.
func renderCompositeLit(literal *ast.CompositeLit) string {
	var builder strings.Builder
	if literal.Type != nil {
		builder.WriteString(types.ExprString(literal.Type))
	}
	builder.WriteString("{")
	for index, element := range literal.Elts {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(types.ExprString(element))
	}
	builder.WriteString("}")
	return builder.String()
}
