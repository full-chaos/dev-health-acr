package contextfabric_test

// The emission collector's RESIDUAL, closed by reachability rather than by
// shape.
//
// WHAT WAS LEFT OPEN. declaration_emission_parity_test.go collects the
// field keys a provider emits by walking its own METHOD BODIES for two
// syntactic shapes: a map literal of the fact-field type, and a
// string-keyed write into a local holding one. Its doc comment states the
// bound in its own words -- "a field emitted through a helper, through a
// loop over a slice of names, or into a map built in another function is
// invisible to it" -- and the bound is known LIVE rather than
// hypothetical: round 3 of the declaration slice found the first version of
// exactly this gap.
//
// WHY THIS IS A REACHABILITY WALK AND NOT THREE MORE SHAPES. Adding a case
// for "helper calls" and a case for "range loops" would close the two holes
// that have been noticed and leave the next one open. That is the failure
// mode the CHAOS-4782 family gate spent four review rounds on: a walk
// enumerated by shape loses a round per shape it did not think of, and the
// four findings were all one class. So the quantification here is over the
// CALL GRAPH -- every package-local function reachable from a provider's
// methods, to any depth -- and over TYPES rather than syntax: a write is
// counted when the thing written into HAS the fact-field map type,
// whatever expression names it (a local, a parameter, a struct field, a
// value returned from somewhere else). A helper three levels down that
// takes the map as a parameter is not a special case here; it is the
// ordinary case.
//
// WHAT IT STILL DOES NOT REACH, and this is COUNTED rather than described.
// A key that is not statically determined -- built from a variable, a
// format string, a value read from a row -- cannot be resolved by any
// static walk. Rather than leave that as a sentence in a header, every such
// site is COUNTED per provider and the count is a column of the artifact.
// A provider with no dynamic-key sites shows a zero, which is an OBSERVED
// zero; a provider that grows one shows up as an artifact diff. That is the
// difference between a residual a reader can audit and a residual that is
// merely admitted. The other two residuals -- a helper in ANOTHER package,
// and reflection -- are named in the artifact header and are not counted,
// because a count of them would be a count of things this walk cannot see
// at all.
//
// HOST-ONLY. This loads the provider package for its TYPES. It compiles the
// package; it does not run it, and it constructs no provider client, so no
// container starts.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const emissionReachabilityArtifact = "testdata/emitted_fields_reachable.txt"

// reachableEmission is one provider's emitted-field picture.
type reachableEmission struct {
	// kind is the FactKind constant name the provider's newCapability call
	// names (e.g. "FactFlow").
	kind string
	// fields are the statically resolved field keys, sorted.
	fields []string
	// dynamicKeySites counts writes into a fact-field map whose key this
	// walk could not resolve to a string. See the header: this is the
	// residual, made countable.
	dynamicKeySites int
	// unresolvedCallSites counts calls whose callee this walk could not
	// resolve to a package-local function: a function value supplied by a
	// parameter or a field, an interface method, a closure from elsewhere.
	// Those are edges the walk cannot follow, so a field written behind one
	// is invisible -- and it is COUNTED for the same reason dynamicKeySites
	// is, because a limit that is not counted is indistinguishable from an
	// absence.
	unresolvedCallSites int
	// reachedFuncs is how many package-local functions the walk visited
	// from this provider's methods. A provider whose helpers were somehow
	// not followed would show a suspiciously small number, so the walk's
	// own reach is reported rather than assumed.
	reachedFuncs int
}

// funcFacts is what one function does, independent of who calls it.
type funcFacts struct {
	writes              map[string]bool
	dynamicKeySites     int
	unresolvedCallSites int
	calls               map[*types.Func]bool
}

// loadProviderPackage type-checks devhealthfacts.
func loadProviderPackage(t *testing.T) *packages.Package {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Tests: false,
	}, "./devhealthfacts")
	if err != nil {
		t.Fatalf("loading the provider package: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d packages, want exactly 1", len(loaded))
	}
	pkg := loaded[0]
	if len(pkg.Errors) > 0 {
		t.Fatalf("the provider package does not type-check, so this walk would be reading a broken tree: %v", pkg.Errors)
	}
	if len(pkg.Syntax) == 0 {
		t.Fatal("the provider package parsed to zero files; every count below would be a vacuous zero")
	}
	return pkg
}

// isFactFieldMap reports whether a type IS the fact-field map --
// map[string]contextfabric.FactValue -- by its TYPE rather than by how it
// was spelled.
//
// This is what makes a parameter, a struct field and a local
// indistinguishable to the walk, which is the whole point: the merged
// collector had to recognise each spelling separately and therefore had a
// hole per spelling it had not met.
func isFactFieldMap(typ types.Type) bool {
	mapType, isMap := typ.Underlying().(*types.Map)
	if !isMap {
		return false
	}
	if basic, isBasic := mapType.Key().Underlying().(*types.Basic); !isBasic || basic.Kind() != types.String {
		return false
	}
	named, isNamed := mapType.Elem().(*types.Named)
	if !isNamed {
		return false
	}
	object := named.Obj()
	return object != nil && object.Name() == "FactValue" &&
		object.Pkg() != nil && strings.HasSuffix(object.Pkg().Path(), "/internal/contextfabric")
}

// stringValuesOfRangeVars maps each range variable that iterates a literal
// slice of strings to the values it can take.
//
// A loop over a literal name list is one of the two shapes the merged
// collector's doc comment names as invisible, and it is resolvable because
// the values are right there. It is handled HERE, at key resolution, rather
// than as a separate collection pass -- so it composes with the call-graph
// walk instead of being a second thing that has to be kept in step.
func stringValuesOfRangeVars(info *types.Info, fn ast.Node) map[types.Object][]string {
	values := map[types.Object][]string{}
	ast.Inspect(fn, func(node ast.Node) bool {
		rangeStmt, isRange := node.(*ast.RangeStmt)
		if !isRange || rangeStmt.Value == nil {
			return true
		}
		composite, isComposite := rangeStmt.X.(*ast.CompositeLit)
		if !isComposite {
			return true
		}
		var literals []string
		for _, element := range composite.Elts {
			value, known := constantString(info, element)
			if !known {
				return true
			}
			literals = append(literals, value)
		}
		if len(literals) == 0 {
			return true
		}
		ident, isIdent := rangeStmt.Value.(*ast.Ident)
		if !isIdent {
			return true
		}
		if object := info.Defs[ident]; object != nil {
			values[object] = literals
		}
		return true
	})
	return values
}

// packageFuncsBoundToLocals maps each local assigned a PACKAGE-LEVEL
// FUNCTION to that function, so a call through the variable is followed as
// an ordinary call-graph edge. It resolves the shape a review round
// constructed and nothing more; a value arriving from a parameter or a field
// is counted at the call site instead.
func packageFuncsBoundToLocals(info *types.Info, fn ast.Node) map[types.Object]*types.Func {
	bound := map[types.Object]*types.Func{}
	ast.Inspect(fn, func(node ast.Node) bool {
		assign, isAssign := node.(*ast.AssignStmt)
		if !isAssign {
			return true
		}
		for index, rhs := range assign.Rhs {
			if index >= len(assign.Lhs) {
				break
			}
			ident, isIdent := rhs.(*ast.Ident)
			if !isIdent {
				continue
			}
			callee, isFunc := info.Uses[ident].(*types.Func)
			if !isFunc {
				continue
			}
			target, isTarget := assign.Lhs[index].(*ast.Ident)
			if !isTarget {
				continue
			}
			object := info.Defs[target]
			if object == nil {
				object = info.Uses[target]
			}
			if object != nil {
				bound[object] = callee
			}
		}
		return true
	})
	return bound
}

// localsBoundToFuncLiterals collects the locals assigned a function LITERAL
// in this same function. They are not edges: the literal's body is lexically
// inside the function being walked, so its writes are already collected.
// Recognising them keeps the residual counter honest -- the distinction
// between "the walk did not see this" and "the walk saw it by another route".
func localsBoundToFuncLiterals(info *types.Info, fn ast.Node) map[types.Object]bool {
	bound := map[types.Object]bool{}
	record := func(target ast.Expr) {
		ident, isIdent := target.(*ast.Ident)
		if !isIdent {
			return
		}
		object := info.Defs[ident]
		if object == nil {
			object = info.Uses[ident]
		}
		if object != nil {
			bound[object] = true
		}
	}
	ast.Inspect(fn, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			for index, rhs := range typed.Rhs {
				if _, isLit := rhs.(*ast.FuncLit); isLit && index < len(typed.Lhs) {
					record(typed.Lhs[index])
				}
			}
		case *ast.ValueSpec:
			for index, value := range typed.Values {
				if _, isLit := value.(*ast.FuncLit); isLit && index < len(typed.Names) {
					record(typed.Names[index])
				}
			}
		}
		return true
	})
	return bound
}

// constantString returns an expression's value when the type checker knows
// it is a constant string. That covers a literal, a named const, and a
// constant expression -- without this walk having to recognise each form.
func constantString(info *types.Info, expr ast.Expr) (string, bool) {
	typeAndValue, known := info.Types[expr]
	if !known || typeAndValue.Value == nil || typeAndValue.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(typeAndValue.Value), true
}

// analyseFunction records what one function body writes and whom it calls.
func analyseFunction(pkg *packages.Package, fn *ast.FuncDecl) funcFacts {
	facts := funcFacts{writes: map[string]bool{}, calls: map[*types.Func]bool{}}
	info := pkg.TypesInfo
	rangeValues := stringValuesOfRangeVars(info, fn)
	funcValues := packageFuncsBoundToLocals(info, fn)
	localClosures := localsBoundToFuncLiterals(info, fn)

	resolveKey := func(expr ast.Expr) ([]string, bool) {
		if value, known := constantString(info, expr); known {
			return []string{value}, true
		}
		if ident, isIdent := expr.(*ast.Ident); isIdent {
			object := info.Uses[ident]
			if object == nil {
				object = info.Defs[ident]
			}
			if values, known := rangeValues[object]; known {
				return values, true
			}
		}
		return nil, false
	}

	ast.Inspect(fn, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CompositeLit:
			// A literal of the fact-field map, wherever it appears --
			// inline in a struct, assigned to a local, returned, passed as
			// an argument. Matched by TYPE, so all four are one case.
			if !isFactFieldMap(info.TypeOf(typed)) {
				return true
			}
			for _, element := range typed.Elts {
				keyValue, isKV := element.(*ast.KeyValueExpr)
				if !isKV {
					continue
				}
				keys, resolved := resolveKey(keyValue.Key)
				if !resolved {
					facts.dynamicKeySites++
					continue
				}
				for _, key := range keys {
					facts.writes[key] = true
				}
			}
		case *ast.AssignStmt:
			// A string-keyed write into ANYTHING of the fact-field map
			// type: a local, a parameter, a struct field, a map returned by
			// a call. The merged collector could only see a local it had
			// watched being initialised.
			for _, lhs := range typed.Lhs {
				index, isIndex := lhs.(*ast.IndexExpr)
				if !isIndex || !isFactFieldMap(info.TypeOf(index.X)) {
					continue
				}
				keys, resolved := resolveKey(index.Index)
				if !resolved {
					facts.dynamicKeySites++
					continue
				}
				for _, key := range keys {
					facts.writes[key] = true
				}
			}
		case *ast.CallExpr:
			var ident *ast.Ident
			switch fun := typed.Fun.(type) {
			case *ast.Ident:
				ident = fun
			case *ast.SelectorExpr:
				ident = fun.Sel
			}
			if ident == nil {
				facts.unresolvedCallSites++
				return true
			}

			// BUILTINS FIRST. `delete` is a *types.Builtin, not a
			// *types.Func, so an earlier version's *types.Func assertion
			// returned before ever reaching the delete check -- a counter
			// documented as a residual measure that could never fire. A
			// review round constructed it: a provider that wrote a field and
			// then deleted it still reported the field as emitted with the
			// counter at zero. Ordering the checks is the fix.
			if _, isBuiltin := info.Uses[ident].(*types.Builtin); isBuiltin {
				if ident.Name == "delete" && len(typed.Args) > 0 && isFactFieldMap(info.TypeOf(typed.Args[0])) {
					// A delete makes the recorded field set an OVERSTATEMENT.
					// Counted rather than resolved: knowing which key
					// survives needs flow analysis this walk does not do.
					facts.dynamicKeySites++
				}
				return true
			}

			if callee, isFunc := info.Uses[ident].(*types.Func); isFunc {
				if callee.Pkg() != nil && callee.Pkg() == pkg.Types {
					facts.calls[callee] = true
				}
				// A cross-package helper is the named residual in the
				// artifact header, not a countable site: the walk cannot see
				// into it at all, and counting every stdlib call would drown
				// the signal.
				return true
			}

			// A TYPE CONVERSION IS NOT A CALL. `int64(x)` parses as a
			// CallExpr whose Fun names a *types.TypeName. Counting those as
			// unfollowable calls put a dozen fabricated residual sites on
			// every provider -- a counter that OVERSTATES its residual, which
			// is worse than one that cannot fire: it manufactures doubt and
			// buries the real signal in noise.
			if _, isConversion := info.Uses[ident].(*types.TypeName); isConversion {
				return true
			}

			// A CALL THROUGH A VALUE. `emit := writeHidden; emit(fields)`
			// resolves the callee to a *types.Var, so an earlier version
			// queued no edge AND counted nothing -- the provider reported
			// "emits nothing" while emitting. Found by a review round.
			if object, isVar := info.Uses[ident].(*types.Var); isVar {
				// Bound to a package-level function: a followable edge.
				if callee, bound := funcValues[object]; bound {
					facts.calls[callee] = true
					return true
				}
				// A LOCAL CLOSURE NEEDS NO EDGE. `helper := func(...){...}`
				// declares its body INSIDE the function being walked, and
				// ast.Inspect already descends into it, so whatever it writes
				// is recorded under this function. Counting the call site
				// would report an unfollowable edge for a body that was in
				// fact fully read -- the second overstatement found in this
				// counter, after type conversions.
				if localClosures[object] {
					return true
				}
				facts.unresolvedCallSites++
				return true
			}
			facts.unresolvedCallSites++
		}
		return true
	})
	return facts
}

// collectReachableEmissions walks the call graph from each provider's
// methods and unions what every reachable function writes.
func collectReachableEmissions(t *testing.T) map[string]*reachableEmission {
	t.Helper()
	return collectFrom(t, loadProviderPackage(t))
}

// collectFrom is the walk proper, pointed at a loaded package, so the same
// logic runs against the real provider package and against the probe
// fixture. One implementation, two subjects: a fixture that exercised a COPY
// of the walk would pin the copy.
func collectFrom(t *testing.T, pkg *packages.Package) map[string]*reachableEmission {
	t.Helper()
	info := pkg.TypesInfo

	factsOf := map[*types.Func]funcFacts{}
	methodsOfType := map[string][]*types.Func{}
	kindOfType := map[string]string{}

	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			object, isFuncObject := info.Defs[fn.Name].(*types.Func)
			if !isFuncObject {
				continue
			}
			factsOf[object] = analyseFunction(pkg, fn)

			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			receiver := receiverTypeName(fn.Recv.List[0].Type)
			if receiver == "" {
				continue
			}
			methodsOfType[receiver] = append(methodsOfType[receiver], object)
			// The receiver type's fact kind, from its own Capability
			// method's newCapability call.
			ast.Inspect(fn, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall || identName(call.Fun) != "newCapability" || len(call.Args) == 0 {
					return true
				}
				if kind := selectorName(call.Args[0]); kind != "" {
					kindOfType[receiver] = kind
				}
				return true
			})
		}
	}

	if len(kindOfType) == 0 {
		t.Fatal("found no provider capabilities in the package; this walk would be vacuous")
	}

	out := map[string]*reachableEmission{}
	for receiver, kind := range kindOfType {
		emission := &reachableEmission{kind: kind}
		fields := map[string]bool{}
		visited := map[*types.Func]bool{}

		// Transitive closure from every method of the provider type. NOT
		// just Read/Capability: a provider that builds facts in a method
		// this walk did not think to name would be missed, and "the
		// methods I thought of" is the enumeration this walk exists to
		// avoid.
		queue := append([]*types.Func(nil), methodsOfType[receiver]...)
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if visited[current] {
				continue
			}
			visited[current] = true
			facts, known := factsOf[current]
			if !known {
				continue
			}
			for key := range facts.writes {
				fields[key] = true
			}
			emission.dynamicKeySites += facts.dynamicKeySites
			emission.unresolvedCallSites += facts.unresolvedCallSites
			for callee := range facts.calls {
				if !visited[callee] {
					queue = append(queue, callee)
				}
			}
		}

		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		emission.fields = keys
		emission.reachedFuncs = len(visited)
		out[kind] = emission
	}
	return out
}

// TestReachableEmissionArtifactIsRegenerated is the regenerate-and-diff
// guard over the reachability walk.
func TestReachableEmissionArtifactIsRegenerated(t *testing.T) {
	generated := renderReachableEmissions(t)
	recorded, err := os.ReadFile(emissionReachabilityArtifact)
	if err != nil {
		t.Fatalf("reading %s: %v\n\n--- generated ---\n%s", emissionReachabilityArtifact, err, generated)
	}
	if string(recorded) != generated {
		t.Fatalf("%s is stale.\n\n--- generated ---\n%s", emissionReachabilityArtifact, generated)
	}
}

func renderReachableEmissions(t *testing.T) string {
	t.Helper()
	emissions := collectReachableEmissions(t)

	var out strings.Builder
	out.WriteString("# GENERATED by TestReachableEmissionArtifactIsRegenerated. DO NOT EDIT BY\n")
	out.WriteString("# HAND: the test regenerates this file and fails on any difference.\n")
	out.WriteString("#\n")
	out.WriteString("# What each provider EMITS, collected by walking the call graph from its own\n")
	out.WriteString("# methods through the package-local functions it reaches, to any depth, and\n")
	out.WriteString("# recording every statically resolvable key written into anything of the\n")
	out.WriteString("# fact-field map type. Matched by TYPE, so a local, a parameter, a struct\n")
	out.WriteString("# field and a returned map are one case rather than four.\n")
	out.WriteString("#\n")
	out.WriteString("# THE RESIDUAL, NAMED. Three things this walk does not reach:\n")
	out.WriteString("# \"the functions it reaches\" is deliberately narrower than \"every function\".\n")
	out.WriteString("# A direct call is followed; so is a call through a local bound to a\n")
	out.WriteString("# package-level function, and a local closure needs no edge because its body\n")
	out.WriteString("# is already walked. A call whose callee is not statically knowable -- a\n")
	out.WriteString("# function value from a parameter or a field, an interface method -- is NOT\n")
	out.WriteString("# followed and is counted in unresolved_call_sites. An earlier header claimed\n")
	out.WriteString("# EVERY reachable function and was wrong: a review round constructed a\n")
	out.WriteString("# provider emitting through a function value and this file reported it as\n")
	out.WriteString("# emitting nothing. A coverage claim that overclaims is itself a defect.\n")
	out.WriteString("#\n")
	out.WriteString("#   0. a call whose callee is not statically knowable. COUNTED in\n")
	out.WriteString("#      unresolved_call_sites. A `delete` from a fact-field map counts under\n")
	out.WriteString("#      dynamic_key_sites instead: it makes the recorded set an OVERSTATEMENT\n")
	out.WriteString("#      rather than an understatement.\n")
	out.WriteString("#   1. a key that is not statically determined -- built from a variable, a\n")
	out.WriteString("#      format string, or a value read from a row. COUNTED, per provider, in\n")
	out.WriteString("#      the dynamic_key_sites column. A zero there is an OBSERVED zero, and a\n")
	out.WriteString("#      provider that grows a dynamic key shows up as a diff in this file.\n")
	out.WriteString("#   2. a helper in ANOTHER package that writes into a map passed to it.\n")
	out.WriteString("#   3. reflection.\n")
	out.WriteString("# 2 and 3 are not counted, because a count of them would be a count of what\n")
	out.WriteString("# this walk cannot see at all. They are the honest edge of the instrument.\n")
	out.WriteString("#\n")
	out.WriteString("# reached_funcs is how many package-local functions the walk visited from\n")
	out.WriteString("# that provider's methods -- the walk reporting its own reach, so a collapse\n")
	out.WriteString("# in coverage is visible here rather than showing up as a provider that\n")
	out.WriteString("# quietly emits less.\n")
	out.WriteString("#\n")
	out.WriteString("# fact kind / reached_funcs / dynamic_key_sites / unresolved_call_sites /\n")
	out.WriteString("# emitted field keys\n")

	kinds := make([]string, 0, len(emissions))
	for kind := range emissions {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		emission := emissions[kind]
		rendered := "(none)"
		if len(emission.fields) > 0 {
			rendered = strings.Join(emission.fields, " ")
		}
		fmt.Fprintf(&out, "\n%s\n  reached_funcs         %d\n  dynamic_key_sites     %d\n  unresolved_call_sites %d\n  fields                %s\n",
			kind, emission.reachedFuncs, emission.dynamicKeySites, emission.unresolvedCallSites, rendered)
	}
	return out.String()
}

// TestReachabilityWalkSeesEverythingTheMergedCollectorSees is the
// monotonicity oracle between the two collectors.
//
// TWO READERS OF THE SAME THING ARE A LIABILITY UNLESS THEIR DISAGREEMENT
// IS ASSERTED. The merged collector reads two syntactic shapes inside
// method bodies; this one reads the call graph by type. The second must be
// a strict SUPERSET of the first -- it closes a residual, it cannot open
// one -- and asserting that direction is what stops the two from silently
// drifting apart. A field the old collector sees and the new one does not
// is a regression in the new walk, reported by name.
func TestReachabilityWalkSeesEverythingTheMergedCollectorSees(t *testing.T) {
	reachable := collectReachableEmissions(t)
	merged := collectProviderEmissions(t)

	if len(merged) == 0 {
		t.Fatal("the merged collector found no providers; the comparison would be vacuous")
	}
	compared := 0
	for kind, old := range merged {
		found, walked := reachable[kind]
		if !walked {
			t.Errorf("the reachability walk did not find provider %q at all", kind)
			continue
		}
		have := make(map[string]bool, len(found.fields))
		for _, field := range found.fields {
			have[field] = true
		}
		for _, field := range old.fields {
			if !have[field] {
				t.Errorf("%s: the merged collector sees field %q and the reachability walk does not -- a closure that loses a field is a regression, not a closure", kind, field)
			}
		}
		compared++
	}
	if compared != len(merged) {
		t.Fatalf("compared %d of %d providers", compared, len(merged))
	}
}

// TestTheWalkReachesBeyondTheMergedCollector proves the closure is REAL --
// that the walk finds fields the merged collector cannot see.
//
// Without this, a walk that happened to collect exactly the same set would
// pass the superset test above and look like a closure while closing
// nothing. This is the same reason a mutation that turns nothing red is a
// finding rather than a pass.
func TestTheWalkReachesBeyondTheMergedCollector(t *testing.T) {
	reachable := collectReachableEmissions(t)
	merged := collectProviderEmissions(t)

	extra := map[string][]string{}
	for kind, found := range reachable {
		old, known := merged[kind]
		if !known {
			continue
		}
		had := make(map[string]bool, len(old.fields))
		for _, field := range old.fields {
			had[field] = true
		}
		for _, field := range found.fields {
			if !had[field] {
				extra[kind] = append(extra[kind], field)
			}
		}
	}
	if len(extra) == 0 {
		t.Fatal("the reachability walk found NOTHING the merged collector missed. Either the residual it was written to close is not live in this package -- in which case say so and delete this walk -- or the walk is not actually following the call graph. A closure that closes nothing is a finding, not a pass.")
	}
	total := 0
	for _, fields := range extra {
		total += len(fields)
	}
	t.Logf("the reachability walk adds %d field(s) across %d provider(s) that the merged collector cannot see: %v", total, len(extra), extra)
}

// TestEveryProviderIsReachedByTheWalk is the non-vacuity floor.
//
// A provider whose methods were never visited would contribute an empty
// field set that reads exactly like a substrate producer emitting nothing.
// "0 fields" and "the walk never got here" must not look alike, so the
// walk's own reach is asserted rather than reported.
func TestEveryProviderIsReachedByTheWalk(t *testing.T) {
	reachable := collectReachableEmissions(t)
	capabilities := liveCapabilities(t)

	if len(reachable) == 0 {
		t.Fatal("the walk found no providers")
	}
	checked := 0
	for kind := range capabilities {
		constantName := factKindConstantName(string(kind))
		emission, walked := reachable[constantName]
		if !walked {
			t.Errorf("registered fact kind %q (%s) was not reached by the walk -- an unreached provider reports an empty emission set that is indistinguishable from a substrate producer", kind, constantName)
			continue
		}
		if emission.reachedFuncs == 0 {
			t.Errorf("%s: the walk visited zero functions, so its empty field set proves nothing", constantName)
		}
		checked++
	}
	if checked != len(capabilities) {
		t.Fatalf("checked %d of %d registered capabilities", checked, len(capabilities))
	}
}

// The fixture package the walk is pinned against. Every shape in it is one a
// review round constructed and the walk got wrong; see its package comment.
const emissionProbePackage = "./testdata/emissionprobe"

// walkProbeFixture runs the same collection logic against the fixture package
// instead of the provider package.
//
// It exists because the REAL registry cannot exhibit these shapes on demand:
// pinning the walk's behaviour against production alone means the pin
// disappears the day a provider is refactored. The fixture is where the
// shapes live permanently.
func walkProbeFixture(t *testing.T) map[string]*reachableEmission {
	t.Helper()
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
	}, emissionProbePackage)
	if err != nil || len(loaded) != 1 || len(loaded[0].Errors) > 0 {
		t.Fatalf("loading the probe fixture: err=%v pkgs=%d", err, len(loaded))
	}
	return collectFrom(t, loaded[0])
}

// TestTheWalkFollowsACallThroughAFunctionValue pins the first defect a review
// round found in this walk.
//
// `emit := writeViaFuncValue; emit(fields)` resolves the callee to a
// *types.Var, so the original walk queued no edge, and because the helper has
// no receiver it was not a root either. The provider emitted a field and this
// file reported it as emitting NOTHING -- the worst possible direction for an
// oracle, since an empty set reads exactly like a substrate producer.
func TestTheWalkFollowsACallThroughAFunctionValue(t *testing.T) {
	probes := walkProbeFixture(t)

	direct, walked := probes["ProbeDirect"]
	if !walked {
		t.Fatal("the fixture's direct-call provider was not walked at all")
	}
	if !containsField(direct.fields, "direct_field") {
		t.Errorf("the direct call was not followed: %v -- the control case is broken, so the case below proves nothing", direct.fields)
	}

	viaValue, walked := probes["ProbeFuncValue"]
	if !walked {
		t.Fatal("the fixture's function-value provider was not walked at all")
	}
	if !containsField(viaValue.fields, "func_value_field") {
		t.Errorf("a field emitted through a function value bound to a package-level function was NOT recorded: %v", viaValue.fields)
	}
}

// TestTheWalkCountsACallItCannotResolve pins the honest half.
//
// A function value that arrives as a PARAMETER is not statically knowable and
// no static walk can follow it. The requirement is not that the walk follow it
// -- it is that the walk SAY SO. An uncounted limit is indistinguishable from
// an absence, which is the whole reason this file carries counters rather than
// prose.
func TestTheWalkCountsACallItCannotResolve(t *testing.T) {
	probes := walkProbeFixture(t)
	indirect, walked := probes["ProbeIndirect"]
	if !walked {
		t.Fatal("the fixture's indirect provider was not walked at all")
	}
	if indirect.unresolvedCallSites == 0 {
		t.Errorf("a call through a parameter-supplied function value was neither followed nor counted -- the walk passed over it silently, reporting fields %v", indirect.fields)
	}
}

// TestTheWalkCountsADeleteFromAFactFieldMap pins the second defect.
//
// `delete` is a *types.Builtin, and the original code asserted *types.Func
// first, so the delete branch it documented as a residual measure could never
// execute. A provider that wrote a field and then deleted it reported the
// field as emitted with the counter at zero: a claim that was not merely
// incomplete but WRONG, and a counter that could not fire.
func TestTheWalkCountsADeleteFromAFactFieldMap(t *testing.T) {
	probes := walkProbeFixture(t)
	deleted, walked := probes["ProbeDeleted"]
	if !walked {
		t.Fatal("the fixture's delete provider was not walked at all")
	}
	if deleted.dynamicKeySites == 0 {
		t.Error("a delete from a fact-field map was not counted -- the recorded field set overstates what the provider emits and nothing says so")
	}
}

// TestALocalClosureCallIsNotCountedAsUnresolved guards the counter against
// OVERSTATING, which is the failure mode the residual columns are most prone
// to and the one that is hardest to notice.
//
// A local closure's body is walked already, so its call site is fully
// accounted for. An earlier version counted it, putting a dozen fabricated
// residual sites on every provider -- a number that reads as doubt about
// coverage that does not exist. The live registry's true count is small and
// the fixture is what keeps the tier reachable.
func TestALocalClosureCallIsNotCountedAsUnresolved(t *testing.T) {
	probes := walkProbeFixture(t)
	direct, walked := probes["ProbeDirect"]
	if !walked {
		t.Fatal("the fixture's direct provider was not walked at all")
	}
	if direct.unresolvedCallSites != 0 {
		t.Errorf("the direct-call provider reports %d unresolved call sites; it has none, so the counter is overstating", direct.unresolvedCallSites)
	}
}

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
