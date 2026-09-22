package mcp_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/otelexport"
)

type otelLogSink struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (m *otelLogSink) Export(_ context.Context, records []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range records {
		m.records = append(m.records, r.Clone())
	}
	return nil
}
func (m *otelLogSink) Shutdown(context.Context) error   { return nil }
func (m *otelLogSink) ForceFlush(context.Context) error { return nil }

func (m *otelLogSink) byBody(body string) []sdklog.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sdklog.Record
	for _, r := range m.records {
		if r.Body().AsString() == body {
			out = append(out, r)
		}
	}
	return out
}

func recordAttrs(r sdklog.Record) map[string]string {
	attrs := map[string]string{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		attrs[string(kv.Key)] = kv.Value.Emit()
		return true
	})
	return attrs
}

// TestServeHTTPExportsRequestLineAndSpan drives the serve command's own path
// (serveHTTPOn) with a live exporter: one admitted tools/list request must
// reach the export as a server span AND as the "acr-mcp http request" line,
// carrying the same principal_ref and result_class the stderr line carries,
// correlated to the span's trace. The stderr stream stays as it was.
func TestServeHTTPExportsRequestLineAndSpan(t *testing.T) {
	hosted := newHostedAPI(t)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)

	spans := tracetest.NewInMemoryExporter()
	logs := &otelLogSink{}
	exporter, err := otelexport.NewWithExporters(context.Background(),
		otelexport.Config{Enabled: true, ServiceName: "acr-mcp", ServiceVersion: testIdentity.Commit},
		otelexport.Exporters{Spans: spans, Metrics: sdkmetric.NewManualReader(), Logs: logs, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exporter.Shutdown(context.Background()) }()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	opts := acrmcp.DefaultServeOptions()
	opts.Transport = acrmcp.TransportHTTP
	opts.Listen = listener.Addr().String()
	opts.Telemetry = exporter
	stderr := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- acrmcp.ServeHTTPOnForTest(ctx, listener, hosted.sidecarConfig(), testIdentity, stderr, opts)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	}()

	e := &endpoint{server: &httptest.Server{URL: "http://" + listener.Addr().String()}, logs: stderr}
	resp := postMCP(t, e, http.MethodPost, rawToolsList(), withRequestID(bearerHeader(valid.token), "req-otel-1"))
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := exporter.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	// stderr: the certified line, unchanged by export.
	log, err := certify.Parse(stderr.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	result, err := certify.Certify(log, certify.Assertion{Event: eventspec.MCPHTTPRequest, Want: map[string]any{
		"request_id": "req-otel-1", "principal_class": "bearer", "result_class": "ok", "auth_outcome": "admitted",
	}})
	if err != nil {
		t.Fatal(err)
	}
	stderrRef, _ := result.Line["principal_ref"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(stderrRef) {
		t.Fatalf("stderr principal_ref %q", stderrRef)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.OTelExport, Want: map[string]any{
		"service_name": "acr-mcp", "state": "enabled", "protocol": "grpc",
	}}); err != nil {
		t.Fatal(err)
	}

	// export: the server span.
	var serverSpan *tracetest.SpanStub
	for _, s := range spans.GetSpans() {
		if s.Name == "POST /mcp" {
			s := s
			serverSpan = &s
		}
	}
	if serverSpan == nil {
		t.Fatalf("no POST server span exported; got %d spans", len(spans.GetSpans()))
	}

	// export: the request line, same values, correlated to the span.
	lines := logs.byBody(eventspec.MCPHTTPRequestLogMessage)
	if len(lines) != 1 {
		t.Fatalf("exported %d request lines, want 1", len(lines))
	}
	attrs := recordAttrs(lines[0])
	if attrs["principal_ref"] != stderrRef || attrs["result_class"] != "ok" || attrs["request_id"] != "req-otel-1" {
		t.Fatalf("exported request line attrs %v, want principal_ref=%s result_class=ok", attrs, stderrRef)
	}
	if lines[0].TraceID() != serverSpan.SpanContext.TraceID() {
		t.Fatalf("request line trace %s, server span trace %s", lines[0].TraceID(), serverSpan.SpanContext.TraceID())
	}
	if len(logs.byBody(eventspec.OTelExportLogMessage)) != 1 {
		t.Fatal("the process-start export line was not itself exported")
	}
}
