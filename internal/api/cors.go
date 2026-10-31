package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CORS for the Ask Dev web origin (CHAOS-12086; Ask Dev direct-browser
// design §5, T6). The browser calls acr-api directly with an ACR OAuth
// bearer, so the routes it calls answer CORS for the exact origins in
// AppConfig.CORSAllowedOrigins (ACR_CORS_ALLOWED_ORIGINS) and for nobody
// else:
//
//   - only the browser routes below; never /authorize (a top-level
//     navigation) or /authorize/consent (server to server), and no other API
//     route;
//   - only a listed origin, compared byte for byte, echoed back -- never
//     "*", never Access-Control-Allow-Credentials (ACR reads no cookie: auth
//     is the bearer);
//   - a preflight is answered here and never reaches a route's auth; an
//     actual response of any status carries the headers, because this
//     middleware runs before the route writes anything.
//
// A request with no Origin (a non-browser client) is untouched. An origin
// that is not listed gets no CORS header and its request runs unchanged, so
// the browser refuses the response; the refusal is logged.

const (
	corsAllowMethods  = "GET, POST, OPTIONS"
	corsAllowHeaders  = "Authorization, Content-Type, X-ACR-Client-Version, X-ACR-Submission-ID, X-ACR-Thread-ID, X-ACR-Wait-Budget-Seconds, X-ACR-Resource, X-Request-ID, Last-Event-ID"
	corsExposeHeaders = "Retry-After, X-Request-ID"
	corsMaxAgeSeconds = "600"
)

// CORSRefusedLogMessage is the line for a request on a browser route from an
// origin that is not on the allow list.
const CORSRefusedLogMessage = "cors origin refused"

// corsDecision carries this middleware's decision out to the access log, so
// an allowed CORS grant is visible at Info as well as a refusal (codex #1770
// r1 P3): the access line of a browser-route request with an Origin carries
// cors (allowed or refused), cors_route and cors_preflight, and an allowed
// one carries cors_origin, the listed origin it echoed.
type corsDecision struct {
	outcome   string
	route     string
	preflight bool
	origin    string
}

type corsDecisionContextKey struct{}

func withCORSDecision(r *http.Request) (*http.Request, *corsDecision) {
	decision := &corsDecision{}
	return r.WithContext(context.WithValue(r.Context(), corsDecisionContextKey{}, decision)), decision
}

func noteCORSDecision(r *http.Request, decision corsDecision) {
	if holder, ok := r.Context().Value(corsDecisionContextKey{}).(*corsDecision); ok {
		*holder = decision
	}
}

func (d *corsDecision) logFields() []any {
	if d == nil || d.outcome == "" {
		return nil
	}
	fields := []any{"cors", d.outcome, "cors_route", d.route, "cors_preflight", d.preflight}
	if d.origin != "" {
		fields = append(fields, "cors_origin", contextfabric.SanitizeLogAttr(d.origin))
	}
	return fields
}

// corsBrowserRoutes are the browser routes as their registered mux patterns
// (app.go), with their closed class names. A path is a browser route only
// when it matches one of these patterns exactly, never because it sits under
// one of their prefixes (CHAOS-12125): a nested or empty-segment path that no
// route serves answers no CORS.
var corsBrowserRoutes = [...]struct{ pattern, class string }{
	{OAuthAuthorizationServerMetadataPath, "oauth_metadata"},
	{OAuthTokenPath, "oauth_token"},
	// CHAOS-12124: sign-out revokes the family from the browser; it must
	// read the answer.
	{OAuthRevocationPath, "oauth_revoke"},
	{ContextFabricInvestigationsPath, "investigations"},
	{ContextFabricInvestigationResultPath, "investigations"},
	{ContextFabricSubmissionStatusPath, "investigations"},
	// CHAOS-11949: attach (SSE, Last-Event-ID) and cancel of a leased run.
	{ContextFabricSubmissionEventsPath, "investigations"},
	{ContextFabricSubmissionCancelPath, "investigations"},
	{AgentContextEvidencePath, "evidence"},
}

// corsRouteClass is the closed name of a browser route ("" for any other
// path). path is the request's escaped path (URL.EscapedPath), so an escaped
// "/" inside an identifier (an evidence reference can hold one) stays inside
// its segment, as the mux reads it.
func corsRouteClass(path string) string {
	for _, route := range corsBrowserRoutes {
		if corsPatternMatches(route.pattern, path) {
			return route.class
		}
	}
	return ""
}

// corsPatternMatches reports whether an escaped path matches a mux pattern's
// path the way the mux matches it: the escaped path splits on "/", each
// segment is unescaped, a literal segment matches itself, and a {name}
// wildcard matches exactly one non-empty segment.
func corsPatternMatches(pattern, escapedPath string) bool {
	want, got := strings.Split(pattern, "/"), strings.Split(escapedPath, "/")
	if len(want) != len(got) {
		return false
	}
	for i, segment := range want {
		value, err := url.PathUnescape(got[i])
		if err != nil {
			return false
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if value == "" {
				return false
			}
			continue
		}
		if segment != value {
			return false
		}
	}
	return true
}

// CORSRouteClassVocabulary is the closed set of browser route names.
func CORSRouteClassVocabulary() [5]string {
	return [5]string{"oauth_metadata", "oauth_token", "oauth_revoke", "investigations", "evidence"}
}

func (a *App) corsMiddleware(next http.Handler) http.Handler {
	// allowed maps a listed origin to itself, so every header and log value
	// below is the configured string, never the request's. listedHosts holds
	// the listed hosts, for the refusal line's near_miss.
	allowed := make(map[string]string, len(a.config.CORSAllowedOrigins))
	listedHosts := make(map[string]struct{}, len(a.config.CORSAllowedOrigins))
	for _, origin := range a.config.CORSAllowedOrigins {
		allowed[origin] = origin
		if parsed, err := url.Parse(origin); err == nil {
			listedHosts[parsed.Hostname()] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := corsRouteClass(r.URL.EscapedPath())
		if route == "" {
			next.ServeHTTP(w, r)
			return
		}
		header := w.Header()
		// The answer depends on Origin whether or not it is listed, so a
		// shared cache must key on it.
		header.Add("Vary", "Origin")
		origins := r.Header.Values("Origin")
		if len(origins) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
		origin, ok := allowed[origins[0]]
		if !ok || len(origins) != 1 {
			// The refused Origin itself is never logged (CodeQL
			// go/clear-text-logging): reason and near_miss are derived by
			// comparison, so no request header string reaches the log.
			reason := "not_listed"
			if len(origins) != 1 {
				reason = "multiple_origins"
			}
			noteCORSDecision(r, corsDecision{outcome: "refused", route: route, preflight: preflight})
			a.logger.InfoContext(r.Context(), CORSRefusedLogMessage,
				"route", route,
				"preflight", preflight,
				"reason", reason,
				"near_miss", corsNearMiss(listedHosts, origins),
				"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
			)
			next.ServeHTTP(w, r)
			return
		}
		noteCORSDecision(r, corsDecision{outcome: "allowed", route: route, preflight: preflight, origin: origin})
		header.Set("Access-Control-Allow-Origin", origin)
		if preflight {
			header.Add("Vary", "Access-Control-Request-Method")
			header.Add("Vary", "Access-Control-Request-Headers")
			header.Set("Access-Control-Allow-Methods", corsAllowMethods)
			header.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			header.Set("Access-Control-Max-Age", corsMaxAgeSeconds)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		header.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		next.ServeHTTP(w, r)
	})
}

// corsNearMiss reports whether a refused Origin names a listed host under
// another scheme, port or letter case: the usual sign of a misspelled allow
// list entry rather than a foreign site.
func corsNearMiss(listedHosts map[string]struct{}, origins []string) bool {
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil {
			continue
		}
		if _, ok := listedHosts[strings.ToLower(parsed.Hostname())]; ok {
			return true
		}
	}
	return false
}
