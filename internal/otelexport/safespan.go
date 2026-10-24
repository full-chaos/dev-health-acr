package otelexport

import "github.com/full-chaos/dev-health-acr/internal/telemetryscrub"

// safeSpanKeys are the only attribute keys a span may export: the three first-party span attributes and the
// closed request-span set. Exact keys only: a prefix would admit whatever a library adds under it.
var safeSpanKeys = map[string]bool{
	"acr.tool": true, "acr.result_class": true, "acr.query_version": true,
	"http.route": true, "http.request.method": true, "http.response.status_code": true,
	"url.scheme": true, "network.protocol.name": true, "network.protocol.version": true,
	"error.type": true, "server.address": true, "server.port": true,
}

// serverOnlySpanKeys name the service's own listener; on a client, internal or producer span the same keys would
// carry an outbound host, which is customer data.
var serverOnlySpanKeys = map[string]bool{"server.address": true, "server.port": true}

// neverSafeSpanKeys can carry a caller address, a raw path, a query, free text or a statement whatever their name
// looks like; none is ever safe (init panics otherwise).
var neverSafeSpanKeys = []string{
	"url.path", "url.query", "url.full", "http.url", "http.target",
	"client.address", "client.port", "network.peer.address", "network.peer.port",
	"user_agent.original", "enduser.id", "db.statement", "db.query.text",
	"exception.message", "exception.stacktrace",
}

// safeEventKeys are the only attributes an event keeps: the error type, never its text.
var safeEventKeys = map[string]bool{"exception.type": true}

// safeInternalSpanNames are the closed names of spans that are not HTTP requests.
var safeInternalSpanNames = map[string]bool{"projection tick": true}

func init() {
	for _, key := range neverSafeSpanKeys {
		if safeSpanKeys[key] || safeEventKeys[key] {
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
func SafeSpanKey(key string) bool { return safeSpanKeys[key] }
