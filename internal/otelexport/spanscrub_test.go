package otelexport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

func attrMap(kvs []attribute.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range kvs {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

func exportStubs(t *testing.T, stubs ...tracetest.SpanStub) []tracetest.SpanStub {
	t.Helper()
	sink := tracetest.NewInMemoryExporter()
	spans := make([]sdktrace.ReadOnlySpan, len(stubs))
	for i, stub := range stubs {
		spans[i] = stub.Snapshot()
	}
	if err := (scrubSpanExporter{SpanExporter: sink}).ExportSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	return sink.GetSpans()
}

func TestSpanScrubDropsClientAndPathAttributes(t *testing.T) {
	// control: the unscrubbed otelhttp handler really produces the attributes the scrub must remove
	raw := tracetest.NewInMemoryExporter()
	rawProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(raw))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	request := func(handler http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/v1/things/secret-id-4711", nil)
		req.Header.Set("User-Agent", "probe-agent/9.9")
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		req.RemoteAddr = "198.51.100.9:4455"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	request(otelhttp.NewHandler(mux, "svc", otelhttp.WithTracerProvider(rawProvider)))
	rawAttrs := attrMap(raw.GetSpans()[0].Attributes)
	for _, key := range []string{"url.path", "user_agent.original", "client.address", "network.peer.address"} {
		if _, ok := rawAttrs[key]; !ok {
			t.Fatalf("control: the unscrubbed span has no %s (%v): the test would pass vacuously", key, rawAttrs)
		}
	}

	exporter, s := newTestExporter(t)
	request(exporter.HTTPHandler(mux))
	if err := exporter.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := s.spans.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported %d spans, want 1", len(spans))
	}
	got := attrMap(spans[0].Attributes)
	for key, value := range got {
		if !SafeSpanKey(key) {
			t.Errorf("exported key %q is not a safe span key", key)
		}
		for _, secret := range []string{"secret-id-4711", "probe-agent", "203.0.113.7", "198.51.100.9"} {
			if strings.Contains(value, secret) {
				t.Errorf("attribute %s=%q carries %q", key, value, secret)
			}
		}
	}
	if got["http.request.method"] != "GET" || got["http.response.status_code"] != "418" {
		t.Errorf("method/status lost: %v", got)
	}
	if spans[0].Name != "GET /v1/things/{id}" {
		t.Errorf("name = %q, want the closed form METHOD + route template", spans[0].Name)
	}
}

func TestSafeSpanKeyIsExact(t *testing.T) {
	for _, key := range []string{"acr.tool", "acr.result_class", "acr.query_version", "http.route", "http.request.method", "http.response.status_code", "url.scheme", "error.type"} {
		if !SafeSpanKey(key) {
			t.Errorf("%s must be safe", key)
		}
	}
	for _, key := range append([]string{"acr.tool.x", "http.route2", "HTTP.ROUTE", "acr.", ""}, neverSafeSpanKeys...) {
		if SafeSpanKey(key) {
			t.Errorf("%q must not be safe", key)
		}
	}
}

func TestSafeSpanKeysAreCleanUnderTheLogClassifier(t *testing.T) {
	for key := range safeSpanKeys {
		if protected, network := telemetryscrub.KeyClass(key); protected || network {
			t.Errorf("safe key %q is protected=%v network=%v under the log classifier", key, protected, network)
		}
	}
	// control: the classifier does flag the address keys the set must never admit
	if _, network := telemetryscrub.KeyClass("client.address"); !network {
		t.Fatal("control: client.address is not a network key under the classifier")
	}
}

func TestSpanScrubRedactsAndCapsValuesOfSafeKeys(t *testing.T) {
	got := exportStubs(t, tracetest.SpanStub{
		Name: "x", SpanKind: trace.SpanKindServer,
		Attributes: []attribute.KeyValue{
			attribute.String("acr.tool", "Bearer abcdefghijklmnop"),
			attribute.String("acr.query_version", strings.Repeat("v", 300)),
			attribute.String("unknown.key", "value"),
		},
	})
	attrs := attrMap(got[0].Attributes)
	if strings.Contains(attrs["acr.tool"], "abcdefghijklmnop") {
		t.Errorf("bearer shape exported: %q", attrs["acr.tool"])
	}
	if len([]rune(attrs["acr.query_version"])) != maxSpanTextRune {
		t.Errorf("value not capped: %d runes", len([]rune(attrs["acr.query_version"])))
	}
	if _, ok := attrs["unknown.key"]; ok {
		t.Error("an unknown key was exported")
	}
}

func TestSpanScrubServerAddressOnlyOnServerSpans(t *testing.T) {
	attrs := []attribute.KeyValue{attribute.String("server.address", "tenant-x.example.com"), attribute.Int("server.port", 8443), attribute.String("http.request.method", "GET")}
	for _, kind := range []trace.SpanKind{trace.SpanKindClient, trace.SpanKindInternal, trace.SpanKindProducer} {
		got := attrMap(exportStubs(t, tracetest.SpanStub{Name: "x", SpanKind: kind, Attributes: attrs})[0].Attributes)
		if _, ok := got["server.address"]; ok {
			t.Errorf("%v span exported server.address", kind)
		}
		if _, ok := got["server.port"]; ok {
			t.Errorf("%v span exported server.port", kind)
		}
	}
	got := attrMap(exportStubs(t, tracetest.SpanStub{Name: "x", SpanKind: trace.SpanKindServer, Attributes: attrs})[0].Attributes)
	if got["server.address"] != "tenant-x.example.com" || got["server.port"] != "8443" {
		t.Errorf("server span lost its own listener: %v", got)
	}
}

func TestSpanScrubNameStatusEventsLinksAndResource(t *testing.T) {
	ctx := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	res := resource.NewWithAttributes("", attribute.String("service.name", "acr"), attribute.String("api_token", "plain-secret-value"), attribute.String("note", "Bearer abcdefghijklmnop"))
	got := exportStubs(t,
		tracetest.SpanStub{
			Name: "GET /v1/things/secret-4711", SpanKind: trace.SpanKindClient,
			Status: sdktrace.Status{Code: codes.Error, Description: "dial tcp tenant-x.example.com:443: i/o timeout"},
			Events: []sdktrace.Event{{Name: "exception", Attributes: []attribute.KeyValue{
				attribute.String("exception.type", "net.OpError"), attribute.String("exception.message", "host tenant-x.example.com"), attribute.String("exception.stacktrace", "frame secret"),
			}}, {Name: "GET https://tenant-x.example.com/x", Attributes: []attribute.KeyValue{attribute.String("exception.type", "x")}}},
			Links:    []sdktrace.Link{{SpanContext: ctx, Attributes: []attribute.KeyValue{attribute.String("peer", "tenant-x.example.com")}}},
			Resource: res,
		},
		tracetest.SpanStub{Name: "GET /v1/things/secret-4711", SpanKind: trace.SpanKindServer, Attributes: []attribute.KeyValue{attribute.String("http.request.method", "GET")}},
		tracetest.SpanStub{Name: "projection tick", SpanKind: trace.SpanKindInternal},
		tracetest.SpanStub{Name: "tenant-x.example.com", SpanKind: trace.SpanKindClient, Attributes: []attribute.KeyValue{attribute.String("http.request.method", "BREW")}},
	)
	span := got[0]
	if span.Name != "span" {
		t.Errorf("a non-HTTP name outside the closed set must become %q, got %q", "span", span.Name)
	}
	if span.Status.Description != "" || span.Status.Code != codes.Error {
		t.Errorf("status = %+v, want the code kept and no text", span.Status)
	}
	for _, ev := range span.Events {
		for _, kv := range ev.Attributes {
			if string(kv.Key) != "exception.type" {
				t.Errorf("event kept attribute %s", kv.Key)
			}
		}
		if strings.Contains(ev.Name, "tenant-x") {
			t.Errorf("event name %q carries a host", ev.Name)
		}
	}
	if len(span.Events) != 2 || span.Events[0].Name != "exception" || attrMap(span.Events[0].Attributes)["exception.type"] != "net.OpError" {
		t.Errorf("events = %+v", span.Events)
	}
	if len(span.Links) != 1 || len(span.Links[0].Attributes) != 0 || span.Links[0].SpanContext.TraceID() != ctx.TraceID() {
		t.Errorf("links = %+v, want the context kept and no attributes", span.Links)
	}
	r := attrMap(span.Resource.Attributes())
	if r["service.name"] != "acr" || r["api_token"] != telemetryscrub.Redacted || strings.Contains(r["note"], "abcdefghijklmnop") {
		t.Errorf("resource = %v", r)
	}
	if got[1].Name != "HTTP GET" {
		t.Errorf("a method with no route must name %q, got %q", "HTTP GET", got[1].Name)
	}
	if got[2].Name != "projection tick" {
		t.Errorf("the closed internal name changed: %q", got[2].Name)
	}
	if got[3].Name != "HTTP" {
		t.Errorf("an unknown method must name %q, got %q", "HTTP", got[3].Name)
	}
}

func TestSpanScrubLeavesACleanSpanUntouched(t *testing.T) {
	in := []attribute.KeyValue{attribute.String("http.request.method", "GET"), attribute.String("http.route", "/v1/things/{id}"), attribute.Int("http.response.status_code", 200), attribute.String("acr.tool", "investigate_question"), attribute.String("acr.result_class", "answered"), attribute.String("acr.query_version", "v1")}
	got := exportStubs(t, tracetest.SpanStub{Name: "GET /v1/things/{id}", SpanKind: trace.SpanKindServer, Attributes: in})[0]
	if got.Name != "GET /v1/things/{id}" || len(got.Attributes) != len(in) {
		t.Fatalf("clean span changed: %q %v", got.Name, got.Attributes)
	}
	for i := range in {
		if got.Attributes[i] != in[i] {
			t.Errorf("attribute %d changed: %v -> %v", i, in[i], got.Attributes[i])
		}
	}
}
