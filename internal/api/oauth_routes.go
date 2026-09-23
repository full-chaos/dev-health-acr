package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/logsanitize"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// OAuth authorization server routes for hosted MCP clients. They are
// registered (in Handler) only when the runtime carries an OAuth
// configuration. Each path is a single-line const so route discovery
// (ci/discover_acr_routes.go) resolves it.

// OAuthAuthorizationServerMetadataPath serves RFC 8414 metadata.
const OAuthAuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"

// OAuthAuthorizePath is the authorization endpoint.
const OAuthAuthorizePath = "/authorize"

// OAuthConsentPath is where the web consent page (acting for the signed-in
// user, authenticated by a web assertion) reads and decides a request.
const OAuthConsentPath = "/authorize/consent"

// OAuthTokenPath is the token endpoint.
const OAuthTokenPath = "/token"

// OAuthRegisterPath is the dynamic client registration endpoint.
const OAuthRegisterPath = "/register"

// OAuthDeviceAuthorizationPath is RFC 8628's device authorization endpoint,
// which starts a device-code grant for headless/remote clients.
const OAuthDeviceAuthorizationPath = "/device_authorization"

const oauthFormMaxBytes = 16 * 1024

// oauthAuthorizationServerMetadata is the RFC 8414 document.
type oauthAuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	DeviceAuthorizationEndpoint       string   `json:"device_authorization_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParameter bool     `json:"authorization_response_iss_parameter_supported"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
}

func (a *App) handleOAuthMetadata(w http.ResponseWriter, _ *http.Request) {
	issuer := a.oauth.Issuer()
	writeOAuthJSON(w, http.StatusOK, oauthAuthorizationServerMetadata{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + OAuthAuthorizePath,
		TokenEndpoint:                     issuer + OAuthTokenPath,
		RegistrationEndpoint:              issuer + OAuthRegisterPath,
		DeviceAuthorizationEndpoint:       issuer + OAuthDeviceAuthorizationPath,
		ScopesSupported:                   []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code", auth.OAuthDeviceCodeGrantType},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		AuthorizationResponseIssParameter: true,
		ClientIDMetadataDocumentSupported: a.oauth.ClientMetadataDocumentsSupported(),
	})
}

func writeOAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, value)
}

type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// emitOAuthStep writes the one telemetry line of an OAuth request.
func (a *App) emitOAuthStep(r *http.Request, step, outcome, clientKind string, status int) {
	a.emitOAuthStepScopes(r, step, outcome, clientKind, status, []string{})
}

// emitOAuthStepScopes writes the line with the requested (authorize) or
// granted (token) scopes.
func (a *App) emitOAuthStepScopes(r *http.Request, step, outcome, clientKind string, status int, scopes []string) {
	if marker, ok := r.Context().Value(oauthLineMarkerKey{}).(*oauthLineMarker); ok {
		marker.emitted = true
	}
	if clientKind == "" {
		clientKind = oauthvocab.ClientKindNone
	}
	fields := eventspec.NewOAuthStepFields(RequestID(r.Context()), step, outcome, clientKind, scopes, status)
	a.logger.InfoContext(r.Context(), eventspec.OAuthStepLogMessage, fields.SlogArgs()...)
}

type oauthLineMarkerKey struct{}

// oauthLineMarker records whether a request already wrote its OAuth line.
type oauthLineMarker struct{ emitted bool }

// oauthConsentLine keeps the one-OAuth-line-per-request contract on the
// consent route, whose web-assertion wrapper answers before the handler runs
// (401 without a valid assertion, 503 without the approval runtime): any
// request the handler never reached still writes a consent line, with the
// outcome its status means.
func (a *App) oauthConsentLine(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker := &oauthLineMarker{}
		r = r.WithContext(context.WithValue(r.Context(), oauthLineMarkerKey{}, marker))
		recorder := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		if marker.emitted {
			return
		}
		outcome := oauthvocab.OutcomeInvalidRequest
		switch {
		case recorder.status == http.StatusUnauthorized || recorder.status == http.StatusForbidden:
			outcome = oauthvocab.OutcomeUnauthenticated
		case recorder.status == http.StatusTooManyRequests:
			outcome = oauthvocab.OutcomeRateLimited
		case recorder.status >= http.StatusInternalServerError:
			outcome = oauthvocab.OutcomeUnavailable
		}
		a.emitOAuthStep(r, oauthvocab.StepConsent, outcome, "", recorder.status)
	})
}

// oauthOutcome maps a service error to its telemetry outcome and OAuth code.
func oauthOutcome(err error) (*auth.OAuthError, bool) {
	var oauthErr *auth.OAuthError
	if errors.As(err, &oauthErr) {
		return oauthErr, true
	}
	return nil, false
}

func (a *App) oauthRateLimited(w http.ResponseWriter, r *http.Request, step string, allow func(string) DeviceAuthorizationLimitDecision) bool {
	decision := allow(a.clientIP(r))
	if decision.Allowed {
		return false
	}
	seconds := max(1, int((decision.RetryAfter+time.Second-1)/time.Second))
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeOAuthJSON(w, http.StatusTooManyRequests, oauthErrorBody{Error: "slow_down", ErrorDescription: "too many requests"})
	a.emitOAuthStep(r, step, oauthvocab.OutcomeRateLimited, "", http.StatusTooManyRequests)
	return true
}

func (a *App) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	if a.oauthRateLimited(w, r, oauthvocab.StepRegister, a.runtime.DeviceAuthorizationLimiter.AllowDeviceCreation) {
		return
	}
	var request auth.OAuthRegistrationRequest
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_client_metadata", ErrorDescription: "registration requires a JSON body"})
		a.emitOAuthStep(r, oauthvocab.StepRegister, oauthvocab.OutcomeInvalidClientMetadata, "", http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, oauthFormMaxBytes))
	if err := decoder.Decode(&request); err != nil {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_client_metadata", ErrorDescription: "registration body is not valid client metadata"})
		a.emitOAuthStep(r, oauthvocab.StepRegister, oauthvocab.OutcomeInvalidClientMetadata, "", http.StatusBadRequest)
		return
	}
	client, err := a.oauth.Register(r.Context(), request)
	if err != nil {
		if refusal, ok := oauthOutcome(err); ok {
			writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: refusal.Code})
			a.emitOAuthStep(r, oauthvocab.StepRegister, refusal.Outcome, "", http.StatusBadRequest)
			return
		}
		a.logOAuthDependencyFailure(r, oauthvocab.StepRegister)
		writeOAuthJSON(w, http.StatusServiceUnavailable, oauthErrorBody{Error: "temporarily_unavailable"})
		a.emitOAuthStep(r, oauthvocab.StepRegister, oauthvocab.OutcomeUnavailable, "", http.StatusServiceUnavailable)
		return
	}
	// Every registered client can run the device grant (StartDeviceAuthorization
	// never checks redirect_uris); authorization_code additionally applies
	// only when the client has at least one registered redirect_uri (Register
	// requires one whenever the client wants authorization_code, so its
	// presence here means exactly that).
	grantTypes := []string{auth.OAuthDeviceCodeGrantType}
	responseTypes := []string{}
	if len(client.RedirectURIs) > 0 {
		grantTypes = []string{"authorization_code", auth.OAuthDeviceCodeGrantType}
		responseTypes = []string{"code"}
	}
	writeOAuthJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ClientID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"client_name":                client.ClientName,
		"redirect_uris":              client.RedirectURIs,
		"grant_types":                grantTypes,
		"response_types":             responseTypes,
		"token_endpoint_auth_method": "none",
	})
	a.emitOAuthStep(r, oauthvocab.StepRegister, oauthvocab.OutcomeOK, oauthvocab.ClientKindDynamic, http.StatusCreated)
}

func (a *App) logOAuthDependencyFailure(r *http.Request, step string) {
	a.logger.WarnContext(r.Context(), "oauth dependency failed",
		"request_id", logsanitize.SanitizeLogAttr(RequestID(r.Context())),
		"step", step,
		"failure_class", "oauth_dependency",
	)
}

// logOAuthRedirectMismatch diagnoses an invalid_redirect_uri refusal: the
// registered and presented redirect_uri origins only (scheme+host+port),
// never a full redirect URI, path, query or credential.
func (a *App) logOAuthRedirectMismatch(r *http.Request, mismatch *auth.OAuthRedirectMismatch) {
	a.logger.WarnContext(r.Context(), "oauth redirect_uri mismatch",
		"request_id", logsanitize.SanitizeLogAttr(RequestID(r.Context())),
		"step", oauthvocab.StepAuthorize,
		"registered_origins", mismatch.Registered,
		"presented_origin", mismatch.Presented,
	)
}

// oauthDeviceAuthorizationBody is RFC 8628 §3.2's device authorization
// response.
type oauthDeviceAuthorizationBody struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete carries the user code as a query parameter so
	// most users only need to click it, never type the code. Omitted only if
	// the configured verification URL fails to parse (config validation
	// rejects that at startup, so this is a defensive fallback, not the
	// normal path).
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// deviceVerificationURIComplete appends user_code to the verification URI's
// query string, preserving any existing query. Returns "" if verificationURI
// does not parse.
func deviceVerificationURIComplete(verificationURI, userCode string) string {
	parsed, err := url.Parse(verificationURI)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	query.Set("user_code", userCode)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// handleOAuthDeviceAuthorization serves RFC 8628's device_authorization
// endpoint: a client with no browser of its own (or that cannot receive a
// redirect, e.g. a headless host) starts a device-code grant here and polls
// OAuthTokenPath with the returned device_code while a human approves it at
// VerificationURI, typing UserCode -- the SAME device_authorizations row and
// approval page (POST /api/v1/oauth/device_approval) an OAuth
// authorization-code request's browser consent already reuses internally
// (device_oauth.go), except here the raw codes leave the server, as RFC 8628
// requires.
func (a *App) handleOAuthDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if a.oauthRateLimited(w, r, oauthvocab.StepDeviceAuthorization, a.runtime.DeviceAuthorizationLimiter.AllowDeviceCreation) {
		return
	}
	if err := parseOAuthForm(w, r); err != nil {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_request"})
		a.emitOAuthStep(r, oauthvocab.StepDeviceAuthorization, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	for _, values := range form {
		if len(values) > 1 {
			writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_request"})
			a.emitOAuthStep(r, oauthvocab.StepDeviceAuthorization, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
			return
		}
	}
	start, err := a.oauth.StartDeviceAuthorization(r.Context(), auth.OAuthDeviceAuthorizationRequest{
		ClientID: form.Get("client_id"), Scope: form.Get("scope"), Resource: form.Get("resource"),
	})
	if err != nil {
		if refusal, ok := oauthOutcome(err); ok {
			writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: refusal.Code})
			a.emitOAuthStep(r, oauthvocab.StepDeviceAuthorization, refusal.Outcome, start.ClientKind, http.StatusBadRequest)
			return
		}
		a.logOAuthDependencyFailure(r, oauthvocab.StepDeviceAuthorization)
		writeOAuthJSON(w, http.StatusServiceUnavailable, oauthErrorBody{Error: "temporarily_unavailable"})
		a.emitOAuthStep(r, oauthvocab.StepDeviceAuthorization, oauthvocab.OutcomeUnavailable, start.ClientKind, http.StatusServiceUnavailable)
		return
	}
	writeOAuthJSON(w, http.StatusOK, oauthDeviceAuthorizationBody{
		DeviceCode: start.DeviceCode, UserCode: start.UserCode,
		VerificationURI:         a.runtime.DeviceVerificationURL,
		VerificationURIComplete: deviceVerificationURIComplete(a.runtime.DeviceVerificationURL, start.UserCode),
		ExpiresIn:               int64(start.ExpiresIn / time.Second),
		Interval:                int64(start.Interval / time.Second),
	})
	a.emitOAuthStep(r, oauthvocab.StepDeviceAuthorization, oauthvocab.OutcomeOK, start.ClientKind, http.StatusOK)
}

func (a *App) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if a.oauthRateLimited(w, r, oauthvocab.StepAuthorize, a.runtime.DeviceAuthorizationLimiter.AllowDeviceCreation) {
		return
	}
	query := r.URL.Query()
	for _, key := range []string{"response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method", "resource", "scope", "state"} {
		if len(query[key]) > 1 {
			a.renderOAuthProblem(w, r, http.StatusBadRequest, "The authorization request repeats a parameter.")
			a.emitOAuthStep(r, oauthvocab.StepAuthorize, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
			return
		}
	}
	request := auth.OAuthAuthorizeRequest{
		ResponseType: query.Get("response_type"), ClientID: query.Get("client_id"), RedirectURI: query.Get("redirect_uri"),
		CodeChallenge: query.Get("code_challenge"), CodeChallengeMethod: query.Get("code_challenge_method"),
		Resource: query.Get("resource"), Scope: query.Get("scope"), State: query.Get("state"),
	}
	authorization, err := a.oauth.Authorize(r.Context(), request)
	if err != nil {
		if refusal, ok := oauthOutcome(err); ok {
			if refusal.Redirectable && refusal.RedirectURL != "" {
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, refusal.RedirectURL, http.StatusSeeOther)
				a.emitOAuthStep(r, oauthvocab.StepAuthorize, refusal.Outcome, "", http.StatusSeeOther)
				return
			}
			a.renderOAuthProblem(w, r, http.StatusBadRequest, "The application's sign-in request is not valid, so it cannot be sent back to the application.")
			if refusal.RedirectMismatch != nil {
				a.logOAuthRedirectMismatch(r, refusal.RedirectMismatch)
			}
			a.emitOAuthStep(r, oauthvocab.StepAuthorize, refusal.Outcome, "", http.StatusBadRequest)
			return
		}
		a.logOAuthDependencyFailure(r, oauthvocab.StepAuthorize)
		a.renderOAuthProblem(w, r, http.StatusServiceUnavailable, "Sign-in is temporarily unavailable. Try again in a moment.")
		a.emitOAuthStep(r, oauthvocab.StepAuthorize, oauthvocab.OutcomeUnavailable, "", http.StatusServiceUnavailable)
		return
	}
	// The browser goes straight to the web consent page for this request;
	// the web signs the user in first when needed and comes back to it.
	target, err := url.Parse(a.oauthConsentURL)
	if err != nil {
		a.renderOAuthProblem(w, r, http.StatusServiceUnavailable, "Sign-in is temporarily unavailable. Try again in a moment.")
		a.emitOAuthStep(r, oauthvocab.StepAuthorize, oauthvocab.OutcomeUnavailable, authorization.Client.Kind, http.StatusServiceUnavailable)
		return
	}
	target.RawQuery = url.Values{"handle": {authorization.Handle}}.Encode()
	target.Fragment, target.RawFragment = "", ""
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target.String(), http.StatusFound)
	a.emitOAuthStepScopes(r, oauthvocab.StepAuthorize, oauthvocab.OutcomeOK, authorization.Client.Kind, http.StatusFound, strings.Fields(authorization.Scope))
}

// oauthConsentRequestBody is what the web consent page sends. preview reads
// the request; approve and deny decide it, once.
type oauthConsentRequestBody struct {
	Action           string   `json:"action"`
	Handle           string   `json:"handle"`
	RepositoryScopes []string `json:"repository_scopes,omitempty"`
}

const (
	oauthConsentActionPreview = "preview"
	oauthConsentActionApprove = "approve"
	oauthConsentActionDeny    = "deny"
)

type oauthConsentPreviewBody struct {
	ClientName         string   `json:"client_name"`
	ClientSelfAsserted bool     `json:"client_self_asserted"`
	ClientKind         string   `json:"client_kind"`
	RedirectOrigin     string   `json:"redirect_origin"`
	Resource           string   `json:"resource"`
	Scopes             []string `json:"scopes"`
	ExpiresAt          string   `json:"expires_at"`
}

type oauthConsentDecisionBody struct {
	RedirectURL string `json:"redirect_url"`
}

// oauthConsentStatus maps a consent refusal code to its HTTP status.
var oauthConsentStatus = map[string]int{
	auth.OAuthConsentCodeInvalid:   http.StatusBadRequest,
	auth.OAuthConsentCodeExpired:   http.StatusGone,
	auth.OAuthConsentCodeCompleted: http.StatusConflict,
}

// handleOAuthConsent serves the web consent page. The route wrapper admits
// only a web assertion (credential:issue) for the signed-in user, so the org
// and the repositories an approval grants come from the web, never the
// browser that holds the handle.
func (a *App) handleOAuthConsent(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeOAuthConsentBody(w, r)
	step := oauthvocab.StepConsent
	if ok && request.Action == oauthConsentActionPreview {
		step = oauthvocab.StepConsentPreview
	}
	if !ok {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: auth.OAuthConsentCodeInvalid})
		a.emitOAuthStep(r, step, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
		return
	}
	// Limited per request handle: see oauthConsentRequestLimit.
	if decision := a.runtime.DeviceAuthorizationLimiter.AllowOAuthConsentRequest(storage.HashOAuthSecret(request.Handle)); !decision.Allowed {
		seconds := max(1, int((decision.RetryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeOAuthJSON(w, http.StatusTooManyRequests, oauthErrorBody{Error: "slow_down", ErrorDescription: "too many requests"})
		a.emitOAuthStep(r, step, oauthvocab.OutcomeRateLimited, "", http.StatusTooManyRequests)
		return
	}
	principal, found := auth.PrincipalFromContext(r.Context())
	if !found {
		writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
		a.emitOAuthStep(r, step, oauthvocab.OutcomeInvalidRequest, "", http.StatusUnauthorized)
		return
	}
	switch request.Action {
	case oauthConsentActionPreview:
		view, clientKind, err := a.oauth.ConsentRequest(r.Context(), request.Handle, principal)
		if err != nil {
			a.writeOAuthConsentError(w, r, step, clientKind, err)
			return
		}
		writeOAuthJSON(w, http.StatusOK, oauthConsentPreviewBody{
			ClientName: view.ClientName, ClientSelfAsserted: view.ClientKind == storage.OAuthClientKindDynamic,
			ClientKind: view.ClientKind, RedirectOrigin: view.RedirectOrigin, Resource: view.Resource,
			Scopes: view.Scopes, ExpiresAt: view.ExpiresAt.Format(time.RFC3339),
		})
		a.emitOAuthStep(r, step, oauthvocab.OutcomeOK, view.ClientKind, http.StatusOK)
	case oauthConsentActionApprove:
		decision, err := a.oauth.ApproveConsent(r.Context(), request.Handle, principal, request.RepositoryScopes)
		if err != nil {
			a.writeOAuthConsentError(w, r, step, decision.ClientKind, err)
			return
		}
		writeOAuthJSON(w, http.StatusOK, oauthConsentDecisionBody{RedirectURL: decision.RedirectURL})
		a.emitOAuthStep(r, step, oauthvocab.OutcomeOK, decision.ClientKind, http.StatusOK)
	case oauthConsentActionDeny:
		decision, err := a.oauth.DenyConsent(r.Context(), request.Handle, principal)
		if err != nil {
			a.writeOAuthConsentError(w, r, step, decision.ClientKind, err)
			return
		}
		writeOAuthJSON(w, http.StatusOK, oauthConsentDecisionBody{RedirectURL: decision.RedirectURL})
		a.emitOAuthStep(r, step, oauthvocab.OutcomeAccessDenied, decision.ClientKind, http.StatusOK)
	}
}

// decodeOAuthConsentBody reads one JSON object with a known action; approve
// must name repositories, preview and deny must not.
func decodeOAuthConsentBody(w http.ResponseWriter, r *http.Request) (oauthConsentRequestBody, bool) {
	var request oauthConsentRequestBody
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return request, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, oauthFormMaxBytes))
	if err != nil || !uniqueTopLevelJSONKeys(raw) {
		return oauthConsentRequestBody{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.More() {
		return oauthConsentRequestBody{}, false
	}
	switch request.Action {
	case oauthConsentActionApprove:
		return request, len(request.RepositoryScopes) > 0
	case oauthConsentActionPreview, oauthConsentActionDeny:
		return request, request.RepositoryScopes == nil
	default:
		return oauthConsentRequestBody{}, false
	}
}

// uniqueTopLevelJSONKeys reports whether raw is one JSON object whose keys
// are all distinct. encoding/json keeps the last of repeated keys, so
// {"action":"preview","action":"approve"} would otherwise decode as approve.
func uniqueTopLevelJSONKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	return true
}

func (a *App) writeOAuthConsentError(w http.ResponseWriter, r *http.Request, step, clientKind string, err error) {
	if refusal, ok := oauthOutcome(err); ok {
		status, known := oauthConsentStatus[refusal.Code]
		if !known {
			status = http.StatusBadRequest
		}
		writeOAuthJSON(w, status, oauthErrorBody{Error: refusal.Code})
		a.emitOAuthStep(r, step, refusal.Outcome, clientKind, status)
		return
	}
	a.logOAuthDependencyFailure(r, step)
	writeOAuthJSON(w, http.StatusServiceUnavailable, oauthErrorBody{Error: "temporarily_unavailable"})
	a.emitOAuthStep(r, step, oauthvocab.OutcomeUnavailable, clientKind, http.StatusServiceUnavailable)
}

type oauthTokenBody struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

func (a *App) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if a.oauthRateLimited(w, r, oauthvocab.StepToken, a.runtime.DeviceAuthorizationLimiter.AllowTokenRequest) {
		return
	}
	if err := parseOAuthForm(w, r); err != nil {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_request"})
		a.emitOAuthStep(r, oauthvocab.StepToken, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
		return
	}
	form := r.PostForm
	for _, values := range form {
		if len(values) > 1 {
			writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_request"})
			a.emitOAuthStep(r, oauthvocab.StepToken, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
			return
		}
	}
	clientID, ok := tokenClientID(r)
	if !ok {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_request"})
		a.emitOAuthStep(r, oauthvocab.StepToken, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
		return
	}
	var token auth.OAuthToken
	var err error
	if form.Get("grant_type") == auth.OAuthDeviceCodeGrantType {
		token, err = a.oauth.ExchangeDeviceCode(r.Context(), auth.OAuthDeviceTokenRequest{
			GrantType: form.Get("grant_type"), DeviceCode: form.Get("device_code"), ClientID: clientID,
		})
	} else {
		token, err = a.oauth.Exchange(r.Context(), auth.OAuthTokenRequest{
			GrantType: form.Get("grant_type"), Code: form.Get("code"), RedirectURI: form.Get("redirect_uri"),
			ClientID: clientID, CodeVerifier: form.Get("code_verifier"), Resource: form.Get("resource"),
		})
	}
	if err != nil {
		if refusal, ok := oauthOutcome(err); ok {
			if refusal.RetryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int((refusal.RetryAfter+time.Second-1)/time.Second))))
			}
			writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: refusal.Code})
			a.emitOAuthStep(r, oauthvocab.StepToken, refusal.Outcome, token.ClientKind, http.StatusBadRequest)
			return
		}
		a.logOAuthDependencyFailure(r, oauthvocab.StepToken)
		writeOAuthJSON(w, http.StatusServiceUnavailable, oauthErrorBody{Error: "temporarily_unavailable"})
		a.emitOAuthStep(r, oauthvocab.StepToken, oauthvocab.OutcomeUnavailable, token.ClientKind, http.StatusServiceUnavailable)
		return
	}
	writeOAuthJSON(w, http.StatusOK, oauthTokenBody{
		AccessToken: token.Issued.Token, TokenType: "Bearer",
		ExpiresIn: int64(token.ExpiresIn / time.Second), Scope: token.Scope,
	})
	a.emitOAuthStepScopes(r, oauthvocab.StepToken, oauthvocab.OutcomeOK, token.ClientKind, http.StatusOK, strings.Fields(token.Scope))
}

// tokenClientID reads the client ID of a public client: the client_id form
// parameter, or HTTP Basic credentials with an empty password (RFC 6749
// §2.3.1, which some OAuth libraries send by default). Both present must
// agree; a password is refused, since no client here has a secret.
func tokenClientID(r *http.Request) (string, bool) {
	form := r.PostForm.Get("client_id")
	header := r.Header.Values("Authorization")
	if len(header) == 0 {
		return form, true
	}
	if len(header) > 1 {
		return "", false
	}
	user, password, ok := r.BasicAuth()
	if !ok || password != "" {
		return "", false
	}
	basic, err := url.QueryUnescape(user)
	if err != nil || basic == "" || (form != "" && form != basic) {
		return "", false
	}
	return basic, true
}

var errOAuthForm = errors.New("oauth request must be a form-encoded body")

func parseOAuthForm(w http.ResponseWriter, r *http.Request) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return errOAuthForm
	}
	r.Body = http.MaxBytesReader(w, r.Body, oauthFormMaxBytes)
	if err := r.ParseForm(); err != nil {
		return err
	}
	if r.URL.RawQuery != "" {
		return errOAuthForm
	}
	return nil
}

var oauthProblemTemplate = template.Must(template.New("problem").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="referrer" content="no-referrer"><title>Sign-in stopped</title>
<style nonce="{{.Nonce}}">body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem;color:#1b1f24;background:#fff}@media (prefers-color-scheme:dark){body{color:#e6e8eb;background:#0f1216}}</style>
</head>
<body><h1>Sign-in stopped</h1><p>{{.Message}}</p></body>
</html>
`))

func oauthPageNonce() string {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "unavailable"
	}
	return base64.RawStdEncoding.EncodeToString(buf)
}

func setOAuthPageHeaders(w http.ResponseWriter, nonce string) {
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; connect-src 'none'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
}

func (a *App) renderOAuthProblem(w http.ResponseWriter, _ *http.Request, status int, message string) {
	nonce := oauthPageNonce()
	setOAuthPageHeaders(w, nonce)
	w.WriteHeader(status)
	_ = oauthProblemTemplate.Execute(w, map[string]any{"Nonce": nonce, "Message": message})
}

// ErrOAuthRequiresWebApproval reports an OAuth configuration on a runtime
// without the web approval surface: no consent could ever be approved.
var ErrOAuthRequiresWebApproval = errors.New("oauth login requires the web approval surface (web assertion verification)")

// ErrOAuthRequiresConsentURL reports an OAuth configuration without a valid
// web consent page URL: /authorize would have nowhere to send the browser.
var ErrOAuthRequiresConsentURL = errors.New("oauth login requires the web consent page URL")

func newOAuthService(deps Dependencies, deviceFlow *auth.DeviceFlowService) (*auth.OAuthService, error) {
	runtime := deps.Runtime.OAuth
	if runtime == nil {
		return nil, nil
	}
	if deps.WebAssertions == nil {
		return nil, ErrOAuthRequiresWebApproval
	}
	if !auth.ValidOAuthConsentURL(runtime.ConsentURL) {
		return nil, ErrOAuthRequiresConsentURL
	}
	return auth.NewOAuthService(runtime.Store, deviceFlow, auth.OAuthConfig{
		Issuer: runtime.Issuer, Resources: runtime.Resources, ClientMetadata: runtime.ClientMetadata, Now: deps.Now,
	})
}

// OAuthRuntime configures the OAuth authorization-code login hosted MCP
// clients use.
type OAuthRuntime struct {
	Store storage.OAuthStore
	// Issuer is the acr-api public origin.
	Issuer string
	// Resources are the hosted MCP endpoint URLs credentials may be bound to.
	Resources []string
	// ClientMetadata resolves client ID metadata documents; nil disables them.
	ClientMetadata auth.OAuthClientMetadataFetcher
	// ConsentURL is the web consent page /authorize redirects the browser to.
	ConsentURL string
}
