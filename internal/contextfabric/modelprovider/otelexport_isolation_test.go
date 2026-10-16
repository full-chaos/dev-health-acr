package modelprovider

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/full-chaos/dev-health-acr/internal/otelexport"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type isolationLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (m *isolationLogs) Export(_ context.Context, records []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		m.records = append(m.records, r.Clone())
	}
	return nil
}
func (m *isolationLogs) Shutdown(context.Context) error   { return nil }
func (m *isolationLogs) ForceFlush(context.Context) error { return nil }

// TestModelCallTelemetryNeverExported pins that the service's own OTLP export
// (internal/otelexport) never carries the model call's own telemetry. It runs
// a real generation through New() inside an exported request span, with the
// runtime logging through the exported logger, and plants the question text
// as the secret.
//
// Non-vacuous by construction: a span processor attached to the global
// TracerProvider New() installs shows the shared client DID record its
// per-attempt span, in the SAME trace as the exported request span, and that
// span holds no question content. The exported sinks must hold the request
// span and the runtime's own log lines, and none of the model call's spans,
// metrics or content.
func TestModelCallTelemetryNeverExported(t *testing.T) {
	ctx := context.Background()
	spans := tracetest.NewInMemoryExporter()
	metrics := sdkmetric.NewManualReader()
	logs := &isolationLogs{}
	exporter, err := otelexport.NewWithExporters(ctx, otelexport.Config{Enabled: true, ServiceName: "acr-api", ServiceVersion: "test"},
		otelexport.Exporters{Spans: spans, Metrics: metrics, Logs: logs, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exporter.Shutdown(ctx) }()
	var stdout bytes.Buffer
	logger := slog.New(exporter.LogHandler(slog.NewJSONHandler(&stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	const secret = "SECRET_QUESTION_OTLP_MUST_NEVER_CARRY_release_readiness_4711"
	provider := recordingProvider(t, chatCompletion(t, validInterpretationJSON))
	cfg := testConfig(provider)
	cfg.Logger = logger
	runtime, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The model call's provider, as New() left it: the global one.
	global, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatalf("global tracer provider is %T, want the *sdktrace.TracerProvider New() installs", otel.GetTracerProvider())
	}
	modelSpans := tracetest.NewInMemoryExporter()
	modelProcessor := sdktrace.NewSimpleSpanProcessor(modelSpans)
	global.RegisterSpanProcessor(modelProcessor)
	defer global.UnregisterSpanProcessor(modelProcessor)

	requestCtx, requestSpan := exporter.Tracer().Start(ctx, "POST /api/v1/context-fabric/investigations")
	request := testRequest()
	request.Question = secret
	if _, _, err := runtime.InterpretQuestion(requestCtx, storage.Principal{OrgID: "org_test"}, request); err != nil {
		t.Fatal(err)
	}
	requestSpan.End()
	if err := exporter.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}

	// Exported spans: exactly the request span, nothing of the model call.
	exported := spans.GetSpans()
	if len(exported) != 1 || exported[0].Name != "POST /api/v1/context-fabric/investigations" {
		names := make([]string, 0, len(exported))
		for _, s := range exported {
			names = append(names, s.Name)
		}
		t.Fatalf("exported spans = %v, want only the request span", names)
	}
	for _, kv := range exported[0].Attributes {
		if strings.Contains(kv.Value.Emit(), secret) {
			t.Fatalf("exported span attribute %s carries the secret", kv.Key)
		}
	}

	// The model call's own span is real, inside the request's trace, and
	// carries no question content.
	modelCallSpans := 0
	for _, s := range modelSpans.GetSpans() {
		if s.Name == "dev_health.llm.request" {
			modelCallSpans++
			if s.SpanContext.TraceID() != requestSpan.SpanContext().TraceID() {
				t.Fatalf("model call span %q is not in the request trace; the isolation claim needs it to be", s.Name)
			}
		}
		for _, kv := range s.Attributes {
			if strings.Contains(kv.Value.Emit(), secret) {
				t.Fatalf("model call span %q attribute %s carries the secret", s.Name, kv.Key)
			}
		}
	}
	if modelCallSpans == 0 {
		t.Fatalf("sanity: no model call span was recorded (%d spans); this test would prove nothing", len(modelSpans.GetSpans()))
	}

	// Exported logs: the runtime's own lines reached the export, none carries
	// the secret.
	logs.mu.Lock()
	records := append([]sdklog.Record(nil), logs.records...)
	logs.mu.Unlock()
	if len(records) == 0 {
		t.Fatalf("sanity: the runtime's own log lines never reached the export; stdout was: %s", stdout.String())
	}
	for _, r := range records {
		if strings.Contains(r.Body().String(), secret) {
			t.Fatalf("exported log body carries the secret: %s", r.Body().String())
		}
		leaked := false
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			if strings.Contains(kv.Value.String(), secret) {
				leaked = true
			}
			return true
		})
		if leaked {
			t.Fatalf("exported log record %q carries the secret in an attribute", r.Body().String())
		}
	}

	// Exported metrics: nothing from Genkit's meter or the shared client's.
	var rm metricdata.ResourceMetrics
	if err := metrics.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		if strings.Contains(sm.Scope.Name, "genkit") || strings.Contains(sm.Scope.Name, "dev_health/llm") {
			t.Fatalf("exported metrics include the model call's scope %q", sm.Scope.Name)
		}
	}
}
