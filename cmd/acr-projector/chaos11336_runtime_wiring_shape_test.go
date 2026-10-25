package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Call-site shape pin of the production wiring in runtime.go, not a runtime
// behaviour test: openRuntime needs real Postgres, ClickHouse and FalkorDB, so
// the helpers behind these call sites are behaviour-tested separately. Each
// lookup fails loudly when its node is absent, so this cannot pass by finding
// nothing.

func parseRuntimeFile(t *testing.T) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "runtime.go", nil, 0)
	if err != nil {
		t.Fatalf("parse runtime.go: %v", err)
	}
	return file
}

func findFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("runtime.go has no function %s", name)
	return nil
}

func isIdents(args []ast.Expr, names ...string) bool {
	if len(args) != len(names) {
		return false
	}
	for i, arg := range args {
		if ident, ok := arg.(*ast.Ident); !ok || ident.Name != names[i] {
			return false
		}
	}
	return true
}

func calleeName(call *ast.CallExpr) string {
	if ident, ok := call.Fun.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

func TestOpenRuntimePassesMetricsToTheCoordinatorObserverAndTheBackend(t *testing.T) {
	fn := findFunc(t, parseRuntimeFile(t), "openRuntime")

	var configLiterals, observerFields, backendCalls int
	var observerOK, backendOK bool
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CompositeLit:
			sel, ok := n.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Config" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "projectionrun" {
				return true
			}
			configLiterals++
			for _, elt := range n.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Observer" {
					observerFields++
					call, ok := kv.Value.(*ast.CallExpr)
					observerOK = ok && calleeName(call) == "projectionObserver" && isIdents(call.Args, "logger", "metrics")
				}
			}
		case *ast.CallExpr:
			if calleeName(n) == "openProjectionBackend" {
				backendCalls++
				backendOK = len(n.Args) == 4 && isIdents(n.Args[:2], "logger", "metrics")
			}
		}
		return true
	})

	if configLiterals != 1 {
		t.Fatalf("openRuntime builds %d projectionrun.Config literals, want exactly 1", configLiterals)
	}
	if observerFields != 1 {
		t.Fatalf("the projectionrun.Config literal has %d Observer fields, want exactly 1", observerFields)
	}
	if !observerOK {
		t.Fatal("projectionrun.Config.Observer must be projectionObserver(logger, metrics): the pair failure counter would not reach the projector")
	}
	if backendCalls != 1 {
		t.Fatalf("openRuntime calls openProjectionBackend %d times, want exactly 1", backendCalls)
	}
	if !backendOK {
		t.Fatal("openProjectionBackend must be called with (logger, metrics, ...): the bootstrap wait counter would not reach the projector")
	}
}

func TestOpenProjectionBackendBuildsTheGraphTelemetryWithMetrics(t *testing.T) {
	fn := findFunc(t, parseRuntimeFile(t), "openProjectionBackend")

	var assignments int
	var ok bool
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		assign, isAssign := node.(*ast.AssignStmt)
		if !isAssign || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		sel, isSel := assign.Lhs[0].(*ast.SelectorExpr)
		if !isSel || sel.Sel.Name != "Telemetry" {
			return true
		}
		if base, isIdent := sel.X.(*ast.Ident); !isIdent || base.Name != "falkorConfig" {
			return true
		}
		assignments++
		call, isCall := assign.Rhs[0].(*ast.CallExpr)
		ok = isCall && calleeName(call) == "falkorGraphTelemetryWithMetrics" && isIdents(call.Args, "logger", "metrics")
		return true
	})
	if assignments != 1 {
		t.Fatalf("openProjectionBackend assigns falkorConfig.Telemetry %d times, want exactly 1", assignments)
	}
	if !ok {
		t.Fatal("falkorConfig.Telemetry must be falkorGraphTelemetryWithMetrics(logger, metrics)")
	}
}
