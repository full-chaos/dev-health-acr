package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// WorkloadTokenExchanger is the interface api.RuntimeDependencies.
// WorkloadTokenExchange satisfies -- auth.WorkloadTokenExchangeService in
// production, a fake in tests.
type WorkloadTokenExchanger interface {
	Exchange(ctx context.Context, subjectToken string, requestedScope []string) (auth.WorkloadTokenExchangeResult, error)
}

// handleTokenExchange implements the RFC 8693 token-exchange grant on the
// shared POST /api/v1/oauth/token endpoint (CHAOS-4013), reached from
// handleDeviceToken's Content-Type dispatch. Nil
// RuntimeDependencies.WorkloadTokenExchange (the deployment default until
// an operator configures the Kubernetes TokenReview integration) degrades
// to a clean 503, the same convention every other optional dependency in
// this package uses (see e.g. (*App).investigator).
func (a *App) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	if a.runtime == nil || a.runtime.WorkloadTokenExchange == nil {
		a.handleRuntimeUnavailable(w, r)
		return
	}
	if !a.allowDeviceRequest(w, r, a.runtime.DeviceAuthorizationLimiter.AllowTokenRequest) {
		return
	}
	// ParseForm buffers the whole body in memory before Validate ever
	// looks at it; an unauthenticated caller must not be able to force an
	// unbounded read, so this bound applies BEFORE parsing, the same
	// ceiling decodeJSONBody already applies to the JSON grant on this
	// same endpoint.
	r.Body = http.MaxBytesReader(w, r.Body, a.config.MaxRequestBodyBytes)
	if err := r.ParseForm(); err != nil {
		a.writeTokenExchangeError(w, contractsv1.TokenExchangeErrorInvalidRequest, http.StatusBadRequest)
		return
	}
	request := contractsv1.TokenExchangeRequest{
		GrantType:          r.PostForm.Get("grant_type"),
		SubjectToken:       r.PostForm.Get("subject_token"),
		SubjectTokenType:   r.PostForm.Get("subject_token_type"),
		RequestedTokenType: r.PostForm.Get("requested_token_type"),
		Scope:              r.PostForm.Get("scope"),
	}
	if err := request.Validate(); err != nil {
		code := contractsv1.TokenExchangeErrorInvalidRequest
		if request.GrantType != contractsv1.TokenExchangeGrantType {
			code = contractsv1.TokenExchangeErrorUnsupportedGrantType
		}
		a.writeTokenExchangeError(w, code, http.StatusBadRequest)
		return
	}
	result, err := a.runtime.WorkloadTokenExchange.Exchange(r.Context(), request.SubjectToken, request.ScopeList())
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrSubjectTokenInvalid), errors.Is(err, auth.ErrWorkloadBindingNotFound):
			// Deliberately the SAME error code for an invalid subject
			// token and an unresolved/disabled binding -- a disabled
			// binding must not be distinguishable from one that never
			// existed (see storageGrantResolver.Resolve's doc comment).
			a.writeTokenExchangeError(w, contractsv1.TokenExchangeErrorInvalidGrant, http.StatusBadRequest)
		case errors.Is(err, auth.ErrScopeNotGranted):
			a.writeTokenExchangeError(w, contractsv1.TokenExchangeErrorInvalidScope, http.StatusBadRequest)
		default:
			a.writeDeviceDependencyError(w, r)
		}
		return
	}
	writeJSON(w, http.StatusOK, contractsv1.TokenExchangeResponse{
		SchemaVersion: contractsv1.TokenExchangeResponseSchema, AccessToken: result.AccessToken,
		IssuedTokenType: contractsv1.TokenExchangeAccessTokenType, TokenType: "Bearer",
		ExpiresIn: result.ExpiresIn, Scope: strings.Join(result.Scope, " "),
	})
}

func (a *App) writeTokenExchangeError(w http.ResponseWriter, code contractsv1.OAuthTokenExchangeErrorCode, status int) {
	writeJSON(w, status, contractsv1.OAuthTokenExchangeErrorResponse{SchemaVersion: contractsv1.OAuthTokenExchangeErrorSchema, Error: code})
}
