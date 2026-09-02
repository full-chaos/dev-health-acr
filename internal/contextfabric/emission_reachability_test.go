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

// THE CLOSURE RULE, stated before the code that implements it.
//
// A provider's emitted-field set is the union of the resolved field-key
// writes over the LEAST FIXED POINT of:
//
//	(1) the provider type's own methods;
//	(2) for every reached function, every callee that resolves to a
//	    package-local function whose BODY THIS WALK CAN INSPECT.
//
// Everything else is a LEAK, and every leak is counted.
//
// WHY THE RULE IS WRITTEN DOWN AND THE WALK DERIVED FROM IT. Three review
// rounds each found a defect in this walk, and each fix seeded the next:
// map literals only, then a missing function-value edge, then a delete
// counter that could not fire, then two counters that overstated, then an
// interface method silently dropped. Every one of those was a MISSING ARM in
// a hand-written ladder of syntactic cases, which is a structure that can
// only ever be as complete as the author's imagination. So the ladder is
// replaced by a CLASSIFICATION over a closed vocabulary: every call site
// gets exactly one disposition, the totality and exclusivity of that is
// asserted, and each disposition owes a fixture that lands in it. A case
// nobody thought of now falls into `leaked` and is COUNTED, instead of
// falling through a ladder and being silently lost.

// callDisposition is what the walk did with one call site. Closed, total and
// mutually exclusive over every *ast.CallExpr in a walked body.
type callDisposition string

const (
	// callFollowed: a package-local function or method whose body the walk
	// can inspect, reached through a concrete receiver. The edge is taken.
	callFollowed callDisposition = "followed"
	// callAccounted: a closure literal whose body is traversed lexically as
	// a child of a followed body, so its writes are ALREADY recorded and no
	// edge is needed. A closure defined locally and passed OUT to another
	// package is still `accounted` FOR ITS OWN BODY -- what it writes here
	// is visible here, whoever ends up calling it.
	callAccounted callDisposition = "accounted"
	// callNotACall: a type conversion. `int64(x)` parses as a CallExpr and
	// is not a call at all.
	callNotACall callDisposition = "not_a_call"
	// callExcludedByDesign: a function in ANOTHER package. The walk cannot
	// see into it and says so in the header; counting every stdlib call
	// would drown the signal.
	callExcludedByDesign callDisposition = "excluded_by_design"
	// callLeaked: everything else. A field written behind one of these is
	// invisible to the walk, so the count is the honest measure of how much
	// of a provider was not seen.
	callLeaked callDisposition = "leaked"
)

var callDispositions = [...]callDisposition{
	callFollowed, callAccounted, callNotACall, callExcludedByDesign, callLeaked,
}

// CallDispositionCount is five.
const CallDispositionCount = len(callDispositions)

// leakCause names WHY a call leaked. One `leaked` counter carries the total,
// and this breaks it down -- the same shape the requirement layer uses for
// its unavailable reasons, and for the same reason: a bare count is a number
// the next reader cannot act on.
type leakCause string

const (
	// leakInterfaceMethod: the static receiver type is an interface, so
	// which body runs is not knowable here. This is a leak EVEN WHEN only
	// one local implementation exists -- a "single implementation" shortcut
	// is a guess that silently becomes wrong the day a second one lands.
	leakInterfaceMethod leakCause = "interface_method"
	// leakExternalFuncValue: a function value that ARRIVES from outside the
	// function -- a parameter, a struct field, a map, a global.
	leakExternalFuncValue leakCause = "external_func_value"
	// leakNoBody: a package-local function with no inspectable declaration
	// (an assembly or linkname stub).
	leakNoBody leakCause = "no_body"
	// leakUnresolvable: no identifier at all, or an object kind this
	// vocabulary does not name. The catch-all that keeps the
	// classification TOTAL.
	leakUnresolvable leakCause = "unresolvable"
)

var leakCauses = [...]leakCause{
	leakInterfaceMethod, leakExternalFuncValue, leakNoBody, leakUnresolvable,
}

// LeakCauseCount is four.
const LeakCauseCount = len(leakCauses)

// callSite is one classified call.
type callSite struct {
	disposition callDisposition
	// cause is set only when disposition is callLeaked.
	cause leakCause
	// callee is set only when disposition is callFollowed.
	callee *types.Func
}

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
	// leakedCallSites is the total of the `leaked` disposition: edges the
	// walk could not follow, so a field written behind one is invisible.
	// COUNTED for the same reason dynamicKeySites is -- a limit that is not
	// counted is indistinguishable from an absence.
	leakedCallSites int
	// leaksByCause breaks that total down, indexed by leakCauses position.
	leaksByCause [LeakCauseCount]int
	// dispositions counts every call site by disposition, indexed by
	// callDispositions position. Reported so the classification's own
	// coverage is visible: a walk that suddenly follows nothing shows up
	// here rather than as a provider that quietly emits less.
	dispositions [CallDispositionCount]int
	// reachedFuncs is how many package-local functions the walk visited
	// from this provider's methods. A provider whose helpers were somehow
	// not followed would show a suspiciously small number, so the walk's
	// own reach is reported rather than assumed.
	reachedFuncs int
}

// funcFacts is what one function does, independent of who calls it.
type funcFacts struct {
	writes          map[string]bool
	dynamicKeySites int
	// sites is every classified call in this body, in source order. Kept
	// whole rather than pre-summed so the totality assertion can count them.
	sites []callSite
	calls map[*types.Func]bool
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

// classifyCall assigns exactly ONE disposition to a call site.
//
// THE ORDER OF THESE TESTS IS THE RULE, and each step is written so that
// falling past it is safe: anything unrecognised reaches the final `leaked`
// return and is COUNTED. There is no path out of this function that neither
// follows an edge nor records a leak, which is what the totality assertion
// checks.
func classifyCall(pkg *packages.Package, call *ast.CallExpr, funcValues map[types.Object]*types.Func, localClosures map[types.Object]bool, bodied map[*types.Func]bool) callSite {
	info := pkg.TypesInfo

	// An immediately-invoked literal: its body is a child of this one and
	// ast.Inspect has already read it.
	if _, isLiteral := call.Fun.(*ast.FuncLit); isLiteral {
		return callSite{disposition: callAccounted}
	}

	var ident *ast.Ident
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		ident = fun
	case *ast.SelectorExpr:
		ident = fun.Sel
	}
	if ident == nil {
		return callSite{disposition: callLeaked, cause: leakUnresolvable}
	}

	// A METHOD ON AN INTERFACE IS A LEAK, always. The selection's receiver
	// type decides, not the callee object: an interface method resolves to a
	// perfectly good *types.Func, which is exactly how the previous version
	// queued it as followable and then dropped it for having no body --
	// silently, with the counter at zero. No "only one implementation
	// exists" shortcut: that is a guess that becomes wrong the day a second
	// implementation lands, and it would become wrong quietly.
	if isSelector {
		if selection := info.Selections[selector]; selection != nil && types.IsInterface(selection.Recv()) {
			return callSite{disposition: callLeaked, cause: leakInterfaceMethod}
		}
	}

	switch object := info.Uses[ident].(type) {
	case *types.Builtin:
		// Builtins have no body to follow. `delete`'s effect on the field
		// set is recorded by the caller, not here.
		return callSite{disposition: callExcludedByDesign}
	case *types.TypeName:
		// A conversion is not a call.
		return callSite{disposition: callNotACall}
	case *types.Func:
		if object.Pkg() == nil || object.Pkg() != pkg.Types {
			return callSite{disposition: callExcludedByDesign}
		}
		if !bodied[object] {
			return callSite{disposition: callLeaked, cause: leakNoBody}
		}
		return callSite{disposition: callFollowed, callee: object}
	case *types.Var:
		// A local bound to a package-level function: a followable edge.
		if callee, bound := funcValues[object]; bound {
			if !bodied[callee] {
				return callSite{disposition: callLeaked, cause: leakNoBody}
			}
			return callSite{disposition: callFollowed, callee: callee}
		}
		// A closure literal declared HERE: its body is walked lexically, so
		// its writes are already recorded and no edge is needed.
		if localClosures[object] {
			return callSite{disposition: callAccounted}
		}
		// A function value that ARRIVED from outside -- a parameter, a
		// struct field, a map, a global. Which body runs is not knowable.
		return callSite{disposition: callLeaked, cause: leakExternalFuncValue}
	}
	return callSite{disposition: callLeaked, cause: leakUnresolvable}
}

// isFactFieldDelete reports a `delete` whose target is a fact-field map.
func isFactFieldDelete(info *types.Info, call *ast.CallExpr) bool {
	ident, isIdent := call.Fun.(*ast.Ident)
	if !isIdent || ident.Name != "delete" || len(call.Args) == 0 {
		return false
	}
	if _, isBuiltin := info.Uses[ident].(*types.Builtin); !isBuiltin {
		return false
	}
	return isFactFieldMap(info.TypeOf(call.Args[0]))
}

func dispositionIndex(value callDisposition) (int, bool) {
	for index, member := range callDispositions {
		if member == value {
			return index, true
		}
	}
	return 0, false
}

func leakCauseIndex(value leakCause) (int, bool) {
	for index, member := range leakCauses {
		if member == value {
			return index, true
		}
	}
	return 0, false
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
func analyseFunction(pkg *packages.Package, fn *ast.FuncDecl, bodied map[*types.Func]bool) funcFacts {
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
			// ONE classification, no ladder. Every call site leaves here
			// with exactly one disposition; classifyCall is where the rule
			// lives and the only place a new case may be added.
			site := classifyCall(pkg, typed, funcValues, localClosures, bodied)
			facts.sites = append(facts.sites, site)
			switch site.disposition {
			case callFollowed:
				facts.calls[site.callee] = true
			case callNotACall, callAccounted, callExcludedByDesign, callLeaked:
				// Nothing to queue. A `delete` from a fact-field map is a
				// WRITE-side effect, not an edge, and is counted below.
			}
			if isFactFieldDelete(pkg.TypesInfo, typed) {
				// A delete makes the recorded field set an OVERSTATEMENT.
				// Counted rather than resolved: knowing which key survives
				// needs flow analysis this walk does not do.
				facts.dynamicKeySites++
			}
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

	// PASS 1: which package-local functions have a body this walk can
	// inspect? The classification needs this to tell `followed` from a
	// `no_body` leak, so it cannot be discovered while classifying.
	bodied := map[*types.Func]bool{}
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			if object, isFuncObject := info.Defs[fn.Name].(*types.Func); isFuncObject {
				bodied[object] = true
			}
		}
	}

	// PASS 2: classify and collect.
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
			factsOf[object] = analyseFunction(pkg, fn, bodied)

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
			for _, site := range facts.sites {
				if index, known := dispositionIndex(site.disposition); known {
					emission.dispositions[index]++
				}
				if site.disposition != callLeaked {
					continue
				}
				emission.leakedCallSites++
				if index, known := leakCauseIndex(site.cause); known {
					emission.leaksByCause[index]++
				}
			}
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
	out.WriteString("# THE CLOSURE RULE. A provider's emitted-field set is the union of the\n")
	out.WriteString("# resolved field-key writes over the least fixed point of (1) the provider\n")
	out.WriteString("# type's own methods and (2) for every reached function, every callee that\n")
	out.WriteString("# resolves to a package-local function whose BODY THIS WALK CAN INSPECT.\n")
	out.WriteString("# Everything else is a LEAK, and every leak is counted.\n")
	out.WriteString("#\n")
	out.WriteString("# Writes are matched by TYPE, so a local, a parameter, a struct field and a\n")
	out.WriteString("# returned map are one case rather than four.\n")
	out.WriteString("#\n")
	out.WriteString("# EVERY CALL SITE IS CLASSIFIED into exactly one disposition, and that\n")
	out.WriteString("# totality is asserted rather than assumed:\n")
	out.WriteString("#   followed            an edge taken into a package-local body\n")
	out.WriteString("#   accounted           a closure literal whose body is walked lexically, so\n")
	out.WriteString("#                       its writes are already recorded and no edge is needed\n")
	out.WriteString("#   not_a_call          a type conversion, which merely parses as a call\n")
	out.WriteString("#   excluded_by_design  a function in another package, or a builtin\n")
	out.WriteString("#   leaked              everything else -- see the per-cause breakdown\n")
	out.WriteString("#\n")
	out.WriteString("# WHAT A LEAK MEANS. A field written behind a leaked call is INVISIBLE to\n")
	out.WriteString("# this walk. The count is the honest measure of how much of a provider was\n")
	out.WriteString("# not seen -- not a defect list, and not a promise that the rest is\n")
	out.WriteString("# complete. An interface method is a leak even when exactly one local\n")
	out.WriteString("# implementation exists: a single-implementation shortcut is a guess that\n")
	out.WriteString("# goes quietly wrong the day a second one lands.\n")
	out.WriteString("#\n")
	out.WriteString("# dynamic_key_sites counts writes whose KEY is not statically determined,\n")
	out.WriteString("# plus every `delete` from a fact-field map -- a delete makes the recorded\n")
	out.WriteString("# set an OVERSTATEMENT rather than an understatement.\n")
	out.WriteString("#\n")
	out.WriteString("# A zero in any counter is an OBSERVED zero: every closed token is printed\n")
	out.WriteString("# on every provider, so a tier nothing reached is visibly empty rather than\n")
	out.WriteString("# absent. reached_funcs is the walk reporting its own coverage, so a\n")
	out.WriteString("# collapse shows up here and not as a provider that quietly emits less.\n")
	out.WriteString("#\n")
	out.WriteString("# This header claims what the disposition vocabulary proves and nothing\n")
	out.WriteString("# more. Two earlier versions overclaimed -- one said the walk follows every\n")
	out.WriteString("# package-local function, one said interface methods are counted while the\n")
	out.WriteString("# code silently dropped them -- and each was found by a review round. A\n")
	out.WriteString("# coverage claim that overclaims is itself a defect.\n")
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
		fmt.Fprintf(&out, "\n%s\n  reached_funcs      %d\n  dynamic_key_sites  %d\n  leaked_call_sites  %d\n",
			kind, emission.reachedFuncs, emission.dynamicKeySites, emission.leakedCallSites)
		for index, cause := range leakCauses {
			fmt.Fprintf(&out, "    leak %-20s %d\n", cause, emission.leaksByCause[index])
		}
		for index, disposition := range callDispositions {
			fmt.Fprintf(&out, "    calls %-19s %d\n", disposition, emission.dispositions[index])
		}
		fmt.Fprintf(&out, "  fields             %s\n", rendered)
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
	if indirect.leakedCallSites == 0 {
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
	if direct.leakedCallSites != 0 {
		t.Errorf("the direct-call provider reports %d leaked call sites; it has none, so the counter is overstating", direct.leakedCallSites)
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

// TestEveryCallSiteGetsExactlyOneDisposition is the TOTALITY AND EXCLUSIVITY
// assertion, and it is the property a hand-written ladder of syntactic cases
// could never state about itself.
//
// Every `*ast.CallExpr` in every walked body must leave `classifyCall` with
// exactly one disposition from the closed vocabulary. A case nobody thought
// of cannot fall through: it reaches the final `leaked` return and is
// counted. The check is that the classified population equals the syntactic
// population -- so a future edit that adds an early `return` without a
// disposition fails here rather than silently dropping call sites.
func TestEveryCallSiteGetsExactlyOneDisposition(t *testing.T) {
	pkg := loadProviderPackage(t)
	info := pkg.TypesInfo

	bodied := map[*types.Func]bool{}
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			if object, ok := info.Defs[fn.Name].(*types.Func); ok {
				bodied[object] = true
			}
		}
	}

	syntactic, classified := 0, 0
	valid := map[callDisposition]bool{}
	for _, member := range callDispositions {
		valid[member] = true
	}
	perDisposition := map[callDisposition]int{}

	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				if _, isCall := node.(*ast.CallExpr); isCall {
					syntactic++
				}
				return true
			})
			facts := analyseFunction(pkg, fn, bodied)
			for _, site := range facts.sites {
				if !valid[site.disposition] {
					t.Errorf("a call site got disposition %q, which is not in the closed vocabulary", site.disposition)
					continue
				}
				if site.disposition == callLeaked && site.cause == "" {
					t.Error("a leaked call site named no cause -- the breakdown cannot account for it")
				}
				if site.disposition == callFollowed && site.callee == nil {
					t.Error("a followed call site named no callee")
				}
				perDisposition[site.disposition]++
				classified++
			}
		}
	}
	if syntactic == 0 {
		t.Fatal("the provider package contains no call expressions; this assertion would be vacuous")
	}
	if classified != syntactic {
		t.Errorf("%d call expressions in the package, %d classified -- %d call sites left the classifier with no disposition and are silently unaccounted for",
			syntactic, classified, syntactic-classified)
	}
	t.Logf("classified %d call sites: %v", classified, perDisposition)
}

// dispositionFixtures maps each disposition to the fixture provider that
// lands in it, and to the field that provider emits (empty for the leak
// dispositions, where the point is that the field is NOT recovered).
//
// THE FIXTURE SET IS DERIVED FROM THE VOCABULARY, not from the shapes the
// author happened to think of. That is the structural fix for the two
// defects a review round found in the previous repair: the type-conversion
// arm and the local-closure arm each had no fixture, because the fixtures
// were an ad-hoc list. A disposition added to the vocabulary without a
// fixture now fails by name.
var dispositionFixtures = map[callDisposition]struct {
	provider string
	// saltedField is the uniquely-named field the fixture emits THROUGH
	// this disposition. Non-leak dispositions must recover it; that is the
	// salted positive, so a disposition that silently drops writes fails by
	// name rather than reporting a healthy empty set.
	saltedField string
}{
	callFollowed:         {provider: "ProbeDirect", saltedField: "direct_field"},
	callAccounted:        {provider: "ProbeClosure", saltedField: "closure_field"},
	callNotACall:         {provider: "ProbeConversion", saltedField: "conversion_field"},
	callExcludedByDesign: {provider: "ProbeCrossPackage", saltedField: "cross_package_field"},
	callLeaked:           {provider: "ProbeIndirect"},
}

// TestEveryDispositionHasAFixtureThatLandsInIt quantifies over the
// vocabulary rather than over a list.
func TestEveryDispositionHasAFixtureThatLandsInIt(t *testing.T) {
	probes := walkProbeFixture(t)
	checked := 0
	for _, disposition := range callDispositions {
		fixture, declared := dispositionFixtures[disposition]
		if !declared {
			t.Errorf("disposition %q has no fixture: a disposition nothing lands in can be dead for its whole life and read as green", disposition)
			continue
		}
		emission, walked := probes[fixture.provider]
		if !walked {
			t.Errorf("disposition %q names fixture provider %q, which the walk did not find", disposition, fixture.provider)
			continue
		}
		index, known := dispositionIndex(disposition)
		if !known {
			t.Errorf("disposition %q is not in its own vocabulary", disposition)
			continue
		}
		if emission.dispositions[index] == 0 {
			t.Errorf("fixture %q was supposed to land in disposition %q and did not -- the fixture no longer exercises the case it is named for", fixture.provider, disposition)
		}
		checked++
	}
	if checked != CallDispositionCount {
		t.Fatalf("checked %d of %d dispositions", checked, CallDispositionCount)
	}
}

// TestEveryNonLeakDispositionRecoversItsSaltedField is the salted positive,
// one per disposition.
//
// A disposition can land correctly and still lose the write behind it --
// that is precisely what interface dispatch did, and the artifact showed a
// confident empty field list. So every non-leak disposition carries a
// uniquely-named field that MUST come back. A disposition that silently
// drops writes now fails by name instead of reporting a healthy zero.
func TestEveryNonLeakDispositionRecoversItsSaltedField(t *testing.T) {
	probes := walkProbeFixture(t)
	checked := 0
	for _, disposition := range callDispositions {
		fixture := dispositionFixtures[disposition]
		if fixture.saltedField == "" {
			continue // a leak disposition: the point is that it does NOT recover
		}
		emission, walked := probes[fixture.provider]
		if !walked {
			t.Errorf("fixture provider %q for disposition %q was not walked", fixture.provider, disposition)
			continue
		}
		if !containsField(emission.fields, fixture.saltedField) {
			t.Errorf("disposition %q did not recover its salted field %q -- the write behind that disposition is being dropped; got %v",
				disposition, fixture.saltedField, emission.fields)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no salted field reached the assertions")
	}
}

// TestTheInterfaceDispositionLeaksAndIsCounted pins the defect the
// re-derivation exists to make structural.
//
// It is NOT that the walk should follow the interface method -- it cannot,
// and pretending otherwise via a single-implementation shortcut would be a
// guess that goes quietly wrong. It is that the field must be NAMED as lost
// rather than silently absent.
func TestTheInterfaceDispositionLeaksAndIsCounted(t *testing.T) {
	probes := walkProbeFixture(t)
	emission, walked := probes["ProbeInterface"]
	if !walked {
		t.Fatal("the interface fixture provider was not walked at all")
	}
	if containsField(emission.fields, "interface_field") {
		t.Error("the walk claims to have recovered a field written behind an interface method; it cannot know that")
	}
	index, _ := leakCauseIndex(leakInterfaceMethod)
	if emission.leaksByCause[index] == 0 {
		t.Error("an interface-method call was neither followed nor counted as a leak -- the field is silently missing, which is the defect this rule exists to prevent")
	}
}
