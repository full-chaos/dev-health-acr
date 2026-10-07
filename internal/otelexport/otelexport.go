// Package otelexport builds the OTLP export of a hosted ACR process's own
// service telemetry: its HTTP server spans, its HTTP server metrics, and a
// copy of its structured slog lines. It is off unless OTEL_ENABLED is true.
//
// NEVER GLOBAL. Every provider this package builds is held by the Exporter and
// handed explicitly to the one handler and the one log handler that use it.
// Nothing here calls otel.SetTracerProvider, otel.SetMeterProvider,
// global.SetLoggerProvider, otel.SetTextMapPropagator or slog.SetDefault. That
// is what keeps the Genkit guarantee in
// internal/contextfabric/modelprovider (suppressGenkitTelemetryExport): Genkit
// reads only the GLOBAL tracer and meter providers and slog.Default, so its
// spans (which carry model prompt and response content as attributes), its
// metrics and its own log lines reach none of the exporters built here. A
// span only reaches the exporter of the provider that created it; a Genkit
// span that is a child of an exported HTTP span (same trace ID) is still
// created by the global, exporter-less provider. TestGenkitTelemetryNeverExported
// in modelprovider and TestNoGlobalTelemetryInstallOutsideModelProvider here
// pin both halves.
//
// What IS exported as logs is exactly the process's own JSON stdout/stderr
// stream: the same records, gated at the same level, through the same
// logger. No log line gains a field by being exported.
package otelexport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/version"
)

// Environment variables. OTEL_EXPORTER_OTLP_ENDPOINT (and every other
// standard OTEL_EXPORTER_OTLP_* variable) is read by the OTLP exporters
// themselves; this package only requires it to be set when export is on.
const (
	EnvEnabled     = "OTEL_ENABLED"
	EnvEndpoint    = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvServiceName = "OTEL_SERVICE_NAME"
)

// instrumentationScope names this package as the producer of the HTTP spans,
// metrics and bridged log records.
const instrumentationScope = "github.com/full-chaos/dev-health-acr/internal/otelexport"

// Config is the resolved export configuration of one process.
type Config struct {
	Enabled bool
	// ServiceName is service.name: OTEL_SERVICE_NAME, or the binary's own
	// default name.
	ServiceName string
	// ServiceVersion is service.version: the build commit.
	ServiceVersion string
}

// ConfigFromEnv resolves Config. defaultServiceName is the binary's own name
// (acr-api, acr-mcp, acr-projector). An OTEL_ENABLED value that is not a
// boolean, or OTEL_ENABLED=true without an endpoint, is an error: a process
// told to export must not start silently exporting nowhere.
func ConfigFromEnv(lookup func(string) (string, bool), defaultServiceName string, info version.Info) (Config, error) {
	cfg := Config{ServiceName: defaultServiceName, ServiceVersion: serviceVersion(info)}
	if name, ok := lookup(EnvServiceName); ok && strings.TrimSpace(name) != "" {
		cfg.ServiceName = strings.TrimSpace(name)
	}
	raw, ok := lookup(EnvEnabled)
	if !ok || strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return Config{}, fmt.Errorf("%s must be a boolean", EnvEnabled)
	}
	if enabled {
		endpoint, ok := lookup(EnvEndpoint)
		endpoint = strings.TrimSpace(endpoint)
		if !ok || endpoint == "" {
			return Config{}, fmt.Errorf("%s=true requires %s", EnvEnabled, EnvEndpoint)
		}
		// A hostless endpoint ("http://:4317") parses, and the OTLP exporter
		// then dials this pod's own loopback: the process looks configured
		// while every export is refused locally. Fail at startup instead.
		if parsed, err := url.Parse(endpoint); err != nil || parsed.Hostname() == "" {
			return Config{}, fmt.Errorf("%s must name a collector host", EnvEndpoint)
		}
	}
	cfg.Enabled = enabled
	return cfg, nil
}

// serviceVersion is the build commit, or the version when no commit was
// stamped (a local build).
func serviceVersion(info version.Info) string {
	if info.Commit != "" && info.Commit != "unknown" {
		return info.Commit
	}
	return info.Version
}

// Exporter holds one process's explicit (never global) providers. A disabled
// Exporter, and a nil *Exporter, export nothing: every method is safe on
// both, so a disabled process runs the same code path.
type Exporter struct {
	config      Config
	traces      *sdktrace.TracerProvider
	metrics     *sdkmetric.MeterProvider
	logs        *sdklog.LoggerProvider
	propagators propagation.TextMapPropagator
	serviceName string
}

// Exporters are the sinks behind an Exporter. New builds OTLP/gRPC ones;
// tests pass in-memory ones.
type Exporters struct {
	Spans   sdktrace.SpanExporter
	Metrics sdkmetric.Reader
	Logs    sdklog.Exporter
	// Synchronous exports each span and log record as it ends instead of in
	// batches. Tests only.
	Synchronous bool
}

// New builds the OTLP/gRPC Exporter for cfg; a disabled cfg builds a
// disabled Exporter that only writes the process-start line.
func New(ctx context.Context, cfg Config) (*Exporter, error) {
	if !cfg.Enabled {
		return &Exporter{config: cfg}, nil
	}
	spans, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}
	metrics, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("otlp metric exporter: %w", err), spans.Shutdown(ctx))
	}
	logs, err := otlploggrpc.New(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("otlp log exporter: %w", err), spans.Shutdown(ctx), metrics.Shutdown(ctx))
	}
	return NewWithExporters(ctx, cfg, Exporters{
		Spans:   spans,
		Metrics: sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(30*time.Second)),
		Logs:    logs,
	})
}

// NewWithExporters builds an Exporter over the given sinks.
func NewWithExporters(ctx context.Context, cfg Config, sinks Exporters) (*Exporter, error) {
	if sinks.Spans == nil || sinks.Metrics == nil || sinks.Logs == nil {
		return nil, errors.New("otelexport: span, metric and log sinks are all required")
	}
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil && res == nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}
	spanProcessor := sdktrace.NewBatchSpanProcessor(sinks.Spans)
	logProcessor := sdklog.Processor(sdklog.NewBatchProcessor(sinks.Logs))
	if sinks.Synchronous {
		spanProcessor = sdktrace.NewSimpleSpanProcessor(sinks.Spans)
		logProcessor = sdklog.NewSimpleProcessor(sinks.Logs)
	}
	return &Exporter{
		config:      cfg,
		traces:      sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSpanProcessor(spanProcessor)),
		metrics:     sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sinks.Metrics)),
		logs:        sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(logProcessor)),
		propagators: propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
		serviceName: cfg.ServiceName,
	}, nil
}

// probePaths are kubelet probe routes: a span per probe would bury request
// spans, so they are not traced.
var probePaths = map[string]bool{"/healthz": true, "/readyz": true, "/livez": true}

// HTTPHandler wraps next in a server span and the HTTP server metrics, on
// this Exporter's own providers. A nil Exporter returns next unchanged.
func (e *Exporter) HTTPHandler(next http.Handler) http.Handler {
	if !e.enabled() {
		return next
	}
	return otelhttp.NewHandler(next, e.serviceName,
		otelhttp.WithTracerProvider(e.traces),
		otelhttp.WithMeterProvider(e.metrics),
		otelhttp.WithPropagators(e.propagators),
		otelhttp.WithFilter(func(r *http.Request) bool { return !probePaths[r.URL.Path] }),
		otelhttp.WithSpanNameFormatter(spanName),
	)
}

// spanName is "METHOD route" once the mux matched a pattern, else
// "METHOD" alone -- never the raw path, which carries identifiers.
func spanName(_ string, r *http.Request) string {
	if route := patternRoute(r.Pattern); route != "" {
		return r.Method + " " + route
	}
	return r.Method
}

// patternRoute is the path part of a ServeMux pattern ("GET /a/{id}" ->
// "/a/{id}").
func patternRoute(pattern string) string {
	if _, route, ok := strings.Cut(pattern, " "); ok {
		return route
	}
	return pattern
}

// RouteNamer wraps a *http.ServeMux (or any handler that sets r.Pattern on
// the request it is given) so the enclosing server span is named by the
// matched route. It is needed when middleware sits between HTTPHandler and
// the mux: the mux then sets Pattern on a request copy HTTPHandler never
// sees. It names whatever span the request context carries and is inert
// when that span does not record (export disabled).
func RouteNamer(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		route := patternRoute(r.Pattern)
		if route == "" {
			return
		}
		span := trace.SpanFromContext(r.Context())
		if !span.IsRecording() {
			return
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(semconv.HTTPRoute(route))
	})
}

// LogHandler returns a handler that writes every record base accepts to base
// AND exports it through this Exporter's own LoggerProvider, gated at the
// same level as base. A nil Exporter returns base unchanged.
func (e *Exporter) LogHandler(base slog.Handler) slog.Handler {
	if !e.enabled() {
		return base
	}
	bridged := otelslog.NewHandler(instrumentationScope, otelslog.WithLoggerProvider(e.logs))
	return teeHandler{primary: base, export: bridged}
}

// teeHandler writes to primary and, for every record primary is enabled for,
// to export. primary decides the level, so exported logs are exactly the
// logged lines.
type teeHandler struct {
	primary slog.Handler
	export  slog.Handler
}

func (h teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.primary.Enabled(ctx, level)
}

func (h teeHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.primary.Handle(ctx, record.Clone())
	if h.export.Enabled(ctx, record.Level) {
		err = errors.Join(err, h.export.Handle(ctx, record))
	}
	return err
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{primary: h.primary.WithAttrs(attrs), export: h.export.WithAttrs(attrs)}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{primary: h.primary.WithGroup(name), export: h.export.WithGroup(name)}
}

// LogStart writes the process-start line: whether this process exports, as
// which service. It is written to logger, so when export is on the line is
// itself the first exported log record. A nil Exporter writes nothing.
//
// At Info, like every other declared event in this repository: a deployment
// running ACR_LOG_LEVEL above info suppresses this line together with every
// other Info line (the per-request lines included), while spans and metrics
// still export. That is the service-wide level contract, not an exception
// this line gets to opt out of -- see docs/operations.md.
func (e *Exporter) LogStart(ctx context.Context, logger *slog.Logger) {
	if e == nil || logger == nil {
		return
	}
	cfg := e.config
	state, protocol := eventspec.OTelExportStateDisabled, eventspec.OTelExportProtocolNone
	if cfg.Enabled {
		state, protocol = eventspec.OTelExportStateEnabled, eventspec.OTelExportProtocolGRPC
	}
	fields := eventspec.NewOTelExportFields(cfg.ServiceName, cfg.ServiceVersion, state, protocol)
	logger.InfoContext(ctx, eventspec.OTelExportLogMessage, fields.SlogArgs()...)
}

// Shutdown flushes and stops every provider. A nil Exporter is a no-op.
func (e *Exporter) Shutdown(ctx context.Context) error {
	if !e.enabled() {
		return nil
	}
	return errors.Join(e.traces.Shutdown(ctx), e.metrics.Shutdown(ctx), e.logs.Shutdown(ctx))
}

// ForceFlush exports everything buffered. A nil Exporter is a no-op.
func (e *Exporter) ForceFlush(ctx context.Context) error {
	if !e.enabled() {
		return nil
	}
	return errors.Join(e.traces.ForceFlush(ctx), e.metrics.ForceFlush(ctx), e.logs.ForceFlush(ctx))
}

// enabled reports whether e holds live providers.
func (e *Exporter) enabled() bool {
	return e != nil && e.traces != nil
}

// Meter returns a meter on this Exporter's own MeterProvider. A disabled or
// nil Exporter returns a no-op meter.
func (e *Exporter) Meter() metric.Meter {
	if !e.enabled() {
		return metricnoop.NewMeterProvider().Meter(instrumentationScope)
	}
	return e.metrics.Meter(instrumentationScope)
}

// Tracer returns a tracer on this Exporter's own TracerProvider, for work
// that is not an HTTP request (the projector's projection tick). A disabled
// or nil Exporter returns a no-op tracer.
func (e *Exporter) Tracer() trace.Tracer {
	if !e.enabled() {
		return tracenoop.NewTracerProvider().Tracer(instrumentationScope)
	}
	return e.traces.Tracer(instrumentationScope)
}
