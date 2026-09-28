package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// contextFabricDataFactsMaxRequestBytes bounds the read_facts request body:
// at most 8 kinds and 25 subjects fit far below this.
const contextFabricDataFactsMaxRequestBytes = 32 << 10

// directFacts returns the composed read_facts reader, or nil. Same nil
// discipline as investigator(): a.runtime may itself be nil.
func (a *App) directFacts() *directread.FactsReader {
	if a.runtime == nil || a.runtime.DirectFacts == nil {
		return nil
	}
	return a.runtime.DirectFacts
}

// contextFabricDataFactsHandler serves read_facts (CHAOS-7073). It runs only
// after authentication, scope, rate limit, client version and entitlement
// passed. The principal comes from authentication; the subject gate inside
// the reader decides, live, which requested subjects the caller may read.
//
// Errors: a malformed or out-of-bounds request is 400 invalid_request; a
// missing reader, a graph or registry failure is 503 upstream_unavailable
// (retryable). No raw dependency error text reaches the response.
func (a *App) contextFabricDataFactsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		reader := a.directFacts()
		if reader == nil {
			a.writeDirectFactsUnavailable(w, r)
			return
		}
		var request directread.FactsRequest
		if err := decodeJSONBody(w, r, contextFabricDataFactsMaxRequestBytes, &request); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The read_facts request is not valid JSON for its declared shape", false, map[string]any{"reason": directread.RefusalInvalidRequest})
			return
		}
		response, err := reader.Read(r.Context(), principal, request)
		if err != nil {
			var requestError *directread.RequestError
			if errors.As(err, &requestError) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "The read_facts request is invalid: "+contextfabric.SanitizeLogAttr(requestError.Detail), false, map[string]any{"reason": requestError.Reason})
				return
			}
			// Cancellation and deadline stay retryable unavailability; the
			// error text is never echoed.
			a.logger.WarnContext(r.Context(), "read_facts unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", directFactsFailureClass(err))
			a.writeDirectFactsUnavailable(w, r)
			return
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "The read_facts response could not be encoded", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "direct_facts_read", "context_fabric_facts", "read_facts", "success", map[string]any{"status": response.Status, "facts": len(response.Facts)})
		writeEncodedJSON(w, http.StatusOK, encoded)
	}
}

func (a *App) writeDirectFactsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Direct fact read is temporarily unavailable", true, map[string]any{"reason": "unavailable"})
}

func directFactsFailureClass(err error) string {
	if errors.Is(err, directread.ErrFactsUnavailable) {
		return "facts_unavailable"
	}
	return "facts_read"
}
