package otelexport

import (
	"context"
	"errors"
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

const (
	configuredEndpointHost = "upstream.invalid"
	resolverAttemptBound   = 30 * time.Second
)

// queryRecorder is a stub name server: it reports the name of every DNS query written to it and fails the read, so a
// lookup ends at once and its duration does not depend on a real resolver.
type queryRecorder struct {
	names chan string
}

func (q *queryRecorder) Dial(context.Context, string, string) (net.Conn, error) {
	return &recordingConn{names: q.names}, nil
}

type recordingConn struct {
	names chan string
}

func (c *recordingConn) Write(b []byte) (int, error) {
	var labels []string
	for i := 12; i < len(b) && b[i] != 0; {
		n := int(b[i])
		if i+1+n > len(b) {
			break
		}
		labels = append(labels, string(b[i+1:i+1+n]))
		i += 1 + n
	}
	select {
	case c.names <- strings.Join(labels, "."):
	default:
	}
	return len(b), nil
}

func (c *recordingConn) Read([]byte) (int, error) {
	return 0, errors.New("recording resolver: no answer")
}
func (c *recordingConn) ReadFrom([]byte) (int, net.Addr, error) {
	return 0, nil, errors.New("recording resolver: no answer")
}
func (c *recordingConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.Write(b) }
func (c *recordingConn) Close() error                              { return nil }
func (c *recordingConn) LocalAddr() net.Addr                       { return &net.UDPAddr{} }
func (c *recordingConn) RemoteAddr() net.Addr                      { return &net.UDPAddr{} }
func (c *recordingConn) SetDeadline(time.Time) error               { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error           { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error          { return nil }

// namesConfiguredHost accepts the endpoint name itself, its search-domain expansions, and the service-config TXT name
// gRPC derives from it.
func namesConfiguredHost(name string) bool {
	name = strings.TrimPrefix(name, "_grpc_config.")
	return name == configuredEndpointHost || strings.HasPrefix(name, configuredEndpointHost+".")
}

func TestTheGRPCExportersNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		if err := os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+configuredEndpointHost+":4317"); err != nil {
			panic(err)
		}
		recorder := &queryRecorder{names: make(chan string, 64)}
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: recorder.Dial}
		spans, metrics, logs, err := newGRPCExporters(ctx)
		if err != nil {
			panic(err)
		}
		// Shutdown closes the exporter's channel and waits for its resolver, so no lookup of a finished exporter can be
		// read as the next one's.
		call := func(exporter string, send func(context.Context) error, shutdown func(context.Context) error) {
			for len(recorder.names) > 0 {
				<-recorder.names
			}
			callCtx, cancel := context.WithTimeout(ctx, resolverAttemptBound)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- send(callCtx) }()
			select {
			case name := <-recorder.names:
				if !namesConfiguredHost(name) {
					panic(fmt.Sprintf("the %s exporter looked up %q, want the configured endpoint %q", exporter, name, configuredEndpointHost))
				}
				cancel()
				err := <-done
				if shutdownErr := shutdown(ctx); shutdownErr != nil {
					panic(shutdownErr)
				}
				fmt.Printf("%s exporter looked up %q; export ended with: %v\n", exporter, name, err)
			case err := <-done:
				panic(fmt.Sprintf("the %s exporter returned before looking up the configured endpoint %q: %v", exporter, configuredEndpointHost, err))
			case <-callCtx.Done():
				panic(fmt.Sprintf("the %s exporter made no lookup of the configured endpoint %q before the call context ended (bound %s)", exporter, configuredEndpointHost, resolverAttemptBound))
			}
		}
		call("spans", func(c context.Context) error {
			return spans.ExportSpans(c, tracetest.SpanStubs{{Name: "probe"}}.Snapshots())
		}, spans.Shutdown)
		call("metrics", func(c context.Context) error { return metrics.Export(c, &metricdata.ResourceMetrics{}) }, metrics.Shutdown)
		call("logs", func(c context.Context) error { return logs.Export(c, make([]sdklog.Record, 1)) }, logs.Shutdown)
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
