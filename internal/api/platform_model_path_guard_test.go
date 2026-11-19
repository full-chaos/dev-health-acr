package api

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// paritytestDir holds engine compositions for tests only; its importers are
// checked to be test files, so it is not a production path.
const paritytestDir = "internal/contextfabric/pginvestigation/paritytest"

const (
	resolverPackagePath      = "/internal/contextfabric/modelruntimeresolver"
	contextFabricPackagePath = "/internal/contextfabric"
)

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

func scanPlatformModelPath(t *testing.T, root string) platformModelPathSites {
	t.Helper()
	var sites platformModelPathSites
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "testdata", ".tmp", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, paritytestDir+"/") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !mentionsPlatformModelPathName(source) {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), rel, source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		for _, spec := range file.Imports {
			if importPath, _ := strconv.Unquote(spec.Path.Value); strings.HasSuffix(importPath, "/"+paritytestDir) {
				sites.paritytestImporters = append(sites.paritytestImporters, rel)
			}
		}
		imports, dotImports := importedNames(file)
		inResolverPackage := strings.HasPrefix(rel, "internal/contextfabric/modelruntimeresolver/")
		inContextFabric := strings.HasPrefix(rel, "internal/contextfabric/")
		for _, decl := range file.Decls {
			site, scope := rel+":package-level", ast.Node(decl)
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if fn.Body == nil {
					continue
				}
				site, scope = rel+":"+fn.Name.Name, fn.Body
			}
			// A name is a site wherever it is referenced, called or not: a
			// function or method value reaches the path as well as a call does.
			// The Resolver type is a site wherever it is not behind a pointer:
			// its fields are exported, so a literal or a zero value (var, new, a
			// value field) can each serve as a constructed resolver.
			var visit func(ast.Node) bool
			visit = func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.StarExpr:
					if isPackageName(n.X, imports, dotImports, resolverPackagePath, "Resolver") {
						return false
					}
				case *ast.SelectorExpr:
					switch {
					case isPackageSelector(n, imports, resolverPackagePath, "Resolver") && !inResolverPackage:
						sites.resolverConstructions = append(sites.resolverConstructions, site)
					case isPackageSelector(n, imports, resolverPackagePath, "New") && !inResolverPackage:
						sites.resolverConstructions = append(sites.resolverConstructions, site)
					case isPackageSelector(n, imports, contextFabricPackagePath, "NewEngine"):
						sites.engineConstructions = append(sites.engineConstructions, site)
					case n.Sel.Name == "Investigate":
						sites.investigateCalls = append(sites.investigateCalls, site)
					case !inContextFabric && slices.Contains([]string{"InterpretQuestion", "SynthesizeAnswer", "PhraseStructureOffers"}, n.Sel.Name):
						sites.modelCallsOutside = append(sites.modelCallsOutside, site)
					}
					// The selected name is not a bare identifier of a dot
					// import, so only the operand is scanned further.
					ast.Inspect(n.X, visit)
					return false
				case *ast.Ident:
					switch {
					case (isDotImported(n, dotImports, resolverPackagePath, "New") || isDotImported(n, dotImports, resolverPackagePath, "Resolver")) && !inResolverPackage:
						sites.resolverConstructions = append(sites.resolverConstructions, site)
					case isDotImported(n, dotImports, contextFabricPackagePath, "NewEngine"):
						sites.engineConstructions = append(sites.engineConstructions, site)
					}
				}
				return true
			}
			ast.Inspect(scope, visit)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sites
}

// platformModelPathNames is every name a site needs in its text to be found:
// a file with none of them cannot hold a site, so it is not parsed.
var platformModelPathNames = [][]byte{
	[]byte("modelruntimeresolver"), []byte("NewEngine"), []byte("Investigate"),
	[]byte("InterpretQuestion"), []byte("SynthesizeAnswer"), []byte("PhraseStructureOffers"), []byte(paritytestDir),
}

func mentionsPlatformModelPathName(source []byte) bool {
	for _, name := range platformModelPathNames {
		if bytes.Contains(source, name) {
			return true
		}
	}
	return false
}

// importedNames maps each name a file binds to an import (its alias, else the
// last element of the path) to the import path, and lists the paths the file
// dot-imports, whose exported names it uses bare.
func importedNames(file *ast.File) (map[string]string, []string) {
	names := map[string]string{}
	var dotImports []string
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." {
			dotImports = append(dotImports, importPath)
			continue
		}
		names[name] = importPath
	}
	return names, dotImports
}

func isAcrPackage(importPath, pathSuffix string) bool {
	return strings.HasSuffix(importPath, "github.com/full-chaos/dev-health-acr"+pathSuffix)
}

// isPackageSelector reports whether expr is name selected from the package at
// the import path that ends in pathSuffix, under whatever name the file gave it.
func isPackageSelector(expr ast.Expr, imports map[string]string, pathSuffix, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && isAcrPackage(imports[ident.Name], pathSuffix)
}

// isDotImported reports whether expr is the bare name of a file that
// dot-imports the package at the import path that ends in pathSuffix.
func isDotImported(expr ast.Expr, dotImports []string, pathSuffix, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name && slices.ContainsFunc(dotImports, func(importPath string) bool {
		return isAcrPackage(importPath, pathSuffix)
	})
}

// isPackageName reports whether expr names name from the package at the
// import path that ends in pathSuffix, selected or through a dot import.
func isPackageName(expr ast.Expr, imports map[string]string, dotImports []string, pathSuffix, name string) bool {
	return isPackageSelector(expr, imports, pathSuffix, name) || isDotImported(expr, dotImports, pathSuffix, name)
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
	sites := scanPlatformModelPath(t, root)
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

// The scan resolves an import by its path, not by the package's default name:
// a construction or call made through an import alias is a site too.
func TestPlatformModelPathScanFindsSitesBehindImportAliases(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/plant/plain.go", `package plant

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

func Plain() { _ = modelruntimeresolver.New(nil, nil, nil) }
`)
	write("internal/plant/aliased.go", `package plant

import mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

func Aliased() { _ = mr.New(nil, nil, nil) }
`)
	write("internal/plant/literal.go", `package plant

import res "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

func Literal() { _ = &res.Resolver{} }
`)
	write("internal/plant/engine.go", `package plant

import cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"

func Engine() { _, _ = cf.NewEngine(cf.EngineDependencies{}, cf.EngineOptions{}) }
`)
	write("internal/plant/unrelated.go", `package plant

import mr "example.test/other/modelruntimeresolver"

func Unrelated() { _ = mr.New(nil, nil, nil) }
`)
	sites := scanPlatformModelPath(t, root)
	got := slices.Clone(sites.resolverConstructions)
	slices.Sort(got)
	want := []string{"internal/plant/aliased.go:Aliased", "internal/plant/literal.go:Literal", "internal/plant/plain.go:Plain"}
	if !slices.Equal(got, want) {
		t.Fatalf("resolver constructions = %q, want %q", got, want)
	}
	if !slices.Equal(sites.engineConstructions, []string{"internal/plant/engine.go:Engine"}) {
		t.Fatalf("engine constructions = %q, want the aliased NewEngine", sites.engineConstructions)
	}
}

// A dot import binds the package's names bare, and a function or method value
// reaches the path without a call at the site: each is a site too, counted
// once, while the same bare names from another package are not.
func TestPlatformModelPathScanFindsDotImportsAndUncalledValues(t *testing.T) {
	root := t.TempDir()
	write := func(name, source string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/plant/dot.go", `package plant

import . "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

func Dot() { _ = New(nil, nil, nil) }

func DotLiteral() { _ = &Resolver{} }

func DotValue() { construct := New; _ = construct }

func DotZero() { var zero Resolver; _ = zero }

func DotPointer(resolver *Resolver) { _ = (*Resolver)(nil); _ = resolver }
`)
	write("internal/plant/dotengine.go", `package plant

import . "github.com/full-chaos/dev-health-acr/internal/contextfabric"

func DotEngine() { _, _ = NewEngine(EngineDependencies{}, EngineOptions{}) }
`)
	write("internal/plant/value.go", `package plant

import (
	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
)

var packageValue = mr.New

func Value() { construct := mr.New; _ = construct }

func EngineValue() { build := cf.NewEngine; _ = build }

func InvestigateValue(engine *cf.Engine) { run := engine.Investigate; _ = run }

func InvestigateMethodExpression() { _ = (*cf.Engine).Investigate }

func ModelValue(runtime cf.ModelRuntime) { phrase := runtime.SynthesizeAnswer; _ = phrase }

func Zero() { var zero mr.Resolver; zero.Default = nil; _ = &zero }

func Allocated() { _ = new(mr.Resolver) }

func Copied(resolver *mr.Resolver) { copied := *resolver; _ = copied }

func Pointers(resolver *mr.Resolver) (*mr.Resolver, func(*mr.Resolver)) {
	var none *mr.Resolver
	_, _ = any(resolver).(*mr.Resolver)
	return none, func(*mr.Resolver) {}
}

type holder struct{ resolver mr.Resolver }

type pointerHolder struct{ resolver *mr.Resolver }
`)
	write("internal/plant/both.go", `package plant

import (
	. "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	mr "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
)

func Both() { _ = mr.New(nil, nil, nil) }
`)
	write("internal/plant/unrelated.go", `package plant

import . "example.test/other/modelruntimeresolver"

func Unrelated() { _ = New(nil, nil, nil); _ = &Resolver{} }
`)
	write("internal/plant/local.go", `package plant

import "github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"

var _ modelruntimeresolver.Build

func New() int { return 0 }

func Local() { _ = New() }
`)
	sites := scanPlatformModelPath(t, root)
	for _, check := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "resolver constructions", got: sites.resolverConstructions, want: []string{
			"internal/plant/both.go:Both",
			"internal/plant/dot.go:Dot",
			"internal/plant/dot.go:DotLiteral",
			"internal/plant/dot.go:DotValue",
			"internal/plant/dot.go:DotZero",
			"internal/plant/value.go:Allocated",
			"internal/plant/value.go:Value",
			"internal/plant/value.go:Zero",
			"internal/plant/value.go:package-level",
			"internal/plant/value.go:package-level",
		}},
		{name: "engine constructions", got: sites.engineConstructions, want: []string{
			"internal/plant/dotengine.go:DotEngine",
			"internal/plant/value.go:EngineValue",
		}},
		{name: "in-process Investigate calls", got: sites.investigateCalls, want: []string{
			"internal/plant/value.go:InvestigateMethodExpression",
			"internal/plant/value.go:InvestigateValue",
		}},
		{name: "model runtime calls outside the engine", got: sites.modelCallsOutside, want: []string{
			"internal/plant/value.go:ModelValue",
		}},
	} {
		got := slices.Clone(check.got)
		slices.Sort(got)
		if !slices.Equal(got, check.want) {
			t.Errorf("%s = %q, want %q", check.name, got, check.want)
		}
	}
}
