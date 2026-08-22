package api

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (a *App) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if !a.allowDeviceRequest(w, r, a.runtime.DeviceAuthorizationLimiter.AllowDeviceCreation) {
		return
	}
	var request contractsv1.DeviceAuthorizationRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil || request.Validate() != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Device authorization request is invalid", false, nil)
		return
	}
	hints := auth.DeviceAuthorizationHints{}
	if request.OrganizationIDHint != nil {
		hints.OrganizationIDHint = *request.OrganizationIDHint
	}
	if request.RepositoryHints != nil {
		hints.RepositoryHints = *request.RepositoryHints
	}
	started, err := a.deviceFlow.Start(r.Context(), hints)
	if err != nil {
		a.writeDeviceDependencyError(w, r)
		return
	}
	response := contractsv1.DeviceAuthorizationResponse{
		SchemaVersion: contractsv1.DeviceAuthorizationResponseSchema,
		DeviceCode:    started.DeviceCode, UserCode: started.UserCode,
		VerificationURI: a.runtime.DeviceVerificationURL,
		ExpiresIn:       int(started.ExpiresIn / time.Second), Interval: int(started.Interval / time.Second),
	}
	writeJSON(w, http.StatusOK, response)
}

// handleDeviceToken dispatches POST /api/v1/oauth/token by Content-Type:
// the pre-existing JSON device-code grant (handleDeviceCodeToken) stays
// untouched, and RFC 8693 workload token exchange (CHAOS-4013) --
// form-encoded, grant_type=urn:ietf:params:oauth:grant-type:token-exchange
// -- is handled by handleTokenExchange. Both share the same
// AllowTokenRequest rate-limit budget for this endpoint.
//
// This is where the two grants' runtime-dependency gates deliberately
// PART WAYS (CHAOS-4071). The route itself carries no
// deviceRuntimeHandler wrapper -- unlike device_authorization and
// device_approval, which stay wrapped -- because that wrapper fails
// closed on a.authenticator.WebAssertions() == nil, a dependency of the
// human/browser device-login flow with nothing to do with RFC 8693
// machine token exchange. handleTokenExchange already carries its OWN
// correct nil check (a.runtime == nil || a.runtime.WorkloadTokenExchange
// == nil), so the form-encoded branch below needs nothing added here.
// The JSON device-code branch, however, still needs the exact
// a.runtime/a.authenticator/WebAssertions() fail-closed check
// deviceRuntimeHandler used to apply at the route: inlined here, gating
// only the dispatch into handleDeviceCodeToken, so a deployment without
// web-assertion JWKS configured keeps rejecting the device-code grant
// exactly as before while the token-exchange grant is no longer
// collaterally gated on it.
func (a *App) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mediaType == "application/x-www-form-urlencoded" {
		a.handleTokenExchange(w, r)
		return
	}
	if a.runtime == nil || a.authenticator == nil || a.authenticator.WebAssertions() == nil {
		a.handleRuntimeUnavailable(w, r)
		return
	}
	a.handleDeviceCodeToken(w, r)
}

func (a *App) handleDeviceCodeToken(w http.ResponseWriter, r *http.Request) {
	if !a.allowDeviceRequest(w, r, a.runtime.DeviceAuthorizationLimiter.AllowTokenRequest) {
		return
	}
	var request contractsv1.DeviceTokenRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil || request.Validate() != nil {
		a.writeOAuthDeviceError(w, contractsv1.OAuthDeviceErrorInvalidGrant, 0)
		return
	}
	issued, err := a.deviceFlow.Poll(r.Context(), request.DeviceCode)
	if err != nil {
		var pollError *auth.DevicePollError
		if errors.As(err, &pollError) {
			a.writeOAuthDeviceError(w, contractsv1.OAuthDeviceErrorCode(pollError.Kind), pollError.RetryAfter)
			return
		}
		a.writeDeviceDependencyError(w, r)
		return
	}
	writeJSON(w, http.StatusOK, contractsv1.DeviceTokenResponse{
		SchemaVersion: contractsv1.DeviceTokenResponseSchema, AccessToken: issued.Token, TokenType: "Bearer",
		ExpiresIn: int(auth.DeviceCredentialLifetime / time.Second), Credential: issued.Credential,
	})
}

func (a *App) handleDeviceApproval(w http.ResponseWriter, r *http.Request) {
	var request contractsv1.DeviceApprovalRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Device approval request is invalid", false, nil)
		return
	}
	if !a.allowDeviceApproval(w, r, storage.HashUserCode(request.UserCode)) {
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	if request.SchemaVersion == contractsv1.DeviceApprovalPreviewRequestSchema {
		if request.RepositoryScopes != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Device approval request is invalid", false, nil)
			return
		}
		previewRequest := contractsv1.DeviceApprovalPreviewRequest{SchemaVersion: request.SchemaVersion, UserCode: request.UserCode}
		if err := previewRequest.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "Device approval request is invalid", false, nil)
			return
		}
		preview, err := a.deviceFlow.Preview(r.Context(), auth.DeviceApprovalPreviewRequest{Principal: principal, UserCode: previewRequest.UserCode})
		if err != nil {
			a.writeDeviceApprovalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, contractsv1.DeviceApprovalPreviewResponse{
			SchemaVersion:      contractsv1.DeviceApprovalPreviewResponseSchema,
			OrganizationIDHint: preview.OrganizationIDHint,
			RepositoryHints:    preview.RepositoryHints,
		})
		return
	}
	if err := request.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Device approval request is invalid", false, nil)
		return
	}
	if _, err := a.deviceFlow.Approve(r.Context(), auth.DeviceApprovalRequest{
		Principal: principal, UserCode: request.UserCode, RepositoryScopes: request.RepositoryScopes,
	}); err != nil {
		a.writeDeviceApprovalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contractsv1.DeviceApprovalResponse{SchemaVersion: contractsv1.DeviceApprovalResponseSchema, Status: "approved"})
}

func (a *App) writeDeviceApprovalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storage.ErrDeviceAuthorizationConflict) {
		writeError(w, r, http.StatusConflict, "device_authorization_conflict", "Device authorization is no longer pending", false, nil)
		return
	}
	if errors.Is(err, auth.ErrInvalidDeviceFlow) || errors.Is(err, storage.ErrDeviceAuthorizationNotFound) || errors.Is(err, storage.ErrDeviceAuthorizationExpired) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Device approval request is invalid", false, nil)
		return
	}
	a.writeDeviceDependencyError(w, r)
}

func (a *App) handleRotateSelfCredential(w http.ResponseWriter, r *http.Request) {
	var request contractsv1.CredentialRotateRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil || request.Validate() != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Credential rotation request is invalid", false, nil)
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	rotation, err := a.credentialService.RotateSelf(r.Context(), principal)
	if err != nil {
		a.writeSelfLifecycleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contractsv1.CredentialRotateResponse{
		SchemaVersion: contractsv1.CredentialRotateResponseSchema, AccessToken: rotation.Issued.Token, Credential: rotation.Issued.Credential,
		Receipt: contractsv1.CredentialRotationReceipt{SourceCredentialID: rotation.Receipt.SourceCredentialID, ReplacementCredentialID: rotation.Receipt.SuccessorCredentialID, RollbackUntil: rotation.Receipt.RollbackUntil},
	})
}

func (a *App) handleRevokeSelfCredential(w http.ResponseWriter, r *http.Request) {
	var request contractsv1.CredentialRevokeRequest
	if err := decodeJSONBody(w, r, a.config.MaxRequestBodyBytes, &request); err != nil || request.Validate() != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Credential revocation request is invalid", false, nil)
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		return
	}
	var revoked auth.SelfRevocation
	var err error
	if request.RollbackReceipt == nil {
		revoked, err = a.credentialService.RevokeSelf(r.Context(), principal)
	} else {
		receipt := auth.SelfRotationReceipt{
			SourceCredentialID:    request.RollbackReceipt.SourceCredentialID,
			SuccessorCredentialID: request.RollbackReceipt.ReplacementCredentialID,
			RollbackUntil:         request.RollbackReceipt.RollbackUntil,
		}
		revoked, err = a.credentialService.RollbackSelfRotation(r.Context(), principal, receipt)
	}
	if err != nil {
		a.writeSelfLifecycleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, contractsv1.CredentialRevokeResponse{SchemaVersion: contractsv1.CredentialRevokeResponseSchema, Credential: revoked.Credential})
}

func (a *App) deviceApprovalHandler(next http.Handler) http.Handler {
	if a.runtime == nil || a.authenticator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	protected := a.authenticator.RequireScope(auth.WebAssertionPermissionCredentialIssue, next)
	protected = a.authenticator.MiddlewareFor(true, protected)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values(auth.WebAssertionHeader)) != 1 || len(r.Header.Values("Authorization")) != 0 {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func (a *App) selfLifecycleHandler(next http.Handler) http.Handler {
	if a.runtime == nil || a.authenticator == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	return a.authenticator.Middleware(a.ProtectedHandler(limits.RequestClassAuth, next))
}

func (a *App) deviceRuntimeHandler(next http.Handler) http.Handler {
	if a.runtime == nil || a.authenticator == nil || a.authenticator.WebAssertions() == nil {
		return http.HandlerFunc(a.handleRuntimeUnavailable)
	}
	return next
}

func (a *App) allowDeviceRequest(w http.ResponseWriter, r *http.Request, allow func(string) DeviceAuthorizationLimitDecision) bool {
	decision := allow(a.clientIP(r))
	if decision.Allowed {
		return true
	}
	a.writeDeviceRateLimit(w, r, decision.RetryAfter)
	return false
}

func (a *App) allowDeviceApproval(w http.ResponseWriter, r *http.Request, userCode storage.UserCodeHash) bool {
	decision := a.runtime.DeviceAuthorizationLimiter.AllowApprovalAttempt(a.clientIP(r), userCode)
	if decision.Allowed {
		return true
	}
	a.writeDeviceRateLimit(w, r, decision.RetryAfter)
	return false
}

func (a *App) writeDeviceRateLimit(w http.ResponseWriter, r *http.Request, retryAfter time.Duration) {
	seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many device authorization requests", true, map[string]any{"retry_after_seconds": seconds})
}

func (a *App) writeOAuthDeviceError(w http.ResponseWriter, code contractsv1.OAuthDeviceErrorCode, retryAfter time.Duration) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int((retryAfter+time.Second-1)/time.Second))))
	}
	writeJSON(w, http.StatusBadRequest, contractsv1.OAuthDeviceErrorResponse{SchemaVersion: contractsv1.OAuthDeviceErrorSchema, Error: code})
}

func (a *App) writeDeviceDependencyError(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Device authorization service is temporarily unavailable", true, nil)
}

func (a *App) writeSelfLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrInvalidCredential) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Credential lifecycle request is invalid", false, nil)
		return
	}
	if errors.Is(err, auth.ErrStaleSelfCredential) || errors.Is(err, storage.ErrConflict) {
		writeError(w, r, http.StatusConflict, "credential_lifecycle_conflict", "Credential lifecycle operation conflicts with current state", false, nil)
		return
	}
	writeError(w, r, http.StatusServiceUnavailable, "upstream_unavailable", "Credential lifecycle service is temporarily unavailable", true, nil)
}
