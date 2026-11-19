package api

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/sourceload"
	"github.com/full-chaos/dev-health-acr/internal/releasebuild"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// paritytestDir holds engine compositions for tests only; its importers are
// checked to be test files, so it is not a production path.
const paritytestDir = "internal/contextfabric/pginvestigation/paritytest"

const (
	acrModulePath        = "github.com/full-chaos/dev-health-acr"
	resolverPackage      = acrModulePath + "/internal/contextfabric/modelruntimeresolver"
	contextFabricPackage = acrModulePath + "/internal/contextfabric"
	paritytestPackage    = acrModulePath + "/" + paritytestDir
	plantedPackages      = acrModulePath + "/internal/api/testdata/platformmodelpath"
)

var platformModelCallNames = []string{"InterpretQuestion", "SynthesizeAnswer", "PhraseStructureOffers"}

// platformModelPathSites is every production site, as "file:function", of the
// path that can serve the platform model: the per-organization resolver whose
// deploymentDefault is the one place the platform model answers, the engine it
// is composed into, the in-process Investigate call that drives the engine,
// and any model runtime call made outside the engine's own tree.
type platformModelPathSites struct {
	resolverConstructions []string
	engineConstructions   []string
	investigateCalls      []string
	modelCallsOutside     []string
	paritytestImporters   []string
}

const (
	resolverSite = iota
	engineSite
	investigateSite
	modelCallSite
	paritytestSite
)

func (s *platformModelPathSites) list(kind int) *[]string {
	return [...]*[]string{&s.resolverConstructions, &s.engineConstructions, &s.investigateCalls, &s.modelCallsOutside, &s.paritytestImporters}[kind]
}

type platformModelPathSite struct {
	kind int
	site string
}

type platformModelPathTargetScan struct {
	counts   map[platformModelPathSite]int
	problems []string
	loaded   map[string]bool
	err      error
}

// scanPlatformModelPath type-checks the non-test files of the packages
// matching patterns once for each release GOOS and returns every site, each
// counted as often as the build that holds it most often has it, and the files
// some build loaded. Sites are found by the objects identifiers resolve to, so
// an import alias, a dot import, a shadowing local or a same-named object from
// another package cannot hide or fake one.
func scanPlatformModelPath(t *testing.T, root string, patterns []string) (platformModelPathSites, map[string]bool) {
	t.Helper()
	targets := platformModelPathTargets()
	results := make([]platformModelPathTargetScan, len(targets))
	var wait sync.WaitGroup
	for i, target := range targets {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[i] = scanPlatformModelPathTarget(root, patterns, target)
		}()
	}
	wait.Wait()
	counts := map[platformModelPathSite]int{}
	loaded := map[string]bool{}
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("load %v: %v", patterns, result.err)
		}
		for _, problem := range result.problems {
			t.Error(problem)
		}
		for name := range result.loaded {
			loaded[name] = true
		}
		for key, count := range result.counts {
			counts[key] = max(counts[key], count)
		}
	}
	var sites platformModelPathSites
	for key, count := range counts {
		for range count {
			*sites.list(key.kind) = append(*sites.list(key.kind), key.site)
		}
	}
	for kind := resolverSite; kind <= paritytestSite; kind++ {
		slices.Sort(*sites.list(kind))
	}
	return sites, loaded
}

func scanPlatformModelPathTarget(root string, patterns []string, target [2]string) platformModelPathTargetScan {
	loader, err := sourceload.Load(root, patterns, target[0], target[1])
	if err != nil {
		return platformModelPathTargetScan{err: err}
	}
	scan := platformModelPathTargetScan{counts: map[platformModelPathSite]int{}, loaded: map[string]bool{}, problems: loader.Problems(target[0], target[1])}
	for _, pkg := range loader.Roots {
		for _, name := range pkg.GoFiles {
			scan.loaded[filepath.Clean(name)] = true
		}
		if inPackageTree(pkg.PkgPath, paritytestPackage) {
			continue
		}
		unit := loader.Units[pkg.ID]
		inResolverPackage := inPackageTree(pkg.PkgPath, resolverPackage)
		inContextFabric := inPackageTree(pkg.PkgPath, contextFabricPackage)
		for _, file := range unit.Files {
			rel, err := filepath.Rel(root, loader.Fset.Position(file.Pos()).Filename)
			if err != nil {
				return platformModelPathTargetScan{err: err}
			}
			rel = filepath.ToSlash(rel)
			for _, spec := range file.Imports {
				if importPath, _ := strconv.Unquote(spec.Path.Value); inPackageTree(importPath, paritytestPackage) {
					scan.counts[platformModelPathSite{paritytestSite, rel}]++
				}
			}
			for _, decl := range file.Decls {
				site := rel + ":package-level"
				if fn, ok := decl.(*ast.FuncDecl); ok {
					site = rel + ":" + fn.Name.Name
				}
				ast.Inspect(decl, func(node ast.Node) bool {
					for _, kind := range platformModelPathKinds(unit.Info, node, inResolverPackage, inContextFabric) {
						scan.counts[platformModelPathSite{kind, site}]++
					}
					return true
				})
			}
		}
	}
	return scan
}

// platformModelPathKinds is the site kinds one node is. A reference to New,
// NewEngine, an Investigate method or a model runtime method is a site whether
// it is called or taken as a value. A Resolver value is a construction wherever
// one comes into being outside its package: its fields are exported, so a
// literal (elided or not), a variable, parameter, result or field whose type
// holds a Resolver by value, new and make each make a working resolver.
func platformModelPathKinds(info *types.Info, node ast.Node, inResolverPackage, inContextFabric bool) []int {
	var kinds []int
	switch n := node.(type) {
	case *ast.Ident:
		if fn, ok := info.Uses[n].(*types.Func); ok {
			switch {
			case isPackageFunc(fn, resolverPackage, "New") && !inResolverPackage:
				kinds = append(kinds, resolverSite)
			case isPackageFunc(fn, contextFabricPackage, "NewEngine"):
				kinds = append(kinds, engineSite)
			case fn.Name() == "Investigate" && isGuardedFunc(fn):
				kinds = append(kinds, investigateSite)
			case !inContextFabric && slices.Contains(platformModelCallNames, fn.Name()) && isGuardedFunc(fn):
				kinds = append(kinds, modelCallSite)
			}
		}
		if variable, ok := info.Defs[n].(*types.Var); ok && !inResolverPackage && holdsResolverValue(variable.Type()) {
			kinds = append(kinds, resolverSite)
		}
	case *ast.FuncType:
		// A parameter or result without a name defines no object; its type
		// is the only trace of the value the signature carries.
		for _, fields := range []*ast.FieldList{n.Params, n.Results} {
			if fields == nil || inResolverPackage {
				continue
			}
			for _, field := range fields.List {
				typ := field.Type
				if variadic, ok := typ.(*ast.Ellipsis); ok {
					typ = variadic.Elt
				}
				if tv, ok := info.Types[typ]; ok && len(field.Names) == 0 && holdsResolverValue(tv.Type) {
					kinds = append(kinds, resolverSite)
				}
			}
		}
	case *ast.CompositeLit:
		tv, ok := info.Types[n]
		if !ok || inResolverPackage {
			break
		}
		literalType := tv.Type
		// An elided literal in a container of pointers is the literal of
		// the pointed-to type, and its address is stored.
		if pointer, ok := types.Unalias(literalType).Underlying().(*types.Pointer); ok && n.Type == nil {
			literalType = pointer.Elem()
		}
		if holdsResolverValue(literalType) {
			kinds = append(kinds, resolverSite)
		}
	case *ast.CallExpr:
		if inResolverPackage {
			break
		}
		builtin, ok := info.Uses[calledIdent(n.Fun)].(*types.Builtin)
		if !ok || len(n.Args) == 0 {
			break
		}
		switch builtin.Name() {
		case "new":
			if tv, ok := info.Types[n.Args[0]]; ok && holdsResolverValue(tv.Type) {
				kinds = append(kinds, resolverSite)
			}
		case "make":
			if tv, ok := info.Types[n]; ok && holdsResolverValue(tv.Type) {
				kinds = append(kinds, resolverSite)
			}
		}
	}
	return kinds
}

func calledIdent(expr ast.Expr) *ast.Ident {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	ident, _ := expr.(*ast.Ident)
	return ident
}

func inPackageTree(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func isPackageFunc(fn *types.Func, pkgPath, name string) bool {
	return fn.Pkg() != nil && fn.Pkg().Path() == pkgPath && fn.Name() == name && fn.Signature().Recv() == nil
}

// isGuardedFunc reports whether fn can be the module's own: a function or
// method the module declares, or an interface method, which a module type may
// implement. The planted packages under testdata stand for code outside the
// module.
func isGuardedFunc(fn *types.Func) bool {
	if recv := fn.Signature().Recv(); recv != nil && types.IsInterface(recv.Type()) {
		return true
	}
	return fn.Pkg() != nil && inPackageTree(fn.Pkg().Path(), acrModulePath) && !inPackageTree(fn.Pkg().Path(), plantedPackages)
}

// holdsResolverValue reports whether a value of type t holds a Resolver by
// value: t is Resolver, or an array, slice, map, channel, struct or type
// parameter constraint over such a type. A pointer or a function does not.
func holdsResolverValue(t types.Type) bool {
	return holdsResolver(t, map[types.Type]bool{})
}

func holdsResolver(t types.Type, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Named:
		if obj := u.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == resolverPackage && obj.Name() == "Resolver" {
			return true
		}
		return holdsResolver(u.Underlying(), seen)
	case *types.TypeParam:
		return holdsResolver(u.Constraint(), seen)
	case *types.Interface:
		for i := range u.NumEmbeddeds() {
			if holdsResolver(u.EmbeddedType(i), seen) {
				return true
			}
		}
	case *types.Union:
		for i := range u.Len() {
			if holdsResolver(u.Term(i).Type(), seen) {
				return true
			}
		}
	case *types.Array:
		return holdsResolver(u.Elem(), seen)
	case *types.Slice:
		return holdsResolver(u.Elem(), seen)
	case *types.Map:
		return holdsResolver(u.Key(), seen) || holdsResolver(u.Elem(), seen)
	case *types.Chan:
		return holdsResolver(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if holdsResolver(u.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

// platformModelPathTargets is one GOOS/GOARCH pair for each GOOS the release
// matrix ships.
func platformModelPathTargets() [][2]string {
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

// platformModelPathNames is every name a site needs in its text: a production
// file outside every release build that holds none of them cannot hold a site.
var platformModelPathNames = [][]byte{
	[]byte("modelruntimeresolver"), []byte("NewEngine"), []byte("Investigate"),
	[]byte("InterpretQuestion"), []byte("SynthesizeAnswer"), []byte("PhraseStructureOffers"), []byte(paritytestDir),
}

// filesOutsideEveryBuild is every production .go file under root that no
// release build loaded and whose text names a site: the type-checked walk
// cannot see it, so it is listed instead of passed.
func filesOutsideEveryBuild(t *testing.T, root string, loaded map[string]bool) []string {
	t.Helper()
	var outside []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "testdata", ".tmp", "node_modules", "vendor":
				return filepath.SkipDir
			}
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || loaded[filepath.Clean(path)] {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(platformModelPathNames, func(name []byte) bool { return bytes.Contains(source, name) }) {
			rel, _ := filepath.Rel(root, path)
			outside = append(outside, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return outside
}

func isSelector(expr ast.Expr, pkg, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

// Every production path to the platform model passes the service model gate:
// the resolver is constructed only where the hosted runtime composes it into
// the one engine, that engine is driven in-process only by
// investigateRecovered, and investigateRecovered is called only by the
// investigation handler on the context gateServiceModel returned, after its
// refusal returned. A new constructor, engine, Investigate caller or direct
// model call anywhere else fails here.
func TestPlatformModelPathIsReachedOnlyThroughTheServiceModelGate(t *testing.T) {
	root := repopath.Root(t)
	sites, loaded := scanPlatformModelPath(t, root, []string{"./..."})
	for _, check := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "resolver constructions", got: sites.resolverConstructions, want: []string{
			"internal/runtime/hosted/open.go:offerPhrasingRuntimeFor",
			// CHAOS-13002: the read planner's resolver over the deployment
			// default, composed into the same one engine as phrasing's.
			"internal/runtime/hosted/open.go:readPlanningRuntimeFor",
			"internal/runtime/hosted/org_model_config.go:newOrgModelRuntimeResolver",
		}},
		{name: "engine constructions", got: sites.engineConstructions, want: []string{
			"internal/runtime/hosted/open.go:buildContextFabricInvestigator",
		}},
		{name: "in-process Investigate calls", got: sites.investigateCalls, want: []string{
			"internal/api/context_fabric_routes.go:investigateRecovered",
			// The panel harness speaks the hosted API over HTTP: its
			// Investigate is a client request that reaches the gate.
			"internal/panelharness/run.go:runPanelist",
		}},
		{name: "model runtime calls outside the engine", got: sites.modelCallsOutside, want: nil},
		{name: "non-test importers of " + paritytestDir, got: sites.paritytestImporters, want: nil},
		{name: "production files that name a site outside every release build", got: filesOutsideEveryBuild(t, root, loaded), want: nil},
	} {
		got := slices.Clone(check.got)
		slices.Sort(got)
		if !slices.Equal(got, check.want) {
			t.Errorf("%s = %q, want %q", check.name, got, check.want)
		}
	}
	assertInvestigationHandlerRunsOnTheGatedContext(t, filepath.Join(root, "internal", "api", "context_fabric_routes.go"))
}

// assertInvestigationHandlerRunsOnTheGatedContext checks that the one call of
// investigateRecovered takes a context derived from gateServiceModel's result
// and sits after the "if !served { return }" refusal of that same call.
func assertInvestigationHandlerRunsOnTheGatedContext(t *testing.T, path string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var calls []*ast.CallExpr
	var bodies []*ast.BlockStmt
	ast.Inspect(file, func(node ast.Node) bool {
		if lit, ok := node.(*ast.FuncLit); ok {
			ast.Inspect(lit.Body, func(inner ast.Node) bool {
				if call, ok := inner.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "investigateRecovered" {
						calls = append(calls, call)
						bodies = append(bodies, lit.Body)
					}
				}
				return true
			})
			return false
		}
		return true
	})
	if len(calls) != 1 {
		t.Fatalf("investigateRecovered calls inside handlers = %d, want exactly 1", len(calls))
	}
	call, body := calls[0], bodies[0]
	sources := map[string]*ast.CallExpr{}
	var refusedBeforeCall bool
	var gateServed string
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if len(n.Rhs) == 1 {
				if rhs, ok := n.Rhs[0].(*ast.CallExpr); ok {
					for index, lhs := range n.Lhs {
						if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
							sources[ident.Name] = rhs
							if index == 1 && isGateCall(rhs) {
								gateServed = ident.Name
							}
						}
					}
				}
			}
		case *ast.IfStmt:
			unary, ok := n.Cond.(*ast.UnaryExpr)
			if ok && unary.Op == token.NOT && gateServed != "" && n.Pos() < call.Pos() {
				if ident, ok := unary.X.(*ast.Ident); ok && ident.Name == gateServed && len(n.Body.List) > 0 {
					if _, ok := n.Body.List[len(n.Body.List)-1].(*ast.ReturnStmt); ok {
						refusedBeforeCall = true
					}
				}
			}
		}
		return true
	})
	if !refusedBeforeCall {
		t.Fatal("the investigation handler does not return on the gate's refusal before investigateRecovered")
	}
	ident, ok := call.Args[0].(*ast.Ident)
	if !ok {
		t.Fatalf("investigateRecovered context argument is %T, want a variable traced to gateServiceModel", call.Args[0])
	}
	for name, hops := ident.Name, 0; ; hops++ {
		source, ok := sources[name]
		if !ok || hops > 4 {
			t.Fatalf("investigateRecovered context %q does not trace to gateServiceModel", ident.Name)
		}
		if isGateCall(source) {
			return
		}
		// CHAOS-11912: WithReservedResultID carries the submission's
		// reserved result id and nothing else; it does not change which
		// model the gate admitted. CHAOS-11952: WithReservedResultIDFor is
		// the same call with one more bit (the id is a submission record's,
		// so its save must find the record); it does not change the model
		// either.
		if !(isSelector(source.Fun, "contextfabric", "WithSynthesisInputCollector") || isSelector(source.Fun, "contextfabric", "WithReservedResultID") || isSelector(source.Fun, "contextfabric", "WithReservedResultIDFor")) || len(source.Args) == 0 {
			t.Fatalf("investigateRecovered context %q passes through %s, not only contextfabric.WithSynthesisInputCollector, WithReservedResultID or WithReservedResultIDFor", ident.Name, callName(source.Fun))
		}
		next, ok := source.Args[0].(*ast.Ident)
		if !ok {
			t.Fatalf("investigateRecovered context %q passes through a non-variable argument", ident.Name)
		}
		name = next.Name
	}
}

func isGateCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "gateServiceModel"
}

func callName(expr ast.Expr) string {
	if selector, ok := expr.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}
	return "a call"
}

// The scan is itself tested on planted packages: a site is found by the object
// its identifiers resolve to, through an import alias or a dot import, called
// or as a value, in a signature or a body, and a Resolver value is found
// however it comes into being (literal, elided literal, variable, field,
// parameter, result, new, make), while pointer uses, shadowing locals, struct
// fields, labels and same-named methods of other types are not sites.
func TestPlatformModelPathScanFindsEveryPlantedSite(t *testing.T) {
	root := repopath.Root(t)
	var patterns []string
	for _, name := range []string{"aliases", "dotimport", "values", "elided", "signature", "foreign"} {
		patterns = append(patterns, plantedPackages+"/"+name)
	}
	sites, _ := scanPlatformModelPath(t, root, patterns)
	const dir = "internal/api/testdata/platformmodelpath/"
	for _, check := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "resolver constructions", got: sites.resolverConstructions, want: []string{
			dir + "aliases/aliases.go:Aliased",
			dir + "aliases/aliases.go:Literal",
			dir + "aliases/aliases.go:Plain",
			dir + "dotimport/resolver.go:Dot",
			dir + "dotimport/resolver.go:DotLiteral",
			dir + "dotimport/resolver.go:DotValue",
			dir + "dotimport/resolver.go:DotZero",
			dir + "elided/elided.go:Array",
			dir + "elided/elided.go:MapKey",
			dir + "elided/elided.go:MapValue",
			dir + "elided/elided.go:Nested",
			dir + "elided/elided.go:Slice",
			dir + "elided/elided.go:ThroughAlias",
			dir + "signature/signature.go:ArrayBound",
			dir + "signature/signature.go:Constrained",
			dir + "signature/signature.go:NamedResult",
			dir + "signature/signature.go:UnnamedParameter",
			dir + "signature/signature.go:UnnamedResult",
			dir + "signature/signature.go:UnnamedVariadic",
			dir + "signature/signature.go:ValueParameter",
			dir + "signature/signature.go:Variadic",
			dir + "values/values.go:Allocated",
			dir + "values/values.go:Both",
			dir + "values/values.go:Copied",
			dir + "values/values.go:Made",
			dir + "values/values.go:Value",
			dir + "values/values.go:Zero",
			dir + "values/values.go:package-level",
			dir + "values/values.go:package-level",
			dir + "values/values.go:package-level",
			dir + "values/values.go:package-level",
		}},
		{name: "engine constructions", got: sites.engineConstructions, want: []string{
			dir + "aliases/aliases.go:Engine",
			dir + "dotimport/engine.go:DotEngine",
			dir + "dotimport/engine.go:DotEngineValue",
			dir + "signature/signature.go:EngineArrayBound",
			dir + "values/values.go:EngineValue",
		}},
		{name: "in-process Investigate calls", got: sites.investigateCalls, want: []string{
			dir + "foreign/foreign.go:ThroughInterface",
			dir + "signature/signature.go:InvestigateArrayBound",
			dir + "values/values.go:InvestigateMethodExpression",
			dir + "values/values.go:InvestigateValue",
		}},
		{name: "model runtime calls outside the engine", got: sites.modelCallsOutside, want: []string{
			dir + "values/values.go:ModelValue",
		}},
		{name: "non-test importers of " + paritytestDir, got: sites.paritytestImporters, want: nil},
	} {
		if !slices.Equal(check.got, check.want) {
			t.Errorf("%s = %q, want %q", check.name, check.got, check.want)
		}
	}
}
