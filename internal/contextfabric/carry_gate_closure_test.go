package contextfabric

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCarryGateClosure_EveryHitIsConstructedInsideAGatedProducer is the
// CONSTRUCTION proof for the same-question containment, and it is the only
// part of this change that can fail for a defect nobody has thought of yet.
//
// WHY A BEHAVIOURAL TEST IS NOT ENOUGH, stated plainly because three review
// rounds proved it. The containment escaped three times, at a different place
// each time -- stored-ancestry edges, then confirmation edges below a parent
// root, then the plan carry, which had no gate at all. Each escape had a
// behavioural test written for it afterwards, and each of those tests passed
// on the next escape, because a behavioural test can only fail for a path that
// EXISTS. The plan carry was never gated because nobody enumerated the
// producers; no fixture can fail for a producer nobody knew about.
//
// So the property asserted here is not "these chains behave correctly" (the
// per-axis arms in chain_identity_test.go do that). It is:
//
//	Every carry HIT in this package is CONSTRUCTED inside a function that a
//	gated producer is the sole caller of.
//
// A fourth axis, or a new hit site inside an existing axis, fails this test at
// the moment it is written rather than at the moment someone thinks to write a
// fixture for it. That is the difference between a property and a habit.
//
// HOW "GATED" IS ESTABLISHED. The three producers are named below, and each is
// separately asserted to contain the choke-point call
// (carryOriginSameQuestionVerdict). Naming them without checking they still
// carry the gate would let the gate be deleted from a named producer while
// this test kept passing on the name alone.
func TestCarryGateClosure_EveryHitIsConstructedInsideAGatedProducer(t *testing.T) {
	t.Parallel()

	// axis names the three carry result types, the Hit constant each uses,
	// and the producer that owns the choke point for that axis.
	//
	// The WALK is not listed. It is discovered: a hit may be constructed in a
	// helper only if the producer is that helper's SOLE caller in this
	// package, which is checked below rather than asserted here. Listing the
	// walks by name would let a second caller be added to one without this
	// test noticing -- and a second caller is exactly how a hit escapes a
	// producer's gate.
	//
	// Producers are named RECEIVER-QUALIFIED. This package declares two
	// unrelated methods called `For` on different receivers, so a bare-name
	// index cannot tell two same-named methods apart -- and an index that
	// cannot tell them apart would silently merge their call sets, which is
	// the sort of quiet weakening this whole change exists to stop.
	axes := []struct {
		resultType  string
		hitConstant string
		producer    string
	}{
		{"windowCarryResult", "WindowCarryHit", "Engine.resolveCarriedWindow"},
		{"kindCarryResult", "KindCarryHit", "Engine.resolveCarriedKind"},
		{"planCarryResult", "PlanCarryHit", "Engine.resolveCarriedPlan"},
	}

	pkg := parsePackageForClosure(t, ".")

	// The gate itself must exist and be reachable, or every "is it gated?"
	// answer below is vacuously about a function that does nothing.
	if len(pkg.bare[chokePointCall]) == 0 {
		t.Fatalf("%s is not declared in this package: the choke point this test certifies does not exist, so a green result here would certify nothing", chokePointCall)
	}
	// The choke point must be UNAMBIGUOUS. Two methods sharing its bare name
	// would make "does this producer call the gate?" answerable by the wrong
	// one, which is a false green of exactly the kind this file exists to
	// prevent.
	pkg.requireUnambiguous(t, chokePointCall)

	for _, axis := range axes {
		t.Run(axis.resultType, func(t *testing.T) {
			if _, ok := pkg.functions[axis.producer]; !ok {
				t.Fatalf("%s is not declared in this package: the producer this axis names does not exist, so nothing below is a measurement", axis.producer)
			}
			sites := pkg.hitConstructionSites(axis.resultType, axis.hitConstant)

			// SALTED POSITIVE (standing rule): the walk must FIND the known
			// producer's hit site. A broken parse, a renamed type or a walk
			// that matches nothing all report "zero ungated sites", which
			// reads exactly like a clean result.
			if len(sites) == 0 {
				t.Fatalf("found no %s literal with Outcome: %s anywhere in the package -- the AST walk is broken or the type was renamed, so 'no ungated construction' here is an absence of measurement, not a result", axis.resultType, axis.hitConstant)
			}

			if !pkg.callsChokePoint(axis.producer) {
				t.Fatalf("%s does not call %s: it is named as this axis's gated producer, and a producer that no longer applies the same-question comparison is exactly the regression this test exists to catch", axis.producer, chokePointCall)
			}

			for _, site := range sites {
				// A helper whose bare name is shared with another declaration
				// cannot be attributed a call set, so it can never be shown
				// gated. Refuse rather than let an ambiguous answer pass.
				pkg.requireUnambiguous(t, site.enclosingBare)
				switch {
				case site.enclosing == axis.producer:
					// Constructed in the producer itself: gated by definition.
				case pkg.soleCallerOf(site.enclosingBare) == axis.producer:
					// Constructed in a helper the producer is the SOLE caller
					// of, so every value it returns passes through the gate.
				default:
					callers := pkg.callersOf(site.enclosingBare)
					t.Errorf("%s:%d constructs %s{Outcome: %s} inside %s, which is not %s and is not a helper %s is the sole caller of (callers: %v).\n"+
						"A hit constructed outside the gated producer's reach is a hit that never meets the same-question comparison -- the shape that escaped three review rounds.\n"+
						"Either construct it inside %s, or make %s the sole caller of %s, or extend the containment to cover the new producer and add it to this test's axis table.",
						site.file, site.line, axis.resultType, axis.hitConstant, site.enclosing, axis.producer, axis.producer, callers,
						axis.producer, axis.producer, site.enclosing)
				}
			}
		})
	}
}

// TestCarryGateClosure_DetectsAnUngatedHitSite is the NEGATIVE CONTROL.
//
// The test above reports success by finding nothing wrong, which is
// indistinguishable from a walk that cannot find anything at all. This plants
// the exact defect -- a hit constructed in a function no producer calls -- in
// a synthetic package and requires the same machinery to name it.
//
// It runs against a temp-dir fixture rather than the real tree so that proving
// the instrument can fail never involves making the real package fail.
func TestCarryGateClosure_DetectsAnUngatedHitSite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := `package sample

type windowCarryResult struct{ Outcome string }

const WindowCarryHit = "hit"

func (e *Engine) resolveCarriedWindow() windowCarryResult {
	return e.walkCarriedWindow()
}

func (e *Engine) walkCarriedWindow() windowCarryResult {
	return windowCarryResult{Outcome: WindowCarryHit}
}

// smuggle is the planted defect: a SECOND, unreachable-from-the-producer site
// that mints a hit. This is the shape of all three real escapes.
func smuggle() windowCarryResult {
	return windowCarryResult{Outcome: WindowCarryHit}
}
`
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	pkg := parsePackageForClosure(t, dir)
	sites := pkg.hitConstructionSites("windowCarryResult", "WindowCarryHit")
	if len(sites) != 2 {
		t.Fatalf("found %d hit sites in the fixture, want 2 (the legitimate one in the walk and the planted one in smuggle) -- the walk cannot see what it claims to check", len(sites))
	}

	var ungated []string
	for _, site := range sites {
		if site.enclosing == "Engine.resolveCarriedWindow" || pkg.soleCallerOf(site.enclosingBare) == "Engine.resolveCarriedWindow" {
			continue
		}
		ungated = append(ungated, site.enclosing)
	}
	if len(ungated) != 1 || ungated[0] != "smuggle" {
		t.Fatalf("the closure check named %v as ungated, want exactly [smuggle]: an instrument that cannot name a planted ungated producer cannot certify that the real package has none", ungated)
	}
}

// TestCarryGateClosure_CatchesEveryConstructionShape is the per-shape negative
// control for the walk, and it exists because codex round 1's shape list was
// PARTLY right: two of the four shapes it named were real misses, one was
// already disclosed, and one (a literal inside a closure) it argued was a miss
// when measurement showed the walk already caught it.
//
// That mix is the reason each row is measured here rather than reasoned about.
// A claim that an instrument misses a shape, and a claim that it catches one,
// are both testable, and this table is what makes either provable.
func TestCarryGateClosure_CatchesEveryConstructionShape(t *testing.T) {
	t.Parallel()

	const header = "package sample\n\ntype windowCarryResult struct{ W *int; S string; Outcome string; D int; V bool }\n\nconst WindowCarryHit = \"hit\"\n\n"

	for _, tc := range []struct {
		name string
		src  string
		// wantSites is how many hit constructions the walk must attribute.
		wantSites int
		// wantUnkeyed is how many unattributable literals it must REFUSE.
		wantUnkeyed int
	}{
		{name: "keyed literal", wantSites: 1,
			src: `func ungated() windowCarryResult { return windowCarryResult{Outcome: WindowCarryHit} }`},
		{name: "post-construction assignment", wantSites: 1,
			src: `func ungated() windowCarryResult { r := windowCarryResult{}; r.Outcome = WindowCarryHit; return r }`},
		{name: "literal inside a closure", wantSites: 1,
			src: `func ungated() windowCarryResult { f := func() windowCarryResult { return windowCarryResult{Outcome: WindowCarryHit} }; return f() }`},
		{name: "literal behind a type alias", wantSites: 1,
			src: "type wAlias = windowCarryResult\n\nfunc ungated() windowCarryResult { return wAlias{Outcome: WindowCarryHit} }"},
		{name: "unkeyed literal is REFUSED, not silently missed", wantSites: 0, wantUnkeyed: 1,
			src: `func ungated() windowCarryResult { return windowCarryResult{nil, "", WindowCarryHit, 0, false} }`},
		{name: "a MISS literal is not a hit", wantSites: 0,
			src: `func ungated() windowCarryResult { return windowCarryResult{Outcome: "miss_no_reference"} }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "s.go"), []byte(header+tc.src+"\n"), 0o600); err != nil {
				t.Fatalf("writing the fixture: %v", err)
			}
			pkg := parsePackageForClosure(t, dir)
			sites, unkeyed := pkg.hitConstructionSitesAndUnkeyed("windowCarryResult", "WindowCarryHit")
			if len(sites) != tc.wantSites {
				t.Errorf("attributed %d hit site(s), want %d -- the walk cannot see this construction shape, so an ungated producer using it would pass the closure proof", len(sites), tc.wantSites)
			}
			if len(unkeyed) != tc.wantUnkeyed {
				t.Errorf("refused %d unkeyed literal(s), want %d (%v)", len(unkeyed), tc.wantUnkeyed, unkeyed)
			}
		})
	}
}

// chokePointCall is the method every gated producer must call. Named once so
// the assertion and the failure message cannot disagree.
const chokePointCall = "carryOriginSameQuestionVerdict"

// closurePackage is the parsed, non-test source of one package plus the two
// derived relations this check needs: which function encloses each node, and
// which functions call which.
type closurePackage struct {
	fset *token.FileSet
	// functions maps a RECEIVER-QUALIFIED name ("Engine.resolveCarriedWindow",
	// or just "smuggle" for a plain function) to its declaration. Qualified
	// because this package really does declare two different methods named
	// `For`, and merging their call sets would weaken every answer below.
	functions map[string]*ast.FuncDecl
	// bare maps a BARE name to every qualified name sharing it. A bare name
	// with more than one entry is AMBIGUOUS: call sites are attributed by
	// selector, which carries only the bare name, so nothing can say which of
	// two same-named methods a call reached. requireUnambiguous refuses those
	// rather than guessing.
	bare map[string][]string
	// aliases maps an alias name to the type it aliases, so a hit constructed
	// through `type wAlias = windowCarryResult` is still attributed to the
	// real result type.
	aliases map[string]string
	// calls maps a callee BARE name to the set of QUALIFIED enclosing function
	// names that call it. Bare on the callee side because that is all a
	// selector expression carries without type information.
	calls map[string]map[string]bool
}

// requireUnambiguous fails the test when name is shared by more than one
// declaration. Every answer this file gives about a bare name is unsound if
// two declarations share it, so the ambiguity is reported rather than
// resolved arbitrarily.
func (p *closurePackage) requireUnambiguous(t *testing.T, name string) {
	t.Helper()
	if len(p.bare[name]) > 1 {
		t.Fatalf("%q is declared %d times (%v): call sites are attributed by selector, which carries only the bare name, so no claim about which declaration a call reached would be sound", name, len(p.bare[name]), p.bare[name])
	}
}

// qualifiedFuncName renders a declaration's receiver-qualified name: methods
// as "Receiver.Method" (pointer receivers stripped of their star), plain
// functions as their own name.
func qualifiedFuncName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	switch recv := typ.(type) {
	case *ast.Ident:
		return recv.Name + "." + fn.Name.Name
	case *ast.IndexExpr: // a generic receiver, e.g. Foo[T]
		if ident, ok := recv.X.(*ast.Ident); ok {
			return ident.Name + "." + fn.Name.Name
		}
	}
	return fn.Name.Name
}

// parsePackageForClosure parses every non-test .go file in dir.
//
// TEST FILES ARE EXCLUDED DELIBERATELY. A test may legitimately construct a
// hit literal to build a fixture, and counting those would make the check
// unpassable for the wrong reason; the property under test is about production
// construction sites. That exclusion is also why the negative control above
// exists in its own temp-dir package rather than as a _test.go decoy here --
// a decoy in a test file would be invisible to the very walk it is decoying.
func parsePackageForClosure(t *testing.T, dir string) *closurePackage {
	t.Helper()
	fset := token.NewFileSet()
	pkg := &closurePackage{
		fset:      fset,
		functions: map[string]*ast.FuncDecl{},
		bare:      map[string][]string{},
		aliases:   map[string]string{},
		calls:     map[string]map[string]bool{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	parsed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		parsed++
		for _, decl := range file.Decls {
			// TYPE ALIASES first (codex r1): `type wAlias = windowCarryResult`
			// makes `wAlias{Outcome: WindowCarryHit}` a hit construction the
			// name-matching walk below could not see. Measured, not assumed --
			// the alias shape was probed and MISSED before this.
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Assign.IsValid() {
						continue // a defined type is a different type; only an ALIAS aliases
					}
					if ident, ok := ts.Type.(*ast.Ident); ok {
						pkg.aliases[ts.Name.Name] = ident.Name
					}
				}
				continue
			}
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			qualified := qualifiedFuncName(fn)
			if prior, clash := pkg.functions[qualified]; clash {
				// A qualified-name clash is a genuine impossibility in valid
				// Go, so reaching here means the parse or the qualifier is
				// wrong and nothing below can be trusted.
				t.Fatalf("two declarations resolve to %s (%s and %s): the name qualifier is wrong, so the call graph below is unsound",
					qualified, fset.Position(prior.Pos()), fset.Position(fn.Pos()))
			}
			pkg.functions[qualified] = fn
			pkg.bare[fn.Name.Name] = append(pkg.bare[fn.Name.Name], qualified)
			pkg.recordCalls(qualified, fn)
		}
	}
	if parsed == 0 {
		t.Fatalf("parsed no non-test Go files in %s -- every result below would be vacuous", dir)
	}
	return pkg
}

// recordCalls indexes every call made inside fn, keyed by the callee's bare
// name. A selector call (e.walkCarriedWindow(...)) is recorded under the
// selector's field name, which is how a method call on the engine receiver is
// matched to its declaration.
func (p *closurePackage) recordCalls(qualified string, fn *ast.FuncDecl) {
	if fn.Body == nil {
		return
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var callee string
		switch target := call.Fun.(type) {
		case *ast.Ident:
			callee = target.Name
		case *ast.SelectorExpr:
			callee = target.Sel.Name
		}
		if callee == "" {
			return true
		}
		if p.calls[callee] == nil {
			p.calls[callee] = map[string]bool{}
		}
		p.calls[callee][qualified] = true
		return true
	})
}

// hitSite is one composite literal that mints a carry hit.
type hitSite struct {
	file string
	line int
	// enclosing is the receiver-qualified name (what a producer is compared
	// against); enclosingBare is the bare name (what a call site's selector
	// carries, and therefore the key the call graph is indexed by).
	enclosing     string
	enclosingBare string
}

// hitConstructionSites finds every composite literal of resultType whose
// Outcome field is set to hitConstant, and reports the function enclosing
// each.
//
// KEYED ON THE OUTCOME FIELD, not on the type alone: a miss literal of the
// same type is not a carry and gating it would be meaningless.
//
// FOUR CONSTRUCTION SHAPES ARE HANDLED, three of them added after codex round
// 1 measured which ones this walk actually missed:
//
//	keyed literal                 CAUGHT (the baseline)
//	literal inside a closure      CAUGHT -- measured; the round ARGUED this was
//	                              a miss and it is not, because ast.Inspect
//	                              descends into function literals and attributes
//	                              them to the enclosing declaration
//	post-construction assignment  now CAUGHT (`r.Outcome = Hit`); it used to be
//	                              a DISCLOSED limit, and disclosing a hole is
//	                              not closing one
//	literal behind a type alias   now CAUGHT (`type wAlias = windowCarryResult`)
//	UNKEYED literal               REFUSED outright -- it sets Outcome
//	                              positionally, which cannot be attributed
//	                              without type information, and an
//	                              unattributable construction is an unproven one
//
// The behavioural arms remain regardless: this walk reasons about syntax, and
// syntax cannot say whether the gate DECIDES correctly.
func (p *closurePackage) hitConstructionSites(resultType, hitConstant string) []hitSite {
	sites, unkeyed := p.hitConstructionSitesAndUnkeyed(resultType, hitConstant)
	if len(unkeyed) > 0 {
		panic("unkeyed " + resultType + " literal(s) this walk cannot attribute: " + strings.Join(unkeyed, ", "))
	}
	return sites
}

// hitConstructionSitesAndUnkeyed is the walk proper. Unkeyed literals are
// returned separately rather than swallowed, so the caller decides whether an
// unreadable construction is a test failure (production) or an expected probe
// result (the negative controls).
func (p *closurePackage) hitConstructionSitesAndUnkeyed(resultType, hitConstant string) ([]hitSite, []string) {
	var sites []hitSite
	var unkeyed []string
	for name, fn := range p.functions {
		if fn.Body == nil {
			continue
		}
		bareName := fn.Name.Name
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			// POST-CONSTRUCTION ASSIGNMENT (codex r1): `r.Outcome = Hit`
			// mints a hit without any literal naming it. Previously this was
			// a DISCLOSED limit; disclosing a hole is not closing one, so it
			// is closed here.
			if assign, ok := n.(*ast.AssignStmt); ok {
				for i, lhs := range assign.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Outcome" || i >= len(assign.Rhs) {
						continue
					}
					if rhs, ok := assign.Rhs[i].(*ast.Ident); ok && rhs.Name == hitConstant {
						pos := p.fset.Position(assign.Pos())
						sites = append(sites, hitSite{file: filepath.Base(pos.Filename), line: pos.Line, enclosing: name, enclosingBare: bareName})
					}
				}
				return true
			}
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			ident, ok := lit.Type.(*ast.Ident)
			if !ok || p.resolveAlias(ident.Name) != resultType {
				return true
			}
			// UNKEYED LITERAL (codex r1): `windowCarryResult{w, id, Hit, 0, false}`
			// sets Outcome positionally, which this walk cannot attribute
			// without type information. Rather than under-report -- an
			// unattributable construction is an unproven one -- refuse the
			// form outright, so the closure proof never depends on a shape it
			// cannot read.
			hasKeys := false
			for _, elt := range lit.Elts {
				if _, ok := elt.(*ast.KeyValueExpr); ok {
					hasKeys = true
					break
				}
			}
			if len(lit.Elts) > 0 && !hasKeys {
				pos := p.fset.Position(lit.Pos())
				unkeyed = append(unkeyed, fmt.Sprintf("%s:%d (in %s)", filepath.Base(pos.Filename), pos.Line, name))
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Outcome" {
					continue
				}
				value, ok := kv.Value.(*ast.Ident)
				if !ok || value.Name != hitConstant {
					continue
				}
				pos := p.fset.Position(lit.Pos())
				sites = append(sites, hitSite{file: filepath.Base(pos.Filename), line: pos.Line, enclosing: name, enclosingBare: bareName})
			}
			return true
		})
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].line < sites[j].line
	})
	return sites, unkeyed
}

// resolveAlias follows a type alias to the type it names, one hop, which is
// all Go permits in a single declaration and all this walk needs.
func (p *closurePackage) resolveAlias(name string) string {
	if target, ok := p.aliases[name]; ok {
		return target
	}
	return name
}

// callersOf returns the sorted set of QUALIFIED function names in this package
// that call the bare name fn.
func (p *closurePackage) callersOf(fn string) []string {
	var callers []string
	for caller := range p.calls[fn] {
		callers = append(callers, caller)
	}
	sort.Strings(callers)
	return callers
}

// soleCallerOf returns the ONE function that calls fn, or "" when fn has zero
// callers or more than one.
//
// "" for zero callers is deliberate: dead code that mints a hit is not gated
// by anything, and treating it as gated would let a producer be deleted out
// from under a hit site while this check stayed green.
func (p *closurePackage) soleCallerOf(fn string) string {
	callers := p.callersOf(fn)
	if len(callers) != 1 {
		return ""
	}
	return callers[0]
}

// callsChokePoint reports whether fn calls the same-question comparison.
func (p *closurePackage) callsChokePoint(qualified string) bool {
	return p.calls[chokePointCall][qualified]
}
