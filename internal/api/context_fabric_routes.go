package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

// ContextFabricInvestigationsPath is reserved for the consumer-neutral ACR
// investigation endpoint. The scaffold deliberately does not register it in
// App.Handler until Reset 0 publishes the matching Go/OpenAPI/JSON-Schema
// contract bundle.
const ContextFabricInvestigationsPath = "/api/v1/context-fabric/investigations"

// ContextFabricInvestigationHandler returns the fully protected endpoint seam
// for the Reset 1 engine. Hosting composition supplies the investigator; API
// code does not choose a graph backend or canonical fact adapter.
func (a *App) ContextFabricInvestigationHandler(investigator contextfabric.Investigator) http.Handler {
	if investigator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request contextfabric.InvestigationRequest
		if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil {
			status := http.StatusBadRequest
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			writeError(w, r, status, "invalid_request", "Context Fabric investigation request is invalid", false, nil)
			return
		}
		request.RequestID = RequestID(r.Context())
		if err := request.Validate(); err != nil || request.Options.MaxSerializedBytes > a.config.MaxSerializedBytes {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Context Fabric investigation request is invalid", false, nil)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		result, err := investigator.Investigate(r.Context(), principal, request)
		if err != nil {
			a.writeContextFabricError(w, r, err)
			return
		}
		maximumBytes := min(int64(a.config.MaxSerializedBytes), int64(request.Options.MaxSerializedBytes))
		encoded, err := encodeBounded(result, maximumBytes)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation response exceeded service limits", false, nil)
			return
		}
		usage := limits.ResourceUsage{
			Items:  int64(contextFabricResultItems(result)),
			Tokens: int64((len(encoded) + 3) / 4),
			Bytes:  int64(len(encoded)),
		}
		if err := CompleteUsage(r.Context(), usage); err != nil {
			writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Context Fabric investigation response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "context_fabric_investigation_completed", "context_fabric_investigation", result.ResultID, "success", map[string]any{"investigation_status": result.Status})
		writeEncodedJSON(w, http.StatusOK, encoded)
	})
	return a.protectedRuntimeHandler(limits.RequestClassContext, auth.ScopeContextRead, true, true, handler)
}

func (a *App) writeContextFabricError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		writeError(w, r, http.StatusGatewayTimeout, "upstream_unavailable", "The Context Fabric investigation timed out", true, nil)
		return
	}
	if errors.Is(err, contextfabric.ErrUnavailable) {
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Context Fabric is temporarily unavailable", true, nil)
		return
	}
	a.logger.ErrorContext(r.Context(), "context fabric investigation failed", "request_id", RequestID(r.Context()), "failure_class", "context_fabric_investigation")
	writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation failed", false, nil)
}

func contextFabricResultItems(result contextfabric.InvestigationResult) int {
	items := len(result.SubjectResolution.Candidates) + len(result.Drivers) + len(result.Paths) + len(result.RemainingWork) + len(result.ReadinessGaps) + len(result.Conflicts)
	if result.Cohort != nil {
		items += len(result.Cohort.Members)
	}
	return items
}
