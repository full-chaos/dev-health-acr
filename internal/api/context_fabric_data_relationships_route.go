package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// contextFabricDataRelationshipsMaxRequestBytes bounds the read_relationships
// request body: a subject, at most 12 types and a cursor fit far below it.
const contextFabricDataRelationshipsMaxRequestBytes = 16 << 10

// directRelationships returns the composed read_relationships reader, or nil
// (the route then fails closed).
func (a *App) directRelationships() *directread.RelationshipsReader {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.DirectRelationships
}

// contextFabricDataRelationshipsHandler serves read_relationships
// (CHAOS-7074). It runs only after authentication, scope, rate limit, client
// version, entitlement and data-store readiness passed. The principal comes
// from authentication; the reader takes a fresh subject-gate decision on the
// root and on every end node of every page, and applies the edge gate.
//
// Errors: a malformed request or a refused cursor is 400 invalid_request
// (details.reason names which); a missing reader, a gate or graph failure is
// 503 upstream_unavailable (retryable); a gate proof the reader could not
// present is 500. No raw dependency error text reaches the response.
func (a *App) contextFabricDataRelationshipsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		reader := a.directRelationships()
		if reader == nil {
			a.writeDirectRelationshipsUnavailable(w, r)
			return
		}
		var request directread.RelationshipsRequest
		if err := decodeJSONBody(w, r, contextFabricDataRelationshipsMaxRequestBytes, &request); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The read_relationships request is not valid JSON for its declared shape", false, map[string]any{"reason": directread.RelationshipsRefusalInvalidRequest})
			return
		}
		response, err := reader.Read(r.Context(), principal, request)
		if err != nil {
			var requestError *directread.RelationshipsRequestError
			if errors.As(err, &requestError) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "The read_relationships request is invalid: "+contextfabric.SanitizeLogAttr(requestError.Detail), false, map[string]any{"reason": requestError.Reason})
				return
			}
			if errors.Is(err, directread.ErrRelationshipsInternal) {
				a.logger.ErrorContext(r.Context(), "read_relationships internal error", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "gate_decision_refused")
				writeError(w, r, http.StatusInternalServerError, "internal_error", "The read_relationships request failed", false, nil)
				return
			}
			a.logger.WarnContext(r.Context(), "read_relationships unavailable", "request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())), "failure_class", "relationships_unavailable")
			a.writeDirectRelationshipsUnavailable(w, r)
			return
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "The read_relationships response could not be encoded", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "direct_relationships_read", "context_fabric_relationships", directread.RelationshipsTool, "success", map[string]any{
			"status": string(response.Status), "edges": len(response.Edges), "edges_not_visible": response.Withheld.EdgesNotVisible,
		})
		writeEncodedJSON(w, http.StatusOK, encoded)
	}
}

func (a *App) writeDirectRelationshipsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Direct relationship read is temporarily unavailable", true, map[string]any{"reason": "unavailable"})
}
