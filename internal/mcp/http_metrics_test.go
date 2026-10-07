package mcp_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/otelexport"
)

// TestServeHTTPCountsToolCallsAndNamesTheSpan drives the serve command's own
// path with a live exporter and reads what the client's tools/call left in the
// metric reader and on the server span: the counter by tool, result class and
// status class; the latency histogram by tool; the span attributes.
func TestServeHTTPCountsToolCallsAndNamesTheSpan(t *testing.T) {
	hosted := newHostedAPI(t)
	valid := hosted.issue(readScopes, []string{repoPlain}, nil)

	spans := tracetest.NewInMemoryExporter()
	reader := sdkmetric.NewManualReader()
	exporter, err := otelexport.NewWithExporters(context.Background(),
		otelexport.Config{Enabled: true, ServiceName: "acr-mcp", ServiceVersion: testIdentity.Commit},
		otelexport.Exporters{Spans: spans, Metrics: reader, Logs: &otelLogSink{}, Synchronous: true})
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- acrmcp.ServeHTTPOnForTest(ctx, listener, hosted.sidecarConfig(), testIdentity, &syncBuffer{}, opts)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	}()

	e := &endpoint{server: &httptest.Server{URL: "http://" + listener.Addr().String()}, logs: &syncBuffer{}}
	for _, call := range [][]byte{
		rawToolsCall("source_evidence", map[string]any{"evidence_ref_id": "evidence_0001"}),
		rawToolsCall("source_evidence", map[string]any{"evidence_ref_id": evidenceMissing}),
		rawToolsList(),
	} {
		resp := postMCP(t, e, http.MethodPost, call, bearerHeader(valid.token))
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if err := exporter.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int64{}
	latencyCount := map[string]uint64{}
	var bounds []float64
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Sum[int64]:
				if m.Name != hostedmetrics.ToolCallsName {
					continue
				}
				for _, point := range data.DataPoints {
					tool, _ := point.Attributes.Value("tool")
					class, _ := point.Attributes.Value("result_class")
					status, _ := point.Attributes.Value("status")
					calls[tool.AsString()+"/"+class.AsString()+"/"+status.AsString()] += point.Value
				}
			case metricdata.Histogram[float64]:
				if m.Name != hostedmetrics.ToolLatencyName {
					continue
				}
				for _, point := range data.DataPoints {
					tool, _ := point.Attributes.Value("tool")
					latencyCount[tool.AsString()] += point.Count
					bounds = point.Bounds
				}
			}
		}
	}
	if calls["source_evidence/ok/2xx"] != 1 || calls["source_evidence/tool_error/2xx"] != 1 || len(calls) != 2 {
		t.Fatalf("tool call counter %v, want one ok and one tool_error source_evidence call and nothing for tools/list", calls)
	}
	if latencyCount["source_evidence"] != 2 || len(latencyCount) != 1 {
		t.Fatalf("tool latency counts %v, want 2 for source_evidence only", latencyCount)
	}
	if want := hostedmetrics.LatencyBucketsSeconds(); len(bounds) != len(want) || bounds[len(bounds)-1] != 300 {
		t.Fatalf("latency bounds %v, want %v", bounds, want)
	}

	spanCells := map[string]int{}
	for _, s := range spans.GetSpans() {
		if s.Name != "POST /mcp" {
			continue
		}
		got := map[string]string{}
		for _, kv := range s.Attributes {
			if kv.Key == acrmcp.SpanAttributeTool || kv.Key == acrmcp.SpanAttributeResultClass {
				got[string(kv.Key)] = kv.Value.AsString()
			}
		}
		spanCells[got[acrmcp.SpanAttributeTool]+"/"+got[acrmcp.SpanAttributeResultClass]]++
	}
	if spanCells["source_evidence/ok"] != 1 || spanCells["source_evidence/tool_error"] != 1 || spanCells["none/ok"] != 1 || len(spanCells) != 3 {
		t.Fatalf("span attribute cells %v, want one each of source_evidence/ok, source_evidence/tool_error, none/ok", spanCells)
	}
}
