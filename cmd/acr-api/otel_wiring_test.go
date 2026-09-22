package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/otelexport"
)

type discardLogs struct{}

func (discardLogs) Export(context.Context, []sdklog.Record) error { return nil }
func (discardLogs) Shutdown(context.Context) error                { return nil }
func (discardLogs) ForceFlush(context.Context) error              { return nil }

// TestPrepareServerServesThroughTheExporterHandler pins that serve's
// wrapHandler reaches the server: the handler prepareServer hands to
// newServer produces an exported server span named by the matched route.
func TestPrepareServerServesThroughTheExporterHandler(t *testing.T) {
	cfg := developmentServeConfig(t)
	cfg.RequireBackingStores = false
	spans := tracetest.NewInMemoryExporter()
	exporter, err := otelexport.NewWithExporters(context.Background(), otelexport.Config{Enabled: true, ServiceName: "acr-api", ServiceVersion: "test"},
		otelexport.Exporters{Spans: spans, Metrics: sdkmetric.NewManualReader(), Logs: discardLogs{}, Synchronous: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exporter.Shutdown(context.Background()) }()

	var served http.Handler
	_, closeRuntime, err := prepareServer(context.Background(), serverBuildRequest{
		config: cfg, logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), serviceVersion: "test",
		wrapHandler: exporter.HTTPHandler,
		newServer: func(_ api.ServerConfig, handler http.Handler, _ *slog.Logger) (serverRunner, error) {
			served = handler
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closeRuntime != nil {
		defer func() { _ = closeRuntime() }()
	}
	recorder := httptest.NewRecorder()
	served.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil))
	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("exported %d spans, want 1", len(got))
	}
	if got[0].Name != "GET /api/v1/agent-context/capabilities" {
		t.Fatalf("span name %q, want the matched route", got[0].Name)
	}
}
