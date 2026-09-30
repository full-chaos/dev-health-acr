package api

import (
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// dataGraphQLEcho is the effective request: the effective max_bytes only.
// The query text and the variables are never echoed: acr sends its own
// rebuilt text (source.query_digest names it).
type dataGraphQLEcho struct {
	MaxBytes int `json:"max_bytes"`
}

// dataGraphQLResponse is graphql_query's wire answer.
type dataGraphQLResponse struct {
	directread.GraphQLResponse
	Request dataGraphQLEcho `json:"request"`
}

// contextFabricDataGraphQLHandler serves graphql_query (CHAOS-7075, S1b;
// design D.8 form iii, E.1). It runs after the protection chain (bearer,
// data:read, rate class Data, client version, entitlement), exactly as
// run_operation.
//
// Status codes (same rules as run_operation):
//
//   - 200 for every terminal typed result: served, refused (call "refused"
//     with refusal {code, reason, read_budget?}), operation_unavailable,
//     upstream_error, upstream_timeout.
//   - 400/413 invalid_request for a body that is not one JSON object of
//     this shape (unknown fields refused) or exceeds 16 KiB.
//   - 501 feature_not_enabled, reason data_graphql_not_configured, when
//     ACR_DATA_GRAPHQL_URL is unset or the root policy did not derive.
//   - 503 upstream_unavailable when no subject gate is composed or the
//     runner cannot decide.
//   - 500 internal_error on a gate proof contract defect.
func (a *App) contextFabricDataGraphQLHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runner := a.dataGraphQL()
		if runner == nil {
			writeContextFabricDataGraphQLNotConfigured(w, r)
			return
		}
		if !a.dataGateComposed() {
			a.writeContextFabricDataUnavailable(w, r, "graphql", "subject_gate_unavailable")
			return
		}
		var request directread.GraphQLRequest
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
				a.writeContextFabricDataInternal(w, r, "graphql")
			case errors.Is(err, directread.ErrGraphQLRunnerNotConfigured):
				writeContextFabricDataGraphQLNotConfigured(w, r)
			case errors.Is(err, directread.ErrOperationAuthorizationUnavailable):
				a.writeContextFabricDataUnavailable(w, r, "graphql", "authorization_unavailable")
			case errors.Is(err, directread.ErrOperationPrincipalInvalid):
				a.writeContextFabricDataInternal(w, r, "graphql")
			default:
				a.writeContextFabricDataUnavailable(w, r, "graphql", "graphql_runner_failed")
			}
			return
		}
		encoded, err := encodeBounded(dataGraphQLResponse{GraphQLResponse: response, Request: dataGraphQLEcho{MaxBytes: response.Page.MaxBytes}}, contextFabricDataResponseBytes)
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
			a.recordReadAudit(r.Context(), principal, "direct_graphql_denied", "context_fabric_data_graphql", "graphql_query", "denied", map[string]any{"refusal_code": code})
		default:
			a.recordReadAudit(r.Context(), principal, "direct_graphql_read", "context_fabric_data_graphql", "graphql_query", "success", map[string]any{"call": string(response.Call)})
		}
		writeEncodedJSON(w, http.StatusOK, encoded)
	}
}
