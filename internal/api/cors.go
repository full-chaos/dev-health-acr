package api

import (
	"net/http"
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
	corsAllowHeaders  = "Authorization, Content-Type, X-ACR-Client-Version, X-ACR-Submission-ID, X-ACR-Thread-ID, X-ACR-Wait-Budget-Seconds, X-ACR-Resource, X-Request-ID"
	corsExposeHeaders = "Retry-After, X-Request-ID"
	corsMaxAgeSeconds = "600"
)

// CORSRefusedLogMessage is the line for a request on a browser route from an
// origin that is not on the allow list.
const CORSRefusedLogMessage = "cors origin refused"

// corsRouteClass is the closed name of a browser route ("" for any other
// path).
func corsRouteClass(path string) string {
	switch {
	case path == OAuthAuthorizationServerMetadataPath:
		return "oauth_metadata"
	case path == OAuthTokenPath:
		return "oauth_token"
	case path == ContextFabricInvestigationsPath || strings.HasPrefix(path, ContextFabricInvestigationsPath+"/"):
		return "investigations"
	case strings.HasPrefix(path, corsEvidencePrefix):
		return "evidence"
	}
	return ""
}

// corsEvidencePrefix is the evidence read route's fixed prefix.
const corsEvidencePrefix = "/api/v1/agent-context/evidence/"

// CORSRouteClassVocabulary is the closed set of browser route names.
func CORSRouteClassVocabulary() [4]string {
	return [4]string{"oauth_metadata", "oauth_token", "investigations", "evidence"}
}

func (a *App) corsMiddleware(next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(a.config.CORSAllowedOrigins))
	for _, origin := range a.config.CORSAllowedOrigins {
		allowed[origin] = struct{}{}
	}
	if len(allowed) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := corsRouteClass(r.URL.Path)
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
		origin := origins[0]
		if _, ok := allowed[origin]; !ok || len(origins) != 1 {
			a.logger.InfoContext(r.Context(), CORSRefusedLogMessage,
				"route", route,
				"preflight", preflight,
				"origin", contextfabric.SanitizeLogAttr(truncateForLog(origin, 200)),
				"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
			)
			next.ServeHTTP(w, r)
			return
		}
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

func truncateForLog(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
