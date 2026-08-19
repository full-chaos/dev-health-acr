package graphrank

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestSlogResolutionTracer_CoversEveryEmittedStage is team-lead's own
// rider (CHAOS-3918 review, 2026-08-19): the production tracer's Stage
// switch has now silently dropped a stage's whole payload TWICE in two
// days -- evidence_census_commit (CHAOS-3896 Slice C), then
// evidence_source_native/evidence_source_native_probe (this ticket) --
// both caught only by an independent codex pass, never by a test. A
// second instance of the same boundary defect means the INVARIANT needs
// enforcing, not another instance patched.
//
// This closes the defect class rather than adding a third hand-written
// regression test: it STATICALLY enumerates every string literal assigned
// to ResolutionTraceEvent's own Stage field anywhere in this package's
// non-test source (a go/ast walk over the actual producer code, not a
// hand-maintained list that could itself silently drift out of sync with
// what the package actually emits), then asserts SlogResolutionTracer has
// an explicit case for each -- never falling to its own "unknown stage"
// branch. Any FUTURE stage added anywhere in this package (chaos3899_*,
// chaos3896_*, resolve.go, or a file not yet written) is covered by this
// same test automatically, with no second edit required here.
//
// SCOPE BOUNDARY (deliberate, per team-lead's own framing: "every
// trace-stage constant emitted anywhere in graphrank"): this walk covers
// this package's own directory only. One stage in SlogResolutionTracer's
// switch, "identity_universe", is emitted from a DIFFERENT package
// (falkorgraph/reader.go, which imports graphrank for the
// ResolutionTraceEvent type) and is therefore outside this test's static
// scan -- already hand-covered by an existing tracer.go case, just not
// verified by this specific automated walk. Widening the scan to also
// cover falkorgraph (or any other importer) is a reasonable follow-up but
// out of scope for this rider.
func TestSlogResolutionTracer_CoversEveryEmittedStage(t *testing.T) {
	stages := discoverEmittedStages(t)
	if len(stages) == 0 {
		t.Fatal("discoverEmittedStages found zero Stage literals -- the AST walk itself is broken (a false pass here would silently defeat this test's whole purpose)")
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			var buf bytes.Buffer
			tracer := NewSlogResolutionTracer(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			tracer.Trace(ResolutionTraceEvent{RequestID: "req-coverage", Stage: stage})
			if strings.Contains(buf.String(), "unknown stage") {
				t.Fatalf("SlogResolutionTracer has no case for stage %q -- it IS emitted somewhere in this package's own source but silently dropped in production (exactly the evidence_census_commit / evidence_source_native defect class)", stage)
			}
		})
	}
}

// discoverEmittedStages walks every non-test .go file in this package's
// own directory (the test's working directory during `go test`) and
// collects every DISTINCT string literal assigned to a `Stage:` key
// inside a `ResolutionTraceEvent{...}` composite literal -- e.g.
// `tracer.Trace(ResolutionTraceEvent{Stage: "evidence_round", ...})`.
// tracer.go itself is excluded: its switch is the CONSUMER under test,
// not a producer -- including it would let a stray `case "foo":` with no
// real emitter anywhere pass this test vacuously (there would be nothing
// left to assert against for that stage).
func discoverEmittedStages(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	seen := map[string]bool{}
	var stages []string
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "tracer.go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			id, ok := lit.Type.(*ast.Ident)
			if !ok || id.Name != "ResolutionTraceEvent" {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Stage" {
					continue
				}
				basic, ok := kv.Value.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(basic.Value)
				if err != nil {
					continue
				}
				if !seen[value] {
					seen[value] = true
					stages = append(stages, value)
				}
			}
			return true
		})
	}
	return stages
}
