package api

import (
	"errors"
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CredentialRevokeOnBehalfPath revokes an MCP-bound credential on behalf of a
// user or admin the Auth Control Plane authorized.
const CredentialRevokeOnBehalfPath = "/internal/v1/credentials/{credential_id}/revoke"

// credentialRevokeOnBehalfHandler is the on-behalf revoke's chain, the
// listing's with the revoke scope: the authenticator refuses an absent or
// malformed bearer and any web assertion before the credential lookup; then
// the credential's class must be workload and its scope
// credential_lifecycle:revoke, which only the revoke binding role carries.
func (a *App) credentialRevokeOnBehalfHandler() http.Handler {
	if a.runtime == nil || a.authenticator == nil || a.credentialService == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	return a.credentialLifecycleLine(oauthvocab.StepCredentialRevoke,
		a.authenticator.MiddlewareFor(false,
			a.requireWorkloadClass(
				a.authenticator.RequireScope(auth.ScopeCredentialLifecycleRevoke,
					LimitMiddleware(a.limits, limits.RequestClassAuth, http.HandlerFunc(a.handleCredentialRevokeOnBehalf))))))
}

func (a *App) handleCredentialRevokeOnBehalf(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || principal.OrgID == "" {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	var request contractsv1.CredentialRevokeOnBehalfRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil || request.Validate() != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Credential revocation request is invalid", false, invalidBodyDetails(err))
		return
	}
	_, err := a.credentialService.RevokeOnBehalf(r.Context(), principal.OrgID, r.PathValue("credential_id"), request.ActorType, request.ActorID)
	var refusal *auth.OnBehalfRefusal
	switch {
	case err == nil:
		a.emitCredentialRevokeOnBehalf(r, request.ActorType, eventspec.CredentialRevokeOnBehalfDecisionRevoked, eventspec.CredentialRevokeOnBehalfReasonNone)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &refusal):
		a.emitCredentialRevokeOnBehalf(r, request.ActorType, eventspec.CredentialRevokeOnBehalfDecisionRefused, refusal.Reason)
		writeError(w, r, http.StatusNotFound, "not_found", "Credential not found", false, nil)
	case errors.Is(err, storage.ErrConflict):
		a.emitCredentialRevokeOnBehalf(r, request.ActorType, eventspec.CredentialRevokeOnBehalfDecisionConflict, eventspec.CredentialRevokeOnBehalfReasonNone)
		a.writeSelfLifecycleError(w, r, err)
	default:
		a.writeSelfLifecycleError(w, r, err)
	}
}

func (a *App) emitCredentialRevokeOnBehalf(r *http.Request, actorType, decision, reason string) {
	fields := eventspec.NewCredentialRevokeOnBehalfFields(RequestID(r.Context()), actorType, decision, reason)
	a.logger.InfoContext(r.Context(), eventspec.CredentialRevokeOnBehalfLogMessage, fields.SlogArgs()...)
}
