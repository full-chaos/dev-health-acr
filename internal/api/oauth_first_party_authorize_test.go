package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
)

// CHAOS-12568: web's server sends every user's authorize, so every user
// reaches ACR from web's address. GET /authorize is limited per source
// (CHAOS-12156), which puts all of web's users in one budget of
// deviceCreationLimit a minute. POST /authorize/first-party is the same
// authorize, signed by the web assertion over its form body and limited per
// user, under the browser client's ceiling.

// webSource is the one address every request of web's server comes from.
const webSource = "10.20.30.40"

func newFirstPartyAuthorizeApp(t *testing.T) (*App, *bytes.Buffer) {
	t.Helper()
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return app, logs
}

// firstPartyCall is one POST /authorize/first-party as web's server sends it.
type firstPartyCall struct {
	subject string
	form    url.Values
	// signed is the body the assertion signs; empty signs the sent body.
	signed       string
	extraClaims  map[string]any
	contentType  string
	query        string
	noAssertion  bool
	jti          string
	permissions  []string
	repositories []string
}

func (c firstPartyCall) serve(t *testing.T, app *App) *httptest.ResponseRecorder {
	t.Helper()
	body := c.form.Encode()
	path := OAuthFirstPartyAuthorizePath
	if c.query != "" {
		path += "?" + c.query
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.RemoteAddr = webSource + ":40000"
	contentType := c.contentType
	if contentType == "" {
		contentType = "application/x-www-form-urlencoded"
	}
	request.Header.Set("Content-Type", contentType)
	if !c.noAssertion {
		signed := c.signed
		if signed == "" {
			signed = body
		}
		request.Header.Set(auth.WebAssertionHeader, c.assertion(t, request, []byte(signed)))
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	return recorder
}

func (c firstPartyCall) assertion(t *testing.T, request *http.Request, body []byte) string {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	digest := sha256.Sum256(body)
	permissions := c.permissions
	if permissions == nil {
		permissions = []string{auth.WebAssertionPermissionCredentialIssue}
	}
	repositories := c.repositories
	if repositories == nil {
		repositories = []string{"*"}
	}
	jti := c.jti
	if jti == "" {
		jti = "jti_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": c.subject, "org_id": oauthTestOrg,
		"repository_scopes": repositories, "permissions": permissions,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
		"method": http.MethodPost, "path": OAuthFirstPartyAuthorizePath, "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	for name, value := range c.extraClaims {
		claims[name] = value
	}
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(oauthTestWebKey, []byte(input)))
}

func firstPartyForm(state string) url.Values {
	form := oauthTestAuthorizeQuery(auth.DevBrowserClientID)
	form.Set("state", state)
	return form
}

func userName(i int) string { return "user_" + strconv.Itoa(i) }

// TestManyUsersThroughWebEachHaveTheirOwnAuthorizeBudget is the red-first
// case of CHAOS-12568. Through GET /authorize, 40 users from web's one address
// get deviceCreationLimit and the rest 429 (the CHAOS-12512 repro). Through
// POST /authorize/first-party the same 40 users, from the same address, are
// all sent to consent, each on their own budget.
func TestManyUsersThroughWebEachHaveTheirOwnAuthorizeBudget(t *testing.T) {
	const users = 40
	shared, _ := newFirstPartyAuthorizeApp(t)
	refused := 0
	for i := range users {
		if status, _ := authorizeFrom(shared, auth.DevBrowserClientID, webSource, userName(i)); status == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != users-deviceCreationLimit {
		t.Fatalf("GET /authorize from one source: %d of %d refused, want %d: this is the shared budget the route fixes", refused, users, users-deviceCreationLimit)
	}
	app, _ := newFirstPartyAuthorizeApp(t)
	for i := range users {
		recorder := firstPartyCall{subject: userName(i), form: firstPartyForm(userName(i))}.serve(t, app)
		if recorder.Code != http.StatusFound {
			t.Fatalf("user %d through web: status %d %s, want 302 to consent: users share one budget", i+1, recorder.Code, recorder.Body.String())
		}
		location, err := url.Parse(recorder.Header().Get("Location"))
		if err != nil || location.Scheme+"://"+location.Host+location.Path != oauthTestConsentURL || location.Query().Get("handle") == "" {
			t.Fatalf("user %d: redirected to %q, want the consent page with a handle", i+1, recorder.Header().Get("Location"))
		}
	}
}

// TestOneUserCannotSpendAnothersAuthorizeBudget: a user past
// deviceCreationLimit is refused with Retry-After; the next user is not.
func TestOneUserCannotSpendAnothersAuthorizeBudget(t *testing.T) {
	app, _ := newFirstPartyAuthorizeApp(t)
	for i := range deviceCreationLimit {
		if code := (firstPartyCall{subject: "user_flood", form: firstPartyForm("flood-" + strconv.Itoa(i))}).serve(t, app).Code; code != http.StatusFound {
			t.Fatalf("authorize %d of user_flood: status %d, want 302", i+1, code)
		}
	}
	over := firstPartyCall{subject: "user_flood", form: firstPartyForm("flood-over")}.serve(t, app)
	if over.Code != http.StatusTooManyRequests || over.Header().Get("Retry-After") == "" {
		t.Fatalf("authorize %d of one user: status %d Retry-After %q, want 429 with Retry-After", deviceCreationLimit+1, over.Code, over.Header().Get("Retry-After"))
	}
	if code := (firstPartyCall{subject: "user_other", form: firstPartyForm("other")}).serve(t, app).Code; code != http.StatusFound {
		t.Fatalf("another user after the flood: status %d, want 302", code)
	}
}

// TestTheBrowserClientCeilingHoldsOverEveryUser: the per-client ceiling of
// GET /authorize also bounds the first-party route, and the two share it.
func TestTheBrowserClientCeilingHoldsOverEveryUser(t *testing.T) {
	app, _ := newFirstPartyAuthorizeApp(t)
	half := browserAuthorizeClientLimit / 2
	for i := range half {
		if status, _ := authorizeFrom(app, auth.DevBrowserClientID, distinctSource(i), "get-"+strconv.Itoa(i)); status != http.StatusFound {
			t.Fatalf("GET %d: status %d, want 302", i+1, status)
		}
	}
	for i := range browserAuthorizeClientLimit - half {
		if code := (firstPartyCall{subject: userName(i), form: firstPartyForm("post-" + strconv.Itoa(i))}).serve(t, app).Code; code != http.StatusFound {
			t.Fatalf("POST %d: status %d, want 302", i+1, code)
		}
	}
	if code := (firstPartyCall{subject: "user_new", form: firstPartyForm("past-ceiling")}).serve(t, app).Code; code != http.StatusTooManyRequests {
		t.Fatalf("a new user past the client ceiling: status %d, want 429", code)
	}
}

// TestTheFirstPartyAuthorizeRefuses: each rule of the route, one request each,
// and each refusal writes its one OAuth line.
func TestTheFirstPartyAuthorizeRefuses(t *testing.T) {
	dynamic := firstPartyForm("state_case_dynamic")
	dynamic.Set("client_id", "https://client.example.test/oauth/client.json")
	repeated := firstPartyForm("state_case_repeated")
	repeated.Add("state", "again")
	unknown := firstPartyForm("state_case_unknown")
	unknown.Set("prompt", "none")
	for _, tc := range []struct {
		name    string
		call    firstPartyCall
		status  int
		outcome string
	}{
		{name: "no assertion", call: firstPartyCall{noAssertion: true}, status: http.StatusUnauthorized, outcome: oauthvocab.OutcomeUnauthenticated},
		{name: "an assertion over another body", call: firstPartyCall{signed: firstPartyForm("state_case_signed").Encode()}, status: http.StatusUnauthorized, outcome: oauthvocab.OutcomeUnauthenticated},
		{name: "a query string", call: firstPartyCall{query: "state=x"}, status: http.StatusUnauthorized, outcome: oauthvocab.OutcomeUnauthenticated},
		{name: "no credential:issue permission", call: firstPartyCall{permissions: []string{"context:read"}, repositories: []string{"acme/api"}}, status: http.StatusForbidden, outcome: oauthvocab.OutcomeUnauthenticated},
		{name: "an impersonating session", call: firstPartyCall{extraClaims: map[string]any{"impersonating": true, "sid": "session_1"}}, status: http.StatusForbidden, outcome: oauthvocab.OutcomeImpersonating},
		{name: "a client that is not the browser class", call: firstPartyCall{form: dynamic}, status: http.StatusBadRequest, outcome: oauthvocab.OutcomeInvalidRequest},
		{name: "a repeated parameter", call: firstPartyCall{form: repeated}, status: http.StatusBadRequest, outcome: oauthvocab.OutcomeInvalidRequest},
		{name: "an unknown parameter", call: firstPartyCall{form: unknown}, status: http.StatusBadRequest, outcome: oauthvocab.OutcomeInvalidRequest},
		{name: "a JSON body", call: firstPartyCall{contentType: "application/json"}, status: http.StatusBadRequest, outcome: oauthvocab.OutcomeInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, logs := newFirstPartyAuthorizeApp(t)
			call := tc.call
			if call.subject == "" {
				call.subject = "user_rules"
			}
			if call.form == nil {
				call.form = firstPartyForm("state_case_rules")
			}
			recorder := call.serve(t, app)
			if recorder.Code != tc.status {
				t.Fatalf("status %d %s, want %d", recorder.Code, recorder.Body.String(), tc.status)
			}
			if lines := strings.Count(logs.String(), `"step":"`+oauthvocab.StepAuthorize+`"`); lines != 1 {
				t.Fatalf("%d authorize lines, want exactly 1:\n%s", lines, logs.String())
			}
			line := oauthLogLine(logs.String(), oauthvocab.StepAuthorize)
			if line == nil || line["outcome"] != tc.outcome {
				t.Fatalf("authorize line %v, want outcome %q", line, tc.outcome)
			}
			assertNoAuthorizeSecretLogged(t, logs.String(), call)
		})
	}
}

// TestAFirstPartyAuthorizeWritesOneLineAndNoSecret: a served request writes
// one authorize line, and no code challenge, state or assertion reaches a log.
func TestAFirstPartyAuthorizeWritesOneLineAndNoSecret(t *testing.T) {
	app, logs := newFirstPartyAuthorizeApp(t)
	call := firstPartyCall{subject: "user_quiet", form: firstPartyForm("state_secret_value")}
	if code := call.serve(t, app).Code; code != http.StatusFound {
		t.Fatalf("status %d, want 302", code)
	}
	if lines := strings.Count(logs.String(), `"step":"`+oauthvocab.StepAuthorize+`"`); lines != 1 {
		t.Fatalf("%d authorize lines, want exactly 1", lines)
	}
	if line := oauthLogLine(logs.String(), oauthvocab.StepAuthorize); line == nil || line["outcome"] != oauthvocab.OutcomeOK {
		t.Fatalf("authorize line %v, want outcome ok", line)
	}
	assertNoAuthorizeSecretLogged(t, logs.String(), call)
}

func assertNoAuthorizeSecretLogged(t *testing.T, logs string, call firstPartyCall) {
	t.Helper()
	for _, secret := range []string{call.form.Get("code_challenge"), call.form.Get("state"), auth.WebAssertionHeader, "eyJ"} {
		if secret != "" && strings.Contains(logs, secret) {
			t.Fatalf("a log line carries %q:\n%s", secret, logs)
		}
	}
}

// TestAFirstPartyAuthorizeNeverFetchesAClientTheBodyNames: a body that names a
// client by a metadata document URL is refused before any authorize runs, so
// that URL is never fetched. The document server counts every request; the
// same client through GET /authorize is fetched, so the count can rise.
func TestAFirstPartyAuthorizeNeverFetchesAClientTheBodyNames(t *testing.T) {
	documents := newCIMDDocumentServer(t)
	clientID := documents.URL + "/oauth/client.json"
	var fetched atomic.Int64
	document := chatGPTShapedDocument(clientID, nil)
	documents.serve(func(w http.ResponseWriter, _ *http.Request) {
		fetched.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(document))
	})
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{
		Issuer: oauthTestIssuer, Resources: []string{oauthTestResource, oauthTestIssuer}, DevBrowserRedirectURIs: []string{oauthTestRedirect},
		ClientMetadata: auth.NewClientMetadataFetcher(documents.Client()),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	form := firstPartyForm("state_case_cimd")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", cimdTestRedirect)
	if code := (firstPartyCall{subject: "user_cimd", form: form}).serve(t, app).Code; code != http.StatusBadRequest {
		t.Fatalf("a POST naming a metadata document client: status %d, want 400", code)
	}
	if n := fetched.Load(); n != 0 {
		t.Fatalf("the metadata document the body named was fetched %d times, want 0", n)
	}
	query := oauthTestAuthorizeQuery(clientID)
	query.Set("redirect_uri", cimdTestRedirect)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+query.Encode(), nil))
	if fetched.Load() == 0 {
		t.Fatalf("GET /authorize for the same client fetched nothing (status %d): the counter would prove nothing", recorder.Code)
	}
}
