package projectionrun_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
)

// traceCapture records the trace ID each log record's context carries.
type traceCapture struct {
	mu     sync.Mutex
	traces []trace.TraceID
}

func (c *traceCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *traceCapture) Handle(ctx context.Context, _ slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.traces = append(c.traces, trace.SpanContextFromContext(ctx).TraceID())
	return nil
}
func (c *traceCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *traceCapture) WithGroup(string) slog.Handler      { return c }

// TestRunTracedOpensOneSpanPerTick pins that RunTraced wraps each tick in a
// span on the given tracer and that the tick's own log lines carry it; Run
// (nil tracer) opens none.
func TestRunTracedOpensOneSpanPerTick(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	capture := &traceCapture{}
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{"org-1"}, Sources: []projectionrun.SourcePair{{Name: "source-a", Source: &fakeSource{name: "source-a", pages: 1}}},
		Backend: newFakeBackend(), Checkpoints: newFakeCheckpointStore(), RebuildMarkers: newFakeRebuildMarker(), Logger: slog.New(capture),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one tick, then Run returns on the cancelled context
	if err := coordinator.RunTraced(ctx, provider.Tracer("test")); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTraced = %v, want context.Canceled", err)
	}
	got := spans.GetSpans()
	if len(got) != 1 || got[0].Name != projectionrun.TickSpanName {
		t.Fatalf("spans = %d (%v), want exactly one %q", len(got), got, projectionrun.TickSpanName)
	}
	tickTrace := got[0].SpanContext.TraceID()
	capture.mu.Lock()
	lines := append([]trace.TraceID(nil), capture.traces...)
	capture.mu.Unlock()
	correlated := 0
	for _, id := range lines {
		if id == tickTrace {
			correlated++
		}
	}
	if correlated == 0 {
		t.Fatalf("none of the tick's %d log lines carries the tick span's trace", len(lines))
	}

	spans.Reset()
	if err := coordinator.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if n := len(spans.GetSpans()); n != 0 {
		t.Fatalf("Run (no tracer) exported %d spans", n)
	}
}
