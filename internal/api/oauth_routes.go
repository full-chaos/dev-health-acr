package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
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

// OAuthConsentPath is where the consent page checks the approval.
const OAuthConsentPath = "/authorize/consent"

// OAuthTokenPath is the token endpoint.
const OAuthTokenPath = "/token"

// OAuthRegisterPath is the dynamic client registration endpoint.
const OAuthRegisterPath = "/register"

const oauthFormMaxBytes = 16 * 1024

// oauthAuthorizationServerMetadata is the RFC 8414 document.
type oauthAuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ResponseModesSupported            []string `json:"response_modes_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParameter bool     `json:"authorization_response_iss_parameter_supported"`
}

func (a *App) handleOAuthMetadata(w http.ResponseWriter, _ *http.Request) {
	issuer := a.oauth.Issuer()
	writeOAuthJSON(w, http.StatusOK, oauthAuthorizationServerMetadata{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + OAuthAuthorizePath,
		TokenEndpoint:                     issuer + OAuthTokenPath,
		RegistrationEndpoint:              issuer + OAuthRegisterPath,
		ScopesSupported:                   []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
		ResponseTypesSupported:            []string{"code"},
		ResponseModesSupported:            []string{"query"},
		GrantTypesSupported:               []string{"authorization_code"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		AuthorizationResponseIssParameter: true,
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
	if clientKind == "" {
		clientKind = oauthvocab.ClientKindNone
	}
	fields := eventspec.NewOAuthStepFields(RequestID(r.Context()), step, outcome, clientKind, scopes, status)
	a.logger.InfoContext(r.Context(), eventspec.OAuthStepLogMessage, fields.SlogArgs()...)
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
	writeOAuthJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ClientID,
		"client_id_issued_at":        client.CreatedAt.Unix(),
		"client_name":                client.ClientName,
		"redirect_uris":              client.RedirectURIs,
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
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
			a.emitOAuthStep(r, oauthvocab.StepAuthorize, refusal.Outcome, "", http.StatusBadRequest)
			return
		}
		a.logOAuthDependencyFailure(r, oauthvocab.StepAuthorize)
		a.renderOAuthProblem(w, r, http.StatusServiceUnavailable, "Sign-in is temporarily unavailable. Try again in a moment.")
		a.emitOAuthStep(r, oauthvocab.StepAuthorize, oauthvocab.OutcomeUnavailable, "", http.StatusServiceUnavailable)
		return
	}
	a.renderOAuthConsent(w, r, authorization)
	a.emitOAuthStepScopes(r, oauthvocab.StepAuthorize, oauthvocab.OutcomeOK, authorization.Client.Kind, http.StatusOK, strings.Fields(authorization.Scope))
}

type oauthConsentBody struct {
	State       string `json:"state"`
	RedirectURL string `json:"redirect_url,omitempty"`
}

func (a *App) handleOAuthConsent(w http.ResponseWriter, r *http.Request) {
	if a.oauthRateLimited(w, r, oauthvocab.StepConsent, a.runtime.DeviceAuthorizationLimiter.AllowOAuthConsentCheck) {
		return
	}
	if err := parseOAuthForm(w, r); err != nil {
		writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: "invalid_request"})
		a.emitOAuthStep(r, oauthvocab.StepConsent, oauthvocab.OutcomeInvalidRequest, "", http.StatusBadRequest)
		return
	}
	consent, err := a.oauth.Consent(r.Context(), r.PostForm.Get("handle"))
	if err != nil {
		if refusal, ok := oauthOutcome(err); ok {
			writeOAuthJSON(w, http.StatusBadRequest, oauthErrorBody{Error: refusal.Code})
			a.emitOAuthStep(r, oauthvocab.StepConsent, refusal.Outcome, consent.ClientKind, http.StatusBadRequest)
			return
		}
		a.logOAuthDependencyFailure(r, oauthvocab.StepConsent)
		writeOAuthJSON(w, http.StatusServiceUnavailable, oauthErrorBody{Error: "temporarily_unavailable"})
		a.emitOAuthStep(r, oauthvocab.StepConsent, oauthvocab.OutcomeUnavailable, consent.ClientKind, http.StatusServiceUnavailable)
		return
	}
	outcome := map[auth.OAuthConsentState]string{
		auth.OAuthConsentPending:  oauthvocab.OutcomePending,
		auth.OAuthConsentApproved: oauthvocab.OutcomeOK,
		auth.OAuthConsentDenied:   oauthvocab.OutcomeAccessDenied,
		auth.OAuthConsentExpired:  oauthvocab.OutcomeExpired,
	}[consent.State]
	if r.PostForm.Get("navigate") != "" {
		// The page's form without script: go back to the application once
		// there is somewhere to go, otherwise say what is still missing.
		switch {
		case consent.RedirectURL != "":
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, consent.RedirectURL, http.StatusSeeOther)
			a.emitOAuthStep(r, oauthvocab.StepConsent, outcome, consent.ClientKind, http.StatusSeeOther)
		case consent.State == auth.OAuthConsentPending:
			a.renderOAuthProblem(w, r, http.StatusOK, "The request is not approved yet. Go back, approve the code, then continue again.")
			a.emitOAuthStep(r, oauthvocab.StepConsent, outcome, consent.ClientKind, http.StatusOK)
		default:
			a.renderOAuthProblem(w, r, http.StatusOK, "This request expired. Start the sign-in again from the application.")
			a.emitOAuthStep(r, oauthvocab.StepConsent, outcome, consent.ClientKind, http.StatusOK)
		}
		return
	}
	writeOAuthJSON(w, http.StatusOK, oauthConsentBody{State: string(consent.State), RedirectURL: consent.RedirectURL})
	a.emitOAuthStep(r, oauthvocab.StepConsent, outcome, consent.ClientKind, http.StatusOK)
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
	token, err := a.oauth.Exchange(r.Context(), auth.OAuthTokenRequest{
		GrantType: form.Get("grant_type"), Code: form.Get("code"), RedirectURI: form.Get("redirect_uri"),
		ClientID: clientID, CodeVerifier: form.Get("code_verifier"), Resource: form.Get("resource"),
	})
	if err != nil {
		if refusal, ok := oauthOutcome(err); ok {
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

// The consent page. It holds the browser handle in the page (never in a URL),
// shows the user code the user approves on the web approval page, and checks
// the approval until it can send the browser back to the application.
var oauthConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Approve agent access</title>
<style nonce="{{.Nonce}}">
body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;max-width:36rem;margin:3rem auto;padding:0 1rem;color:#1b1f24;background:#fff}
@media (prefers-color-scheme:dark){body{color:#e6e8eb;background:#0f1216}a{color:#8ab4ff}}
.code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:2rem;letter-spacing:.2rem;padding:.5rem 1rem;border:1px solid #8888;border-radius:.5rem;display:inline-block}
.muted{opacity:.75;font-size:.9rem}
button{font:inherit;padding:.5rem 1rem;border-radius:.4rem;border:1px solid #8888;cursor:pointer}
</style>
</head>
<body>
<h1>Approve agent access</h1>
<p><strong>{{.ClientName}}</strong> is asking for read access to your organization's agent context{{if .Resource}} on <code>{{.Resource}}</code>{{end}}.</p>
{{if .SelfAsserted}}<p class="muted">The application named itself. Approve only if you started this sign-in.</p>{{end}}
<ol>
<li>Open <a href="{{.VerificationURL}}" target="_blank" rel="noopener noreferrer">{{.VerificationURL}}</a> and sign in.</li>
<li>Enter this code and choose the repositories to share:<br><span class="code" id="user-code">{{.UserCode}}</span></li>
<li>Come back to this page. It returns you to the application when you have approved.</li>
</ol>
<p id="status" role="status">Waiting for approval. The code expires at {{.ExpiresAt}}.</p>
<form method="post" action="{{.ConsentPath}}" id="continue">
<input type="hidden" name="handle" value="{{.Handle}}">
<input type="hidden" name="navigate" value="1">
<button type="submit">I approved it, continue</button>
</form>
<script nonce="{{.Nonce}}">
(function(){
var form=document.getElementById("continue"),status=document.getElementById("status"),stopped=false;
function check(){
if(stopped)return;
fetch(form.action,{method:"POST",credentials:"omit",headers:{"Content-Type":"application/x-www-form-urlencoded"},body:new URLSearchParams({handle:form.elements.handle.value})})
.then(function(r){return r.json();})
.then(function(b){
if(b.redirect_url){stopped=true;status.textContent="Approved. Returning to the application.";window.location.assign(b.redirect_url);return;}
if(b.state==="expired"){stopped=true;status.textContent="This request expired. Start the sign-in again from the application.";return;}
if(b.error){stopped=true;status.textContent="This request is not valid. Start the sign-in again from the application.";return;}
setTimeout(check,3000);
}).catch(function(){setTimeout(check,5000);});
}
form.addEventListener("submit",function(e){e.preventDefault();check();});
setTimeout(check,3000);
})();
</script>
</body>
</html>
`))

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
	header.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
}

func (a *App) renderOAuthConsent(w http.ResponseWriter, r *http.Request, authorization auth.OAuthAuthorization) {
	nonce := oauthPageNonce()
	setOAuthPageHeaders(w, nonce)
	name := authorization.Client.Name
	if name == "" {
		name = "An application"
	}
	w.WriteHeader(http.StatusOK)
	_ = oauthConsentTemplate.Execute(w, map[string]any{
		"Nonce": nonce, "ClientName": name, "Resource": authorization.Resource,
		"SelfAsserted":    authorization.Client.Kind == storage.OAuthClientKindDynamic,
		"VerificationURL": a.runtime.DeviceVerificationURL, "UserCode": authorization.UserCode,
		"ExpiresAt":   authorization.ExpiresAt.UTC().Format("15:04 UTC"),
		"ConsentPath": OAuthConsentPath, "Handle": authorization.Handle,
	})
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

func newOAuthService(deps Dependencies, deviceFlow *auth.DeviceFlowService) (*auth.OAuthService, error) {
	runtime := deps.Runtime.OAuth
	if runtime == nil {
		return nil, nil
	}
	if deps.WebAssertions == nil {
		return nil, ErrOAuthRequiresWebApproval
	}
	return auth.NewOAuthService(runtime.Store, deviceFlow, auth.OAuthConfig{
		Issuer: runtime.Issuer, Resources: runtime.Resources, Now: deps.Now,
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
}
