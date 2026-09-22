package otelexport

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// memoryLogs is an in-memory sdklog.Exporter.
type memoryLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (m *memoryLogs) Export(_ context.Context, records []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		m.records = append(m.records, r.Clone())
	}
	return nil
}
func (m *memoryLogs) Shutdown(context.Context) error   { return nil }
func (m *memoryLogs) ForceFlush(context.Context) error { return nil }

func (m *memoryLogs) all() []sdklog.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sdklog.Record(nil), m.records...)
}

type sinks struct {
	spans   *tracetest.InMemoryExporter
	metrics *sdkmetric.ManualReader
	logs    *memoryLogs
}

const (
	testService = "acr-test-service"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
)

func newTestExporter(t *testing.T) (*Exporter, sinks) {
	t.Helper()
	s := sinks{spans: tracetest.NewInMemoryExporter(), metrics: sdkmetric.NewManualReader(), logs: &memoryLogs{}}
	exporter, err := NewWithExporters(context.Background(), Config{Enabled: true, ServiceName: testService, ServiceVersion: testCommit},
		Exporters{Spans: s.spans, Metrics: s.metrics, Logs: s.logs, Synchronous: true})
	if err != nil {
		t.Fatalf("NewWithExporters: %v", err)
	}
	t.Cleanup(func() { _ = exporter.Shutdown(context.Background()) })
	return exporter, s
}

func env(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestConfigFromEnvDomain(t *testing.T) {
	info := version.Info{Version: "v1.2.3", Commit: testCommit}
	endpoint := "http://collector:4317"
	cases := []struct {
		name        string
		values      map[string]string
		info        version.Info
		wantErr     string
		wantEnabled bool
		wantService string
		wantVersion string
	}{
		{name: "all absent", values: map[string]string{}, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled empty", values: map[string]string{EnvEnabled: ""}, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled whitespace", values: map[string]string{EnvEnabled: "  "}, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled false", values: map[string]string{EnvEnabled: "false", EnvEndpoint: endpoint}, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled 0", values: map[string]string{EnvEnabled: "0"}, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled true", values: map[string]string{EnvEnabled: "true", EnvEndpoint: endpoint}, wantEnabled: true, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled 1 padded", values: map[string]string{EnvEnabled: " 1 ", EnvEndpoint: endpoint}, wantEnabled: true, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled TRUE", values: map[string]string{EnvEnabled: "TRUE", EnvEndpoint: endpoint}, wantEnabled: true, wantService: "acr-api", wantVersion: testCommit},
		{name: "enabled yes is not a boolean", values: map[string]string{EnvEnabled: "yes", EnvEndpoint: endpoint}, wantErr: "OTEL_ENABLED must be a boolean"},
		{name: "enabled 2 is not a boolean", values: map[string]string{EnvEnabled: "2", EnvEndpoint: endpoint}, wantErr: "OTEL_ENABLED must be a boolean"},
		{name: "enabled without endpoint", values: map[string]string{EnvEnabled: "true"}, wantErr: "OTEL_ENABLED=true requires OTEL_EXPORTER_OTLP_ENDPOINT"},
		{name: "enabled with empty endpoint", values: map[string]string{EnvEnabled: "true", EnvEndpoint: ""}, wantErr: "requires OTEL_EXPORTER_OTLP_ENDPOINT"},
		{name: "enabled with blank endpoint", values: map[string]string{EnvEnabled: "true", EnvEndpoint: "   "}, wantErr: "requires OTEL_EXPORTER_OTLP_ENDPOINT"},
		{name: "disabled without endpoint is fine", values: map[string]string{EnvEnabled: "false"}, wantService: "acr-api", wantVersion: testCommit},
		{name: "service name override", values: map[string]string{EnvServiceName: " acr-api-canary "}, wantService: "acr-api-canary", wantVersion: testCommit},
		{name: "blank service name keeps default", values: map[string]string{EnvServiceName: "  "}, wantService: "acr-api", wantVersion: testCommit},
		{name: "unstamped commit falls back to version", values: map[string]string{}, info: version.Info{Version: "dev", Commit: "unknown"}, wantService: "acr-api", wantVersion: "dev"},
		{name: "empty commit falls back to version", values: map[string]string{}, info: version.Info{Version: "v0.1.0"}, wantService: "acr-api", wantVersion: "v0.1.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := info
			if tc.info != (version.Info{}) {
				in = tc.info
			}
			cfg, err := ConfigFromEnv(env(tc.values), "acr-api", in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "collector") {
					t.Fatalf("error leaks a value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if cfg.Enabled != tc.wantEnabled || cfg.ServiceName != tc.wantService || cfg.ServiceVersion != tc.wantVersion {
				t.Fatalf("cfg = %+v, want enabled=%v service=%q version=%q", cfg, tc.wantEnabled, tc.wantService, tc.wantVersion)
			}
		})
	}
}

func TestHTTPHandlerExportsRouteSpanAndMetrics(t *testing.T) {
	exporter, s := newTestExporter(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := httptest.NewServer(exporter.HTTPHandler(mux))
	defer server.Close()

	for _, path := range []string{"/v1/things/secret-id-4711", "/healthz"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
	}
	if err := exporter.ForceFlush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	spans := s.spans.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want exactly 1 (the probe must not be traced): %+v", len(spans), spans)
	}
	span := spans[0]
	if span.Name != "GET /v1/things/{id}" {
		t.Fatalf("span name = %q, want the route pattern, never the raw path", span.Name)
	}
	if strings.Contains(span.Name, "secret-id-4711") {
		t.Fatalf("span name carries the raw path identifier: %q", span.Name)
	}
	attrs := map[string]string{}
	for _, kv := range span.Resource.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs["service.name"] != testService || attrs["service.version"] != testCommit {
		t.Fatalf("resource = %v, want service.name=%s service.version=%s", attrs, testService, testCommit)
	}
	var status string
	for _, kv := range span.Attributes {
		if kv.Key == "http.response.status_code" {
			status = kv.Value.Emit()
		}
	}
	if status != "418" {
		t.Fatalf("http.response.status_code = %q, want 418", status)
	}

	var rm metricdata.ResourceMetrics
	if err := s.metrics.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "http.server.request.duration" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("http.server.request.duration not exported: %+v", rm.ScopeMetrics)
	}
}

func TestLogHandlerTeesAtBaseLevelWithTraceCorrelation(t *testing.T) {
	exporter, s := newTestExporter(t)
	var stdout bytes.Buffer
	logger := slog.New(exporter.LogHandler(slog.NewJSONHandler(&stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))).With("component", "test")

	ctx, span := exporter.Tracer().Start(context.Background(), "parent")
	logger.InfoContext(ctx, "acr-mcp http request", "principal_ref", "ab12cd34ef56ab78", "result_class", "ok")
	logger.DebugContext(ctx, "below the base level", "principal_ref", "never")
	span.End()
	if err := exporter.ForceFlush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if !strings.Contains(stdout.String(), `"principal_ref":"ab12cd34ef56ab78"`) || strings.Contains(stdout.String(), "below the base level") {
		t.Fatalf("stdout stream changed: %s", stdout.String())
	}
	records := s.logs.all()
	if len(records) != 1 {
		t.Fatalf("exported %d log records, want exactly the 1 logged line", len(records))
	}
	r := records[0]
	if r.Body().AsString() != "acr-mcp http request" || r.Severity() != otellog.SeverityInfo {
		t.Fatalf("record body/severity = %q/%v", r.Body().AsString(), r.Severity())
	}
	got := map[string]string{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		got[string(kv.Key)] = kv.Value.Emit()
		return true
	})
	for key, want := range map[string]string{"principal_ref": "ab12cd34ef56ab78", "result_class": "ok", "component": "test"} {
		if got[key] != want {
			t.Fatalf("exported attribute %s = %q, want %q (all: %v)", key, got[key], want, got)
		}
	}
	if r.TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("record trace id %s, want the request span's %s", r.TraceID(), span.SpanContext().TraceID())
	}
	var service string
	for _, kv := range r.Resource().Attributes() {
		if kv.Key == "service.name" {
			service = kv.Value.Emit()
		}
	}
	if service != testService {
		t.Fatalf("log resource service.name = %q, want %q", service, testService)
	}
}

type plainHandler struct{}

func (*plainHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func TestDisabledExporterIsInert(t *testing.T) {
	for _, exporter := range []*Exporter{nil, {config: Config{ServiceName: "acr-api"}}} {
		handler := &plainHandler{}
		if got := exporter.HTTPHandler(handler); got != http.Handler(handler) {
			t.Fatal("a disabled exporter wrapped the handler")
		}
		base := slog.NewJSONHandler(&bytes.Buffer{}, nil)
		if got := exporter.LogHandler(base); got != slog.Handler(base) {
			t.Fatal("a disabled exporter replaced the log handler")
		}
		if err := exporter.ForceFlush(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := exporter.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, span := exporter.Tracer().Start(context.Background(), "x")
		if span.SpanContext().IsValid() {
			t.Fatal("a disabled exporter's tracer produced a recording span")
		}
	}
}

func TestNewDisabledBuildsNoExporters(t *testing.T) {
	exporter, err := New(context.Background(), Config{ServiceName: "acr-api"})
	if err != nil || exporter == nil || exporter.enabled() {
		t.Fatalf("New(disabled) = %+v, %v; want a non-nil disabled exporter", exporter, err)
	}
}

// TestLogStartCertified drives the real producer through a real JSON handler
// for both states and certifies the line against its eventspec declaration.
func TestLogStartCertified(t *testing.T) {
	enabled, _ := newTestExporter(t)
	disabled, err := New(context.Background(), Config{ServiceName: "acr-mcp", ServiceVersion: "v0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		exporter *Exporter
		want     map[string]any
	}{
		{enabled, map[string]any{"service_name": testService, "service_version": testCommit, "state": eventspec.OTelExportStateEnabled, "protocol": eventspec.OTelExportProtocolGRPC}},
		{disabled, map[string]any{"service_name": "acr-mcp", "service_version": "v0.1.0", "state": eventspec.OTelExportStateDisabled, "protocol": eventspec.OTelExportProtocolNone}},
	} {
		var out bytes.Buffer
		tc.exporter.LogStart(context.Background(), slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})))
		log, err := certify.Parse(out.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.OTelExport, Want: tc.want}); err != nil {
			t.Fatalf("certify: %v\n%s", err, out.String())
		}
	}
	var out bytes.Buffer
	(*Exporter)(nil).LogStart(context.Background(), slog.New(slog.NewJSONHandler(&out, nil)))
	if out.Len() != 0 {
		t.Fatalf("a nil exporter wrote a start line: %s", out.String())
	}
}
