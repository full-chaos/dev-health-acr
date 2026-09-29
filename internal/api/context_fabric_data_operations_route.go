package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// dataOperationEcho is the effective request (design C.2 "Echo"): the
// operation as the catalogue knows it ("unknown" for any other name, so a
// client string is never echoed), the effective max_bytes, and -- only on a
// served answer, where every path passed the allowlist -- the client's own
// variables.
type dataOperationEcho struct {
	Operation string          `json:"operation"`
	MaxBytes  int             `json:"max_bytes"`
	Variables json.RawMessage `json:"variables,omitempty"`
}

// dataOperationResponse is run_operation's wire answer: the runner's
// response plus the echo.
type dataOperationResponse struct {
	directread.OperationResponse
	Request dataOperationEcho `json:"request"`
}

// contextFabricDataOperationsHandler serves run_operation (CHAOS-7072, S1a;
// design D.2, D.7, E.1). It runs after the protection chain (bearer,
// data:read, rate class Data, client version, entitlement).
//
// Status codes, decided from design D.2/D.7:
//
//   - 200 for every terminal typed result: served, refused (policy refusal:
//     call "refused" with refusal {code, reason}), operation_unavailable,
//     upstream_error, upstream_timeout. A refusal is an answer an MCP
//     client must read in the body, not a transport failure.
//   - 400/413 invalid_request for a body that is not one JSON object of
//     this shape (unknown fields refused) or exceeds 16 KiB.
//   - 501 feature_not_enabled, reason data_query_not_configured, when the
//     query service URL is not configured or the policy did not load.
//   - 503 upstream_unavailable when no subject gate is composed (no graph),
//     or the runner cannot decide (the gate or the grant listing failed).
//   - 500 internal_error when a gate proof was presented expired, spent or
//     to the wrong caller: a defect, never a subject refusal.
//
// The principal comes from the authenticated context only; the body names
// no organization and no caller. The runner authorizes every id through
// the subject gate for THIS request; this handler holds no proof.
func (a *App) contextFabricDataOperationsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runner := a.dataOperations()
		if runner == nil {
			writeContextFabricDataNotConfigured(w, r)
			return
		}
		if !a.dataGateComposed() {
			a.writeContextFabricDataUnavailable(w, r, "operations", "subject_gate_unavailable")
			return
		}
		var request directread.OperationRequest
		if err := decodeJSONBody(w, r, contextFabricDataRequestBytes, &request); err != nil {
			writeContextFabricDataDecodeError(w, r, err)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		response, err := runner.Run(r.Context(), principal, request)
		if err != nil {
			switch {
			case isDirectReadContractError(err):
				a.writeContextFabricDataInternal(w, r, "operations")
			case errors.Is(err, directread.ErrOperationRunnerNotConfigured):
				writeContextFabricDataNotConfigured(w, r)
			case errors.Is(err, directread.ErrOperationAuthorizationUnavailable):
				a.writeContextFabricDataUnavailable(w, r, "operations", "authorization_unavailable")
			case errors.Is(err, directread.ErrOperationPrincipalInvalid):
				a.writeContextFabricDataInternal(w, r, "operations")
			default:
				a.writeContextFabricDataUnavailable(w, r, "operations", "operation_runner_failed")
			}
			return
		}
		echo := dataOperationEcho{Operation: response.Operation, MaxBytes: response.Page.MaxBytes}
		if response.Call == directread.CallServed && len(request.Variables) > 0 {
			echo.Variables = request.Variables
		}
		encoded, err := encodeBounded(dataOperationResponse{OperationResponse: response, Request: echo}, contextFabricDataResponseBytes)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Direct data response exceeded service limits", false, nil)
			return
		}
		switch response.Call {
		case directread.CallRefused:
			code := ""
			if response.Refusal != nil {
				code = string(response.Refusal.Code)
			}
			a.recordReadAudit(r.Context(), principal, "direct_operation_denied", "context_fabric_data_operation", response.Operation, "denied", map[string]any{"refusal_code": code})
		default:
			a.recordReadAudit(r.Context(), principal, "direct_operation_read", "context_fabric_data_operation", response.Operation, "success", map[string]any{"call": string(response.Call)})
		}
		writeEncodedJSON(w, http.StatusOK, encoded)
	}
}
