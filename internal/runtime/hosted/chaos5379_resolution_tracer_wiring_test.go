package hosted

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// fakeOverrideResolutionTracer is a minimal graphrank.ResolutionTracer
// double used only to prove identity (that defaultResolutionTracer returns
// THIS exact value, not a wrapper around it).
type fakeOverrideResolutionTracer struct{}

func (fakeOverrideResolutionTracer) Trace(graphrank.ResolutionTraceEvent) {}

// TestDefaultResolutionTracer_NilOverrideInstallsTheProductionSink is the
// deployed-wiring half of the emission-path gate. request.options.
// ResolutionTracer is nil for every real deployment (its own doc comment),
// so this branch IS the production construction -- and a sink that is
// plumbed but never installed is, operationally, indistinguishable from no
// sink at all.
//
// Mutation check: reverting defaultResolutionTracer to `return override`
// unconditionally makes this test fail (the result would stay nil).
func TestDefaultResolutionTracer_NilOverrideInstallsTheProductionSink(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	tracer := defaultResolutionTracer(nil, logger)

	if tracer == nil {
		t.Fatal("defaultResolutionTracer(nil, logger) = nil, want the production SlogResolutionTracer -- ResolutionTracer must not be nil in a real deployment")
	}
	if _, ok := tracer.(graphrank.SlogResolutionTracer); !ok {
		t.Fatalf("defaultResolutionTracer(nil, logger) = %T, want graphrank.SlogResolutionTracer", tracer)
	}
}

// TestDefaultResolutionTracer_ExplicitOverrideStillWins keeps the
// acceptance-debt escape hatch (an in-process harness capturing trace
// events directly) taking priority, unchanged.
func TestDefaultResolutionTracer_ExplicitOverrideStillWins(t *testing.T) {
	override := fakeOverrideResolutionTracer{}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	tracer := defaultResolutionTracer(override, logger)

	if tracer != graphrank.ResolutionTracer(override) {
		t.Fatalf("defaultResolutionTracer(override, logger) = %#v, want the exact override value unchanged", tracer)
	}
}

// TestTheDeployedTracerEmitsTheDecisionAtTheProductionLogLevel reads an
// actual emitted line back out of the sink the DEPLOYED construction
// returns, through a real slog handler at the production default level
// (slog.LevelInfo, internal/sidecar/config.go's defaultLogLevel).
//
// A test that hands its own sink to a test adapter proves formatting only:
// it stays green when the runtime stops installing one. This one goes
// through defaultResolutionTracer, the exact call open.go makes, so the
// wiring and the level are both under the assertion.
//
// CHAOS-5516: decision_summary's emission now reads ONLY
// event.DecisionSummaryFields (tracer.go's own "decision_summary" case is
// event.DecisionSummaryFields.SlogArgs()... and nothing else) -- the OLD
// individual Decision* fields this test used to set directly on the shared
// ResolutionTraceEvent are no longer read for this stage at all, so this
// fixture is built through the generated typed constructor, the same
// contract every real caller now goes through, never a second, independently
// hand-typed construction of the same event.
func TestTheDeployedTracerEmitsTheDecisionAtTheProductionLogLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	defaultResolutionTracer(nil, logger).Trace(graphrank.ResolutionTraceEvent{
		RequestID: "request_5379_wiring", Stage: "decision_summary",
		DecisionSummaryFields: eventspec.NewDecisionSummaryFields(
			"request_5379_wiring", 2, 1, 0, 1,
			[]string{"team.v2:github:platform"}, []string{"exact_index"}, []string{"statistical"},
			false, "none", "none",
			0, 0, false, 0, "none", "none", []string{}, 0,
			"none", "none", "none", []string{}, []string{},
		),
	})

	if buf.Len() == 0 {
		t.Fatal("the deployed tracer emitted NOTHING at the production log level -- an operator on the rig would see no decision at all")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO -- the decision must survive the production log level", level)
	}
	if stage, _ := rec["stage"].(string); stage != "decision_summary" {
		t.Fatalf("stage = %q, want \"decision_summary\"", stage)
	}
	for key, want := range map[string]float64{
		"decision_event_count": 2, "committed_count": 1, "no_commit_count": 1, "ambiguous_count": 0,
	} {
		got, ok := rec[key].(float64)
		if !ok {
			t.Errorf("the emitted line carries no numeric %q -- an absent count and a measured zero must never read alike; line: %s", key, buf.String())
			continue
		}
		if got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

// TestOpenInstallsTheResolutionTracerThroughTheTestedHelper is the
// structural half: the three tests above assert what
// defaultResolutionTracer DOES, and would all stay green if open.go
// stopped calling it. This one asserts the call site itself, on the AST
// (a text search for the call would pass on a commented-out one -- see the
// negative control below): graphConfig.ResolutionTracer is assigned from a
// call to defaultResolutionTracer, and that identifier is never rebound
// elsewhere in the package, so the tested helper is the one production
// runs.
func TestOpenInstallsTheResolutionTracerThroughTheTestedHelper(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "open.go", nil, 0)
	if err != nil {
		t.Fatalf("parse open.go: %v", err)
	}
	if got := countTracerInstallations(file); got != 1 {
		t.Fatalf("assignments of graphConfig.ResolutionTracer from a defaultResolutionTracer(...) call = %d, want exactly 1 -- the deployed sink must come from the helper these tests cover", got)
	}

	// Negative control: the same walk over a tree whose only such
	// assignment is commented out must report zero. A pin that cannot fail
	// proves nothing about the pin above.
	commented, err := parser.ParseFile(token.NewFileSet(), "control.go", `package hosted

func control() {
	// graphConfig.ResolutionTracer = defaultResolutionTracer(nil, nil)
	_ = 1
}
`, 0)
	if err != nil {
		t.Fatalf("parse the negative control: %v", err)
	}
	if got := countTracerInstallations(commented); got != 0 {
		t.Fatalf("the negative control reported %d installations, want 0 -- the walk is matching text, not the AST", got)
	}
}

// countTracerInstallations walks file for assignments whose left side is
// the selector graphConfig.ResolutionTracer and whose right side calls
// defaultResolutionTracer. A right side this function cannot recognize is
// counted as a FAILURE to match rather than skipped -- a silent skip would
// report agreement when it had simply not looked.
func countTracerInstallations(file *ast.File) int {
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		selector, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "ResolutionTracer" {
			return true
		}
		base, ok := selector.X.(*ast.Ident)
		if !ok || base.Name != "graphConfig" {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "defaultResolutionTracer" {
			return true
		}
		found++
		return true
	})
	return found
}

// TestDefaultResolutionTracerIsNeverRedefined closes the other half of the
// structural pin: asserting the call site says nothing if a second
// declaration of the same name could shadow the tested one.
func TestDefaultResolutionTracerIsNeverRedefined(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package: %v", err)
	}
	declarations := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "defaultResolutionTracer" {
					declarations++
				}
			}
		}
	}
	if declarations != 1 {
		t.Fatalf("declarations of defaultResolutionTracer = %d, want exactly 1", declarations)
	}
}
