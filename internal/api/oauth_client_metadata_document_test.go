package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

const (
	cimdTestRedirect     = "https://client.example.test/connector_oauth_redirect"
	cimdTestOriginOnly   = "https://mcp.example.test"
	cimdTestAssertion    = "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJjaW1kLXRlc3QifQ.c2lnbmF0dXJl"
	cimdTestJWTBearer    = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	cimdTestRefusalStart = `"client_refusal":"`
)

// cimdDocumentServer serves one client ID metadata document over TLS; the
// handler is swapped per case.
type cimdDocumentServer struct {
	*httptest.Server
	mu      sync.Mutex
	handler http.HandlerFunc
}

func newCIMDDocumentServer(t *testing.T) *cimdDocumentServer {
	t.Helper()
	s := &cimdDocumentServer{}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		handler := s.handler
		s.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *cimdDocumentServer) serve(handler http.HandlerFunc) {
	s.mu.Lock()
	s.handler = handler
	s.mu.Unlock()
}

func (s *cimdDocumentServer) serveJSON(body string) {
	s.serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
}

// chatGPTShapedDocument is the shape ChatGPT's connector publishes: it names
// private_key_jwt and lists none among the methods it supports.
func chatGPTShapedDocument(clientID string, overrides map[string]any) string {
	document := map[string]any{
		"client_id":                             clientID,
		"client_uri":                            "https://client.example.test/",
		"redirect_uris":                         []string{cimdTestRedirect},
		"token_endpoint_auth_method":            "private_key_jwt",
		"token_endpoint_auth_methods_supported": []string{"none", "private_key_jwt"},
		"grant_types":                           []string{"authorization_code", "refresh_token"},
		"response_types":                        []string{"code"},
		"client_name":                           "Connector",
		"token_endpoint_auth_signing_alg":       "RS256",
		"jwks_uri":                              "https://client.example.test/oauth/jwks.json",
	}
	for key, value := range overrides {
		if value == nil {
			delete(document, key)
			continue
		}
		document[key] = value
	}
	encoded, _ := json.Marshal(document)
	return string(encoded)
}

func cimdAuthorizeQuery(clientID, redirect string) url.Values {
	digest := sha256.Sum256([]byte(strings.Repeat("v", 50)))
	return url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"scope": {"context:read evidence:read data:read"}, "resource": {cimdTestOriginOnly},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"},
		"state": {"st"}, "ui_locales": {"en-US"},
	}
}

func oauthLogLine(logs, step string) map[string]any {
	for _, raw := range strings.Split(strings.TrimSpace(logs), "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(raw), &line) == nil && line["msg"] == "acr-api oauth step" && line["step"] == step {
			return line
		}
	}
	return nil
}

// TestOAuthMetadataDocumentClientListingNoneSignsIn: a client whose metadata
// document names private_key_jwt but lists none among its supported methods
// signs in end to end as a public PKCE client, with an origin-only resource,
// and a client assertion it sends to /token is logged by class and ignored.
func TestOAuthMetadataDocumentClientListingNoneSignsIn(t *testing.T) {
	documents := newCIMDDocumentServer(t)
	clientID := documents.URL + "/oauth/client.json"
	documents.serveJSON(chatGPTShapedDocument(clientID, nil))
	app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ClientMetadata: auth.NewClientMetadataFetcher(documents.Client())}, true)
	if err != nil {
		t.Fatal(err)
	}

	handle := authorizeToConsent(t, app, cimdAuthorizeQuery(clientID, cimdTestRedirect))
	line := oauthLogLine(logs.String(), "authorize")
	if line == nil || line["outcome"] != "ok" || line["client_kind"] != "metadata_document" || line["client_refusal"] != "none" {
		t.Fatalf("authorize line = %v", line)
	}

	all := []string{"*"}
	recorder, body := serveConsent(app, consentRequest(t, map[string]any{"action": "preview", "handle": handle}, all, "p1"))
	if recorder.Code != http.StatusOK || body["client_kind"] != "metadata_document" || body["resource"] != cimdTestOriginOnly {
		t.Fatalf("preview: %d %v", recorder.Code, body)
	}
	if line := oauthLogLine(logs.String(), "consent_preview"); line == nil || line["outcome"] != "ok" || line["client_refusal"] != "none" {
		t.Fatalf("consent_preview line = %v", line)
	}
	recorder, body = serveConsent(app, consentRequest(t, map[string]any{"action": "approve", "handle": handle, "repository_scopes": all}, all, "a1"))
	redirectURL, _ := body["redirect_url"].(string)
	redirect, err := url.Parse(redirectURL)
	if recorder.Code != http.StatusOK || err != nil || redirect.Scheme+"://"+redirect.Host+redirect.Path != cimdTestRedirect || redirect.Query().Get("code") == "" {
		t.Fatalf("approve: %d %v", recorder.Code, body)
	}

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {redirect.Query().Get("code")}, "redirect_uri": {cimdTestRedirect},
		"client_id": {clientID}, "code_verifier": {strings.Repeat("v", 50)}, "resource": {cimdTestOriginOnly},
		"client_assertion_type": {cimdTestJWTBearer}, "client_assertion": {cimdTestAssertion},
	}
	token := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
	token.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRecorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(tokenRecorder, token)
	if tokenRecorder.Code != http.StatusOK || !strings.Contains(tokenRecorder.Body.String(), `"access_token":"fcacr_`) {
		t.Fatalf("token: %d %s", tokenRecorder.Code, tokenRecorder.Body.String())
	}
	if want := `"msg":"oauth client assertion ignored","request_id":`; !strings.Contains(logs.String(), want) ||
		!strings.Contains(logs.String(), `"step":"token","client_kind":"metadata_document","assertion_type":"jwt_bearer"`) {
		t.Fatalf("client assertion line missing: %s", logs.String())
	}
	if strings.Contains(logs.String(), cimdTestAssertion) || strings.Contains(logs.String(), clientID) {
		t.Fatal("the logs carry the client assertion or the client ID")
	}
	if line := oauthLogLine(logs.String(), "token"); line == nil || line["outcome"] != "ok" || line["client_kind"] != "metadata_document" {
		t.Fatalf("token line = %v", line)
	}
}

// TestOAuthTokenLogsAClientAssertionOnlyWhenOneIsSent: the assertion line is
// written for a request that carries either assertion parameter, whatever the
// exchange's outcome, and never for one that carries neither.
func TestOAuthTokenLogsAClientAssertionOnlyWhenOneIsSent(t *testing.T) {
	for name, tc := range map[string]struct {
		extra url.Values
		want  string
	}{
		"none":                   {url.Values{}, ""},
		"jwt bearer":             {url.Values{"client_assertion_type": {cimdTestJWTBearer}, "client_assertion": {cimdTestAssertion}}, "jwt_bearer"},
		"assertion only":         {url.Values{"client_assertion": {cimdTestAssertion}}, "other"},
		"another assertion type": {url.Values{"client_assertion_type": {"urn:example:other"}}, "other"},
	} {
		t.Run(name, func(t *testing.T) {
			app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"grant_type": {"authorization_code"}, "code": {"unknown"}, "client_id": {"acrc_00000000000000000000000000000000"}}
			for key, values := range tc.extra {
				form[key] = values
			}
			request := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("token: %d %s", recorder.Code, recorder.Body.String())
			}
			if line := oauthLogLine(logs.String(), "token"); line == nil || line["outcome"] != "invalid_grant" || line["client_refusal"] != "none" {
				t.Fatalf("token line = %v, want invalid_grant with client_refusal none", line)
			}
			count := strings.Count(logs.String(), `"msg":"oauth client assertion ignored"`)
			if tc.want == "" {
				if count != 0 {
					t.Fatalf("assertion line written for a request without one: %s", logs.String())
				}
				return
			}
			if count != 1 || !strings.Contains(logs.String(), `"step":"token","client_kind":"none","assertion_type":"`+tc.want+`"`) {
				t.Fatalf("assertion lines = %d, want 1 with assertion_type %s: %s", count, tc.want, logs.String())
			}
			if strings.Contains(logs.String(), cimdTestAssertion) || strings.Contains(logs.String(), "urn:example:other") {
				t.Fatal("the assertion line carries a request value")
			}
		})
	}
}

// TestOAuthAuthorizeNamesTheClientRefusal: every client refusal on /authorize
// is a 400 page that names the class in plain words without echoing a value,
// and the authorize line carries the class.
func TestOAuthAuthorizeNamesTheClientRefusal(t *testing.T) {
	documents := newCIMDDocumentServer(t)
	clientID := documents.URL + "/oauth/client.json"
	port := documents.Listener.Addr().(*net.TCPAddr).Port
	for _, tc := range []struct {
		name     string
		document func() string
		handler  http.HandlerFunc
		fetcher  auth.OAuthClientMetadataFetcher
		clientID string
		redirect string
		refusal  string
		reason   string
	}{
		{name: "private_key_jwt only", document: func() string {
			return chatGPTShapedDocument(clientID, map[string]any{"token_endpoint_auth_methods_supported": []string{"private_key_jwt"}})
		}, refusal: "auth_method_unsupported", reason: "client authentication method this server does not support"},
		{name: "private_key_jwt without a list", document: func() string {
			return chatGPTShapedDocument(clientID, map[string]any{"token_endpoint_auth_methods_supported": nil})
		}, refusal: "auth_method_unsupported", reason: "client authentication method this server does not support"},
		{name: "document names another client", document: func() string {
			return chatGPTShapedDocument(clientID, map[string]any{"client_id": "https://evil.example.test/oauth/client.json"})
		}, refusal: "bad_client_id", reason: "names a different client ID"},
		{name: "no redirect uris", document: func() string {
			return chatGPTShapedDocument(clientID, map[string]any{"redirect_uris": nil})
		}, refusal: "invalid_redirect_uris", reason: "lists no return address this server accepts"},
		{name: "unusable redirect uri", document: func() string {
			return chatGPTShapedDocument(clientID, map[string]any{"redirect_uris": []string{cimdTestRedirect, "http://evil.example.test/cb"}})
		}, refusal: "invalid_redirect_uris", reason: "lists no return address this server accepts"},
		{name: "presented redirect not listed", document: func() string { return chatGPTShapedDocument(clientID, nil) },
			redirect: "https://client.example.test/elsewhere", refusal: "redirect_uri_mismatch", reason: "is not one the application registered"},
		{name: "too large", document: func() string {
			return chatGPTShapedDocument(clientID, map[string]any{"padding": strings.Repeat("x", 6<<10)})
		}, refusal: "too_large", reason: "is too large"},
		{name: "not json", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		}, refusal: "invalid_document", reason: "is not valid JSON"},
		{name: "not found", handler: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
			refusal: "fetch_failed", reason: "could not be retrieved"},
		{name: "redirected", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://client.example.test/oauth/client.json", http.StatusFound)
		}, refusal: "fetch_failed", reason: "could not be retrieved"},
		{name: "private address", document: func() string { return chatGPTShapedDocument(clientID, nil) },
			fetcher: auth.NewPublicClientMetadataFetcher(), clientID: fmt.Sprintf("https://127.0.0.1:%d/oauth/client.json", port),
			refusal: "private_address", reason: "on an address this server does not contact"},
		{name: "http client id", clientID: "http://client.example.test/oauth/client.json",
			refusal: "not_https", reason: "is not an https address"},
		{name: "root client id", clientID: "https://client.example.test/",
			refusal: "unsupported_client_id", reason: "is not a form this server accepts"},
		{name: "unknown registered client", clientID: "acrc_00000000000000000000000000000000",
			refusal: "unknown_client", reason: "is not registered with this server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switch {
			case tc.handler != nil:
				documents.serve(tc.handler)
			case tc.document != nil:
				documents.serveJSON(tc.document())
			default:
				documents.serve(func(w http.ResponseWriter, _ *http.Request) { t.Error("the document server was contacted") })
			}
			caseFetcher := tc.fetcher
			if caseFetcher == nil {
				caseFetcher = auth.NewClientMetadataFetcher(documents.Client())
			}
			app, logs, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}, ClientMetadata: caseFetcher}, true)
			if err != nil {
				t.Fatal(err)
			}
			id, redirect := clientID, cimdTestRedirect
			if tc.clientID != "" {
				id = tc.clientID
			}
			if tc.redirect != "" {
				redirect = tc.redirect
			}
			recorder := httptest.NewRecorder()
			app.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+"?"+cimdAuthorizeQuery(id, redirect).Encode(), nil))
			page := recorder.Body.String()
			if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Location") != "" {
				t.Fatalf("status %d Location %q, want a 400 page", recorder.Code, recorder.Header().Get("Location"))
			}
			if !strings.Contains(page, "Sign-in stopped") || !strings.Contains(page, tc.reason) {
				t.Fatalf("page does not name the refusal %q: %s", tc.reason, page)
			}
			for _, value := range []string{id, redirect, "client.example.test", "evil.example.test", "127.0.0.1", "private_key_jwt", tc.refusal} {
				if strings.Contains(page, value) {
					t.Fatalf("page echoes %q: %s", value, page)
				}
			}
			line := oauthLogLine(logs.String(), "authorize")
			if line == nil || line["outcome"] == "ok" || line["client_kind"] != "none" || line["client_refusal"] != tc.refusal || line["status"] != float64(http.StatusBadRequest) {
				t.Fatalf("authorize line = %v, want client_refusal %s", line, tc.refusal)
			}
			if strings.Count(logs.String(), cimdTestRefusalStart) != 1 {
				t.Fatalf("want exactly one line with a client refusal: %s", logs.String())
			}
		})
	}
}
