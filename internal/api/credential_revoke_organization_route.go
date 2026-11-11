package api

import (
	"net/http"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/logsanitize"
)

// CredentialRevokeOrganizationPath revokes every credential of the caller's
// organization, for the Auth Control Plane when it disables or deletes it.
const CredentialRevokeOrganizationPath = "/internal/v1/organization/credentials/revoke"

// credentialRevokeOrganizationHandler is the organization revoke's chain, the
// on-behalf revoke's with the organization revoke scope: the authenticator
// refuses an absent or malformed bearer and any web assertion before the
// credential lookup and admits a disabled organization (a gone one is 401);
// then the credential's class must be workload and its scope
// credential_lifecycle:revoke_organization, which only the revoke binding role
// carries.
func (a *App) credentialRevokeOrganizationHandler() http.Handler {
	if a.runtime == nil || a.authenticator == nil || a.credentialService == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	return a.authenticator.MiddlewareWithOrganizationCheck(auth.OrganizationCheckAdmitDisabled,
		a.requireWorkloadClass(
			a.authenticator.RequireScope(auth.ScopeCredentialLifecycleRevokeOrganization,
				LimitMiddleware(a.limits, limits.RequestClassAuth, http.HandlerFunc(a.handleCredentialRevokeOrganization)))))
}

func (a *App) handleCredentialRevokeOrganization(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || principal.OrgID == "" {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	var request contractsv1.CredentialRevokeOrganizationRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil || request.Validate() != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Organization credential revocation request is invalid", false, invalidBodyDetails(err))
		return
	}
	// A workload principal's subject is its binding id (the authenticator sets
	// it from the credential row), so the caller's own exchanges are known.
	binding := ""
	if principal.Subject != principal.CredentialID {
		binding = principal.Subject
	}
	batch := a.organizationRevokeBatch
	if batch <= 0 {
		batch = auth.OrganizationRevokeBatch
	}
	result, err := a.credentialService.RevokeOrganization(r.Context(), principal.OrgID, binding, principal.CredentialID, request.ActorID, request.Reason, batch)
	if err != nil {
		a.logger.ErrorContext(r.Context(), "credential organization revoke failed",
			"request_id", logsanitize.SanitizeLogAttr(RequestID(r.Context())), "reason", request.Reason,
			"revoked", result.Revoked, "failure_class", "credential_store")
		writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Credential service is temporarily unavailable", true, nil)
		return
	}
	a.logger.InfoContext(r.Context(), "credential organization revoke answered",
		"request_id", logsanitize.SanitizeLogAttr(RequestID(r.Context())), "reason", request.Reason,
		"revoked", result.Revoked, "caller_revoked", result.CallerRevoked, "more", result.More)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, contractsv1.CredentialRevokeOrganizationResponse{
		SchemaVersion: contractsv1.CredentialRevokeOrganizationResponseSchema,
		Revoked:       result.Revoked, CallerRevoked: result.CallerRevoked, More: result.More,
	})
}
