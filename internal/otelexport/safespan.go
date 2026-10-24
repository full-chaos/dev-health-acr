package otelexport

import (
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/full-chaos/dev-health-acr/internal/telemetryscrub"
)

// httpSafeKeys are the attribute keys the HTTP server telemetry may export, spans and metrics alike, each with the one value
// type it may carry. One list serves the span allow-list below and the allow-list view on the otelhttp metrics, so the two
// cannot drift apart: the request host and port (server.address, server.port) are chosen by the caller and are in neither.
var httpSafeKeys = map[string]attribute.Type{
	"http.route": attribute.STRING, "http.request.method": attribute.STRING, "http.response.status_code": attribute.INT64,
	"url.scheme": attribute.STRING, "network.protocol.name": attribute.STRING, "network.protocol.version": attribute.STRING,
}

// safeSpanKeys are the only attribute keys a span may export, each with the one value type it may carry: the three
// first-party span attributes, the error type and the closed request set. Exact keys only (a prefix would admit whatever a
// library adds under it), and a value of another type is dropped with its attribute (a nested map or slice could hide
// unapproved keys and text under an approved outer key).
var safeSpanKeys = func() map[string]attribute.Type {
	keys := map[string]attribute.Type{
		"acr.tool": attribute.STRING, "acr.result_class": attribute.STRING, "acr.query_version": attribute.STRING,
		"error.type": attribute.STRING,
	}
	for key, typ := range httpSafeKeys {
		keys[key] = typ
	}
	return keys
}()

// neverSafeSpanKeys can carry a caller address, a raw path, a query, free text or a statement whatever their name
// looks like; none is ever safe (init panics otherwise).
var neverSafeSpanKeys = []string{
	"url.path", "url.query", "url.full", "http.url", "http.target", "server.address", "server.port",
	"client.address", "client.port", "network.peer.address", "network.peer.port",
	"user_agent.original", "enduser.id", "db.statement", "db.query.text",
	"exception.message", "exception.stacktrace",
}

// safeEventKeys are the only attributes an event keeps (as a string): the error type, never its text.
var safeEventKeys = map[string]bool{"exception.type": true}

// safeInternalSpanNames are the closed names of spans that are not HTTP requests.
var safeInternalSpanNames = map[string]bool{"projection tick": true}

func init() {
	for _, key := range neverSafeSpanKeys {
		if _, safe := safeSpanKeys[key]; safe || safeEventKeys[key] {
			panic("otelexport: " + key + " must never be a safe span key")
		}
	}
	for key := range safeSpanKeys {
		if protected, network := telemetryscrub.KeyClass(key); protected || network {
			panic("otelexport: safe span key " + key + " is a protected or network key")
		}
	}
}

// SafeSpanKey reports whether key may leave the process as a span attribute.
func SafeSpanKey(key string) bool { _, ok := safeSpanKeys[key]; return ok }

// httpMetricView filters the attributes of every instrument of the otelhttp scope to httpSafeKeys. It is an allow-list: the
// library takes the server address and port from the request's Host header, and a later release may add another caller
// controlled attribute (a client address, a user agent, a raw path); both are dropped here, not listed one by one.
func httpMetricView() sdkmetric.View {
	keys := make([]attribute.Key, 0, len(httpSafeKeys))
	for key := range httpSafeKeys {
		keys = append(keys, attribute.Key(key))
	}
	return sdkmetric.NewView(
		sdkmetric.Instrument{Scope: instrumentation.Scope{Name: otelhttp.ScopeName}},
		sdkmetric.Stream{AttributeFilter: attribute.NewAllowKeysFilter(keys...)},
	)
}
