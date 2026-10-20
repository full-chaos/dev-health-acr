package api

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
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
			ast.Inspect(scope, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.CompositeLit:
					if isSelector(n.Type, "modelruntimeresolver", "Resolver") && !inResolverPackage {
						sites.resolverConstructions = append(sites.resolverConstructions, site)
					}
				case *ast.CallExpr:
					selector, ok := n.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch {
					case isSelector(selector, "modelruntimeresolver", "New") && !inResolverPackage:
						sites.resolverConstructions = append(sites.resolverConstructions, site)
					case isSelector(selector, "contextfabric", "NewEngine"):
						sites.engineConstructions = append(sites.engineConstructions, site)
					case selector.Sel.Name == "Investigate":
						sites.investigateCalls = append(sites.investigateCalls, site)
					case !inContextFabric && slices.Contains([]string{"InterpretQuestion", "SynthesizeAnswer", "PhraseStructureOffers"}, selector.Sel.Name):
						sites.modelCallsOutside = append(sites.modelCallsOutside, site)
					}
				}
				return true
			})
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
		if !isSelector(source.Fun, "contextfabric", "WithSynthesisInputCollector") || len(source.Args) == 0 {
			t.Fatalf("investigateRecovered context %q passes through %s, not only contextfabric.WithSynthesisInputCollector", ident.Name, callName(source.Fun))
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
