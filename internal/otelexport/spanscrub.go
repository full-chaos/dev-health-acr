package otelexport

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

const (
	redactedValue   = telemetryscrub.Redacted
	maxSpanTextRune = 256
	fallbackName    = "span"
)

var knownMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "CONNECT": true, "OPTIONS": true, "TRACE": true,
}

// scrubSpanExporter sits in front of the real span exporter. A recorded span is immutable and a processor sees only a
// read-only snapshot, so the scrub is the exporter: every span leaves the process through it. Exported spans carry only
// the safe attribute keys, a closed name, scrubbed values, no event text, no link attributes, no status text.
type scrubSpanExporter struct{ sdktrace.SpanExporter }

func (e scrubSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, span := range spans {
		out[i] = scrubbedSpan{ReadOnlySpan: span}
	}
	return e.SpanExporter.ExportSpans(ctx, out)
}

type scrubbedSpan struct{ sdktrace.ReadOnlySpan }

func clip(s string) string {
	s = telemetryscrub.ScrubText(s)
	if r := []rune(s); len(r) > maxSpanTextRune {
		return string(r[:maxSpanTextRune])
	}
	return s
}

func scrubValue(kv attribute.KeyValue) attribute.KeyValue {
	switch kv.Value.Type() {
	case attribute.STRING:
		return attribute.String(string(kv.Key), clip(kv.Value.AsString()))
	case attribute.STRINGSLICE:
		in := kv.Value.AsStringSlice()
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = clip(s)
		}
		return attribute.StringSlice(string(kv.Key), out)
	}
	return kv
}

func (s scrubbedSpan) Attributes() []attribute.KeyValue {
	var out []attribute.KeyValue
	for _, kv := range s.ReadOnlySpan.Attributes() {
		if want, safe := safeSpanKeys[string(kv.Key)]; !safe || kv.Value.Type() != want {
			continue
		}
		out = append(out, scrubValue(kv))
	}
	return out
}

// SpanContext keeps the trace and span ids and drops the trace state: its text comes from the incoming tracestate header.
func (s scrubbedSpan) SpanContext() trace.SpanContext {
	return s.ReadOnlySpan.SpanContext().WithTraceState(trace.TraceState{})
}

// Parent keeps the ids of the incoming parent and drops its trace state for the same reason.
func (s scrubbedSpan) Parent() trace.SpanContext {
	return s.ReadOnlySpan.Parent().WithTraceState(trace.TraceState{})
}

// plainValue reports a value type that carries no nested keys: nested maps and slices, byte slices and empty values are
// never exported.
func plainValue(kv attribute.KeyValue) bool {
	switch kv.Value.Type() {
	case attribute.BOOL, attribute.INT64, attribute.FLOAT64, attribute.STRING, attribute.BOOLSLICE, attribute.INT64SLICE, attribute.FLOAT64SLICE, attribute.STRINGSLICE:
		return true
	}
	return false
}

// Name is one of: "<METHOD> <route template>", "HTTP <METHOD>", "HTTP", a closed internal name, or "span". It is never
// built from a host or a raw path: the route comes from the http.route attribute (the matched mux pattern).
func (s scrubbedSpan) Name() string {
	method, route := "", ""
	for _, kv := range s.ReadOnlySpan.Attributes() {
		switch kv.Key {
		case "http.request.method":
			method = kv.Value.AsString()
		case "http.route":
			route = kv.Value.AsString()
		}
	}
	if method != "" || route != "" {
		if !knownMethods[method] {
			return "HTTP"
		}
		if strings.HasPrefix(route, "/") && !strings.Contains(route, "://") {
			return method + " " + clip(route)
		}
		return "HTTP " + method
	}
	if safeInternalSpanNames[s.ReadOnlySpan.Name()] {
		return s.ReadOnlySpan.Name()
	}
	return fallbackName
}

func (s scrubbedSpan) Events() []sdktrace.Event {
	in := s.ReadOnlySpan.Events()
	out := make([]sdktrace.Event, len(in))
	for i, ev := range in {
		ev.Name = "event"
		if in[i].Name == "exception" {
			ev.Name = "exception"
		}
		ev.Attributes = nil
		for _, kv := range in[i].Attributes {
			if safeEventKeys[string(kv.Key)] && kv.Value.Type() == attribute.STRING {
				ev.Attributes = append(ev.Attributes, scrubValue(kv))
			}
		}
		out[i] = ev
	}
	return out
}

func (s scrubbedSpan) Links() []sdktrace.Link {
	in := s.ReadOnlySpan.Links()
	out := make([]sdktrace.Link, len(in))
	for i, l := range in {
		out[i] = sdktrace.Link{SpanContext: l.SpanContext, DroppedAttributeCount: l.DroppedAttributeCount}
	}
	return out
}

// Status keeps the code and drops the description: error text carries hosts, paths and request content.
func (s scrubbedSpan) Status() sdktrace.Status {
	st := s.ReadOnlySpan.Status()
	st.Description = ""
	return st
}

func (s scrubbedSpan) Resource() *resource.Resource {
	in := s.ReadOnlySpan.Resource()
	if in == nil {
		return nil
	}
	var attrs []attribute.KeyValue
	for _, kv := range in.Attributes() {
		if !plainValue(kv) {
			continue
		}
		if protected, network := telemetryscrub.KeyClass(string(kv.Key)); protected || network {
			attrs = append(attrs, attribute.String(string(kv.Key), redactedValue))
			continue
		}
		attrs = append(attrs, scrubValue(kv))
	}
	return resource.NewWithAttributes(in.SchemaURL(), attrs...)
}
