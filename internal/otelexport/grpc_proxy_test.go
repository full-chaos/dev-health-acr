package otelexport

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheGRPCExportersNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		if err := os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://upstream.invalid:4317"); err != nil {
			panic(err)
		}
		spans, metrics, logs, err := newGRPCExporters(ctx)
		if err != nil {
			panic(err)
		}
		call := func(send func(context.Context) error) {
			callCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			if err := send(callCtx); err == nil || !strings.Contains(err.Error(), "name resolver error") {
				panic(fmt.Sprintf("export did not attempt the configured endpoint: %v", err))
			}
		}
		call(func(c context.Context) error {
			return spans.ExportSpans(c, tracetest.SpanStubs{{Name: "probe"}}.Snapshots())
		})
		call(func(c context.Context) error { return metrics.Export(c, &metricdata.ResourceMetrics{}) })
		call(func(c context.Context) error { return logs.Export(c, make([]sdklog.Record, 1)) })
	}).AssertNothingReached(t)
}

type traceCollector struct {
	coltracepb.UnimplementedTraceServiceServer
	headers chan metadata.MD
}

func (c *traceCollector) Export(ctx context.Context, _ *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	select {
	case c.headers <- md:
	default:
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func TestTheOTLPHeadersEnvironmentVariableStillReachesTheCollector(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	collector := &traceCollector{headers: make(chan metadata.MD, 1)}
	server := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(server, collector)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-probe=probe-value")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	spans, metrics, logs, err := newGRPCExporters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = spans.Shutdown(ctx)
		_ = metrics.Shutdown(ctx)
		_ = logs.Shutdown(ctx)
	}()
	if err := spans.ExportSpans(ctx, tracetest.SpanStubs{{Name: "probe"}}.Snapshots()); err != nil {
		t.Fatal(err)
	}
	select {
	case md := <-collector.headers:
		if got := md.Get("x-probe"); len(got) != 1 || got[0] != "probe-value" {
			t.Fatalf("x-probe metadata = %v, want [probe-value]", got)
		}
	case <-ctx.Done():
		t.Fatal("collector received no export")
	}
}
