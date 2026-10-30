package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12086 (Ask Dev direct-browser design §5, T6): acr-api answers CORS
// for the exact web origin on the browser routes, before any route's auth,
// on every status; for no other origin and no other route; and never with a
// wildcard or credentials.

const corsTestOrigin = "https://www.fullchaos.dev"

type corsCall struct {
	method, path, origin, token string
	headers                     map[string]string
	body                        []byte
}

func (h *submissionHarness) cors(app int, call corsCall) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(call.method, call.path, bytes.NewReader(call.body))
	if call.origin != "" {
		request.Header.Set("Origin", call.origin)
	}
	if call.token != "" {
		request.Header.Set("Authorization", "Bearer "+call.token)
	}
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	for key, value := range call.headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

func corsHarness(t *testing.T) *submissionHarness {
	t.Helper()
	harness, _ := newMemorySubmissionHarness(t, 1)
	harness.apps[0].config.CORSAllowedOrigins = []string{corsTestOrigin, "http://localhost"}
	return harness
}

func createBody(t *testing.T) []byte {
	t.Helper()
	body := investigationRequestBody()
	body.Question = submissionQuestion
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func dataHeaders(extra map[string]string) map[string]string {
	headers := map[string]string{"X-ACR-Client-Version": "1.0.0", "Content-Type": "application/json"}
	for key, value := range extra {
		headers[key] = value
	}
	return headers
}

// expectCORSActual asserts the headers of an actual (non-preflight) response
// to a listed origin.
func expectCORSActual(t *testing.T, name string, recorder *httptest.ResponseRecorder, wantStatus int, origin string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("%s: status = %d, want %d (body %s)", name, recorder.Code, wantStatus, recorder.Body.String())
	}
	header := recorder.Header()
	if got := header.Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("%s: Access-Control-Allow-Origin = %q, want %q", name, got, origin)
	}
	if got := header.Get("Access-Control-Expose-Headers"); got != "Retry-After, X-Request-ID" {
		t.Fatalf("%s: Access-Control-Expose-Headers = %q", name, got)
	}
	if !containsValue(header.Values("Vary"), "Origin") {
		t.Fatalf("%s: Vary = %v, want Origin", name, header.Values("Vary"))
	}
	expectNoCredentialsOrWildcard(t, name, recorder)
}

func expectNoCORS(t *testing.T, name string, recorder *httptest.ResponseRecorder) {
	t.Helper()
	for _, key := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Access-Control-Max-Age"} {
		if got := recorder.Header().Get(key); got != "" {
			t.Fatalf("%s: %s = %q, want no CORS header", name, key, got)
		}
	}
	expectNoCredentialsOrWildcard(t, name, recorder)
}

func expectNoCredentialsOrWildcard(t *testing.T, name string, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("%s: Access-Control-Allow-Credentials = %q, must never be sent", name, got)
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") == "*" {
		t.Fatalf("%s: Access-Control-Allow-Origin is a wildcard", name)
	}
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.TrimSpace(part) == want {
				return true
			}
		}
	}
	return false
}

// A preflight from the listed origin is answered on every browser route,
// without a credential (it never meets auth), with the exact lists the
// design names.
func TestABrowserPreflightIsAnsweredBeforeAuthOnEveryBrowserRoute(t *testing.T) {
	harness := corsHarness(t)
	for _, path := range []string{
		OAuthAuthorizationServerMetadataPath,
		OAuthTokenPath,
		ContextFabricInvestigationsPath,
		ContextFabricInvestigationsPath + "/result_cors_00000001",
		ContextFabricInvestigationsPath + "/submissions/submission-cors-0001",
		"/api/v1/agent-context/evidence/ev_cors_0001",
	} {
		for _, origin := range []string{corsTestOrigin, "http://localhost"} {
			got := harness.cors(0, corsCall{method: http.MethodOptions, path: path, origin: origin, headers: map[string]string{
				"Access-Control-Request-Method":  "POST",
				"Access-Control-Request-Headers": "authorization, content-type, x-acr-client-version, x-acr-submission-id",
			}})
			if got.Code != http.StatusNoContent {
				t.Fatalf("%s from %s: preflight status = %d, want 204 (body %s)", path, origin, got.Code, got.Body.String())
			}
			want := map[string]string{
				"Access-Control-Allow-Origin":  origin,
				"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
				"Access-Control-Allow-Headers": "Authorization, Content-Type, X-ACR-Client-Version, X-ACR-Submission-ID, X-ACR-Thread-ID, X-ACR-Wait-Budget-Seconds, X-ACR-Resource, X-Request-ID",
				"Access-Control-Max-Age":       "600",
			}
			for key, value := range want {
				if g := got.Header().Get(key); g != value {
					t.Fatalf("%s from %s: %s = %q, want %q", path, origin, key, g, value)
				}
			}
			for _, vary := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
				if !containsValue(got.Header().Values("Vary"), vary) {
					t.Fatalf("%s: Vary %v lacks %s", path, got.Header().Values("Vary"), vary)
				}
			}
			expectNoCredentialsOrWildcard(t, path, got)
		}
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("a preflight ran %d investigations", got)
	}
}

// Every actual response to the listed origin carries the CORS headers,
// whatever its status, so the browser can read error bodies and Retry-After.
func TestEveryActualResponseToTheWebOriginCarriesCORS(t *testing.T) {
	harness := corsHarness(t)
	body := createBody(t)
	create := func(token, submissionID string, extra map[string]string, payload []byte) *httptest.ResponseRecorder {
		headers := dataHeaders(extra)
		if submissionID != "" {
			headers[contractsv1.ContextFabricSubmissionIDHeader] = submissionID
		}
		return harness.cors(0, corsCall{method: http.MethodPost, path: ContextFabricInvestigationsPath, origin: corsTestOrigin, token: token, headers: headers, body: payload})
	}
	// 200
	ok := create(harness.tokenA, "submission-cors-0001", nil, body)
	expectCORSActual(t, "200 create", ok, http.StatusOK, corsTestOrigin)
	// 404: an unknown result id
	expectCORSActual(t, "404 result", harness.cors(0, corsCall{method: http.MethodGet, path: ContextFabricInvestigationsPath + "/result_cors_missing01", origin: corsTestOrigin, token: harness.tokenA, headers: dataHeaders(nil)}), http.StatusNotFound, corsTestOrigin)
	// 401: no bearer; and a web cookie is no credential
	expectCORSActual(t, "401 no bearer", create("", "", nil, body), http.StatusUnauthorized, corsTestOrigin)
	expectCORSActual(t, "401 cookie only", create("", "", map[string]string{"Cookie": "__Secure-next-auth.session-token=a-valid-looking-session"}, body), http.StatusUnauthorized, corsTestOrigin)
	// 426: an unsupported client version
	expectCORSActual(t, "426 client version", create(harness.tokenA, "", map[string]string{"X-ACR-Client-Version": "0.0.1"}, body), http.StatusUpgradeRequired, corsTestOrigin)
	// 406: a stream request while the stream is off
	expectCORSActual(t, "406 stream off", create(harness.tokenA, "", map[string]string{"Accept": contractsv1.ContextFabricInvestigationEventStreamMediaType}, body), http.StatusNotAcceptable, corsTestOrigin)
	// 409: the same submission id for a different question
	changed := investigationRequestBody()
	changed.Question = submissionQuestion + " And the drivers?"
	changedBody, _ := json.Marshal(changed)
	expectCORSActual(t, "409 mismatch", create(harness.tokenA, "submission-cors-0001", nil, changedBody), http.StatusConflict, corsTestOrigin)
	// 403: the deployment's model is not entitled for the organization
	harness.denied.Store("acr_service_model", true)
	harness.denied.Store("platform_model_fallback", true)
	expectCORSActual(t, "403 model", create(harness.tokenA, "submission-cors-0002", nil, body), http.StatusForbidden, corsTestOrigin)
	harness.denied.Delete("acr_service_model")
	harness.denied.Delete("platform_model_fallback")
	// 503: ClickHouse not ready
	harness.clickhouseDown.Store(true)
	expectCORSActual(t, "503 store", create(harness.tokenA, "submission-cors-0003", nil, body), http.StatusServiceUnavailable, corsTestOrigin)
	harness.clickhouseDown.Store(false)
	// 429 with an exposed Retry-After: the allowance is used up
	harness.apps[0].config.AskDevLimits = contextfabric.SubmissionLimits{MonthlyPlatformRunAllowance: 1}
	refused := create(harness.tokenA, "submission-cors-0004", nil, body)
	expectCORSActual(t, "429 allowance", refused, http.StatusTooManyRequests, corsTestOrigin)
	if refused.Header().Get("Retry-After") == "" {
		t.Fatal("429: no Retry-After to expose")
	}
}

// An origin not on the list, a request with no Origin, and a non-browser
// route get no CORS header; an empty allow list answers CORS for nobody.
func TestCORSIsAnsweredForTheListedOriginOnTheBrowserRoutesOnly(t *testing.T) {
	harness := corsHarness(t)
	body := createBody(t)
	preflight := map[string]string{"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization"}
	for _, origin := range []string{"https://evil.example", "https://www.fullchaos.dev.evil.example", "http://www.fullchaos.dev", "null", "https://WWW.fullchaos.dev"} {
		got := harness.cors(0, corsCall{method: http.MethodOptions, path: ContextFabricInvestigationsPath, origin: origin, headers: preflight})
		if got.Code == http.StatusNoContent {
			t.Fatalf("%s: preflight answered 204", origin)
		}
		expectNoCORS(t, origin+" preflight", got)
		expectNoCORS(t, origin+" actual", harness.cors(0, corsCall{method: http.MethodPost, path: ContextFabricInvestigationsPath, origin: origin, token: harness.tokenA, headers: dataHeaders(nil), body: body}))
	}
	if !strings.Contains(harness.logs[0].String(), `"msg":"`+CORSRefusedLogMessage+`"`) || !strings.Contains(harness.logs[0].String(), `"route":"investigations"`) {
		t.Fatalf("a refused origin is not logged:\n%s", harness.logs[0].String())
	}
	// No Origin: a non-browser client is untouched.
	expectNoCORS(t, "no origin", harness.cors(0, corsCall{method: http.MethodPost, path: ContextFabricInvestigationsPath, token: harness.tokenA, headers: dataHeaders(nil), body: body}))
	// Two Origin headers are not an origin.
	twice := httptest.NewRequest(http.MethodOptions, ContextFabricInvestigationsPath, nil)
	twice.Header.Add("Origin", corsTestOrigin)
	twice.Header.Add("Origin", "https://evil.example")
	twice.Header.Set("Access-Control-Request-Method", "POST")
	recorder := httptest.NewRecorder()
	harness.apps[0].Handler().ServeHTTP(recorder, twice)
	expectNoCORS(t, "two origins", recorder)
	// Not a browser route.
	for _, path := range []string{OAuthAuthorizePath, OAuthConsentPath, ContextFabricDataCatalogPath, "/api/v1/agent-context/capabilities", "/healthz"} {
		got := harness.cors(0, corsCall{method: http.MethodOptions, path: path, origin: corsTestOrigin, headers: preflight})
		expectNoCORS(t, path+" preflight", got)
		if containsValue(got.Header().Values("Vary"), "Origin") {
			t.Fatalf("%s: Vary: Origin on a route that never answers CORS", path)
		}
	}
	// An empty allow list.
	harness.apps[0].config.CORSAllowedOrigins = nil
	expectNoCORS(t, "empty allow list", harness.cors(0, corsCall{method: http.MethodOptions, path: ContextFabricInvestigationsPath, origin: corsTestOrigin, headers: preflight}))
	// The server still runs a refused origin's request (CORS is the
	// browser's read gate, not authorization): five refused origins plus
	// the request with no Origin.
	if got := harness.investigator.executions.Load(); got != 6 {
		t.Fatalf("executions = %d, want 6", got)
	}
}

// codex #1770 r1 P3: every CORS decision is on the request's Info access
// line, an allowed grant as well as a refusal, so a regression in an allowed
// origin's headers is not indistinguishable from an ordinary response.
func TestEveryCORSDecisionIsOnTheAccessLog(t *testing.T) {
	harness := corsHarness(t)
	logs := harness.logs[0]
	logs.Reset()
	preflight := map[string]string{"Access-Control-Request-Method": "POST"}
	for _, tc := range []struct {
		name string
		call corsCall
		want map[string]any
	}{
		{"allowed preflight", corsCall{method: http.MethodOptions, path: ContextFabricInvestigationsPath, origin: corsTestOrigin, headers: preflight},
			map[string]any{"cors": "allowed", "cors_route": "investigations", "cors_preflight": true, "cors_origin": corsTestOrigin}},
		{"allowed actual", corsCall{method: http.MethodGet, path: OAuthAuthorizationServerMetadataPath, origin: corsTestOrigin},
			map[string]any{"cors": "allowed", "cors_route": "oauth_metadata", "cors_preflight": false, "cors_origin": corsTestOrigin}},
		{"refused", corsCall{method: http.MethodGet, path: OAuthAuthorizationServerMetadataPath, origin: "https://evil.example"},
			map[string]any{"cors": "refused", "cors_route": "oauth_metadata", "cors_preflight": false, "cors_origin": nil}},
		{"no origin", corsCall{method: http.MethodGet, path: OAuthAuthorizationServerMetadataPath},
			map[string]any{"cors": nil, "cors_route": nil, "cors_preflight": nil, "cors_origin": nil}},
		{"not a browser route", corsCall{method: http.MethodGet, path: "/healthz", origin: corsTestOrigin},
			map[string]any{"cors": nil, "cors_route": nil, "cors_preflight": nil, "cors_origin": nil}},
	} {
		harness.cors(0, tc.call)
		line := lastAccessLine(t, logs)
		for key, want := range tc.want {
			if got := line[key]; got != want {
				t.Errorf("%s: access line %s = %v, want %v\n%v", tc.name, key, got, want, line)
			}
		}
	}
}

func lastAccessLine(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "request completed" {
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("no request completed line in:\n%s", logs.String())
	}
	logs.Reset()
	return found
}
