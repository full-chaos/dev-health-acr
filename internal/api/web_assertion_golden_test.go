package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const (
	goldenIssuer   = "https://web.example.test"
	goldenAudience = "acr-api"
	goldenPacket   = "/api/v1/agent-context/context-packets"
)

type goldenRoute struct {
	Method       string `json:"method"`
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	Scope        string `json:"scope"`
	WebAssertion bool   `json:"web_assertion"`
}

type goldenFixture struct {
	HeaderKeys []string      `json:"header_keys"`
	ClaimKeys  []string      `json:"claim_keys"`
	Routes     []goldenRoute `json:"routes"`
}

func loadGolden(t *testing.T) goldenFixture {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "internal", "api", "testdata", "web_assertion_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture goldenFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

type goldenHarness struct {
	t       *testing.T
	now     time.Time
	valid   ed25519.PrivateKey
	foreign ed25519.PrivateKey
	app     *App
	jti     int
}

func newGoldenHarness(t *testing.T) *goldenHarness {
	t.Helper()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	public, valid, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, foreign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: goldenIssuer, Audience: goldenAudience, JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	entitled := EntitlementFunc(func(_ context.Context, org, _ string) (bool, error) { return org == "org_1" || org == "org_2", nil })
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, entitled, nil, verifier)
	return &goldenHarness{t: t, now: now, valid: valid, foreign: foreign, app: app}
}

func (h *goldenHarness) claims(method, path string, body []byte, permissions []string) map[string]any {
	h.jti++
	digest := sha256.Sum256(body)
	return map[string]any{
		"iss": goldenIssuer, "aud": goldenAudience, "sub": "user_123", "org_id": "org_1",
		"repository_scopes": []string{hostedTestRepository}, "permissions": permissions,
		"iat": h.now.Unix(), "nbf": h.now.Unix(), "exp": h.now.Add(30 * time.Second).Unix(), "jti": fmt.Sprintf("golden_%d", h.jti),
		"method": method, "path": path, "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
}

func assertGoldenUniform401(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Message != "Missing or invalid ACR credential" || envelope.Error.Retryable || envelope.Error.Details != nil {
		t.Fatalf("refusal is not the uniform one: %#v", envelope.Error)
	}
}

func goldenHeader() map[string]any {
	return map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": "current"}
}

func goldenSign(t *testing.T, key ed25519.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func (h *goldenHarness) do(request *http.Request) *httptest.ResponseRecorder {
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

func goldenPacketBody(t *testing.T, repository string) []byte {
	t.Helper()
	body, err := json.Marshal(contextRequestFor(repository))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func goldenRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestWebAssertionGolden_positiveControls(t *testing.T) {
	h := newGoldenHarness(t)
	fixture := loadGolden(t)
	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	body := goldenPacketBody(t, hostedTestRepository)
	claims := h.claims(http.MethodPost, goldenPacket, body, []string{auth.ScopeContextRead})
	if !slices.Equal(keys(claims), fixture.ClaimKeys) || !slices.Equal(keys(goldenHeader()), fixture.HeaderKeys) {
		t.Fatalf("assertion shape drifted from the copied web shape: %v %v", keys(claims), keys(goldenHeader()))
	}
	request := goldenRequest(http.MethodPost, goldenPacket, body)
	request.Header.Set(auth.WebAssertionHeader, goldenSign(t, h.valid, goldenHeader(), claims))
	if response := h.do(request); response.Code != http.StatusOK {
		t.Fatalf("control status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestWebAssertionGolden_refusesEachWrongField(t *testing.T) {
	h := newGoldenHarness(t)
	body := goldenPacketBody(t, hostedTestRepository)
	rows := []struct {
		name   string
		claims func(map[string]any)
		header func(map[string]any)
		send   func(*http.Request) *http.Request
	}{
		{name: "issuer", claims: func(c map[string]any) { c["iss"] = "https://other.example.test" }},
		{name: "issuer empty", claims: func(c map[string]any) { c["iss"] = "" }},
		{name: "aud", claims: func(c map[string]any) { c["aud"] = "other-audience" }},
		{name: "aud missing", claims: func(c map[string]any) { delete(c, "aud") }},
		{name: "alg HS256", header: func(m map[string]any) { m["alg"] = "HS256" }},
		{name: "alg none", header: func(m map[string]any) { m["alg"] = "none" }},
		{name: "alg RS256", header: func(m map[string]any) { m["alg"] = "RS256" }},
		{name: "typ", header: func(m map[string]any) { m["typ"] = "at+jwt" }},
		{name: "kid unknown", header: func(m map[string]any) { m["kid"] = "removed" }},
		{name: "kid missing", header: func(m map[string]any) { delete(m, "kid") }},
		{name: "signature foreign key"},
		{name: "time expired", claims: func(c map[string]any) { c["exp"] = h.now.Add(-6 * time.Second).Unix() }},
		{name: "time nbf future", claims: func(c map[string]any) { c["nbf"] = h.now.Add(6 * time.Second).Unix() }},
		{name: "time iat future", claims: func(c map[string]any) { c["iat"] = h.now.Add(6 * time.Second).Unix() }},
		{name: "time overlong", claims: func(c map[string]any) { c["exp"] = h.now.Add(31 * time.Second).Unix() }},
		{name: "time missing", claims: func(c map[string]any) { delete(c, "exp") }},
		{name: "method", claims: func(c map[string]any) { c["method"] = http.MethodGet }},
		{name: "path", claims: func(c map[string]any) { c["path"] = "/api/v1/agent-context/capabilities" }},
		{name: "path query", send: func(r *http.Request) *http.Request { r.URL.RawQuery = "x=1"; return r }},
		{name: "body digest", claims: func(c map[string]any) { c["body_sha256"] = "AAAA" }},
		{name: "body swapped", send: func(r *http.Request) *http.Request {
			r.Body = nopBody(goldenPacketBody(t, "example-org/other-service"))
			return r
		}},
		{name: "subject empty", claims: func(c map[string]any) { c["sub"] = "" }},
		{name: "subject missing", claims: func(c map[string]any) { delete(c, "sub") }},
		{name: "jti empty", claims: func(c map[string]any) { c["jti"] = "" }},
		{name: "jti missing", claims: func(c map[string]any) { delete(c, "jti") }},
		{name: "iat missing", claims: func(c map[string]any) { delete(c, "iat") }},
		{name: "iat not a number", claims: func(c map[string]any) { c["iat"] = "now" }},
		{name: "nbf missing", claims: func(c map[string]any) { delete(c, "nbf") }},
		{name: "nbf not a number", claims: func(c map[string]any) { c["nbf"] = "now" }},
		{name: "org empty", claims: func(c map[string]any) { c["org_id"] = "" }},
		{name: "org too long", claims: func(c map[string]any) { c["org_id"] = string(bytes.Repeat([]byte("o"), 257)) }},
		{name: "repo empty", claims: func(c map[string]any) { c["repository_scopes"] = []string{} }},
		{name: "repo global wildcard", claims: func(c map[string]any) { c["repository_scopes"] = []string{"*"} }},
		{name: "repo owner wildcard", claims: func(c map[string]any) { c["repository_scopes"] = []string{"example-org/*"} }},
		{name: "repo duplicate", claims: func(c map[string]any) { c["repository_scopes"] = []string{hostedTestRepository, hostedTestRepository} }},
		{name: "repo not normalized", claims: func(c map[string]any) { c["repository_scopes"] = []string{"Example-Org/Widget-Service"} }},
		{name: "permission empty", claims: func(c map[string]any) { c["permissions"] = []string{} }},
		{name: "permission unknown", claims: func(c map[string]any) { c["permissions"] = []string{"admin:all"} }},
		{name: "permission write", claims: func(c map[string]any) { c["permissions"] = []string{auth.ScopeEpisodeWrite} }},
		{name: "permission duplicate", claims: func(c map[string]any) {
			c["permissions"] = []string{auth.ScopeContextRead, auth.ScopeContextRead}
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			claims := h.claims(http.MethodPost, goldenPacket, body, []string{auth.ScopeContextRead})
			header := goldenHeader()
			if row.claims != nil {
				row.claims(claims)
			}
			if row.header != nil {
				row.header(header)
			}
			key := h.valid
			if row.name == "signature foreign key" {
				key = h.foreign
			}
			request := goldenRequest(http.MethodPost, goldenPacket, body)
			request.Header.Set(auth.WebAssertionHeader, goldenSign(t, key, header, claims))
			if row.send != nil {
				request = row.send(request)
			}
			assertGoldenUniform401(t, h.do(request))
		})
	}
}

type nopBodyReader struct{ *bytes.Reader }

func (nopBodyReader) Close() error { return nil }

func nopBody(b []byte) nopBodyReader { return nopBodyReader{bytes.NewReader(b)} }

func (h *goldenHarness) signed(method, path string, body []byte, mutate func(map[string]any)) *http.Request {
	claims := h.claims(method, path, body, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead})
	if mutate != nil {
		mutate(claims)
	}
	request := goldenRequest(method, path, body)
	request.Header.Set(auth.WebAssertionHeader, goldenSign(h.t, h.valid, goldenHeader(), claims))
	return request
}

func TestWebAssertionGolden_authorizationOnValidCredential(t *testing.T) {
	h := newGoldenHarness(t)
	packet := goldenPacketBody(t, hostedTestRepository)
	evidence := "/api/v1/agent-context/evidence/evidence-ref-00000001"
	rows := []struct {
		name    string
		request *http.Request
		status  int
		code    string
	}{
		{"org unknown", h.signed(http.MethodPost, goldenPacket, packet, func(c map[string]any) { c["org_id"] = "org_unknown" }), http.StatusForbidden, "feature_not_enabled"},
		{"repo foreign", h.signed(http.MethodPost, goldenPacket, goldenPacketBody(t, "other-org/other-repo"), nil), http.StatusForbidden, "repo_forbidden"},
		{"permission evidence only on context route", h.signed(http.MethodPost, goldenPacket, packet, func(c map[string]any) { c["permissions"] = []string{auth.ScopeEvidenceRead} }), http.StatusForbidden, "insufficient_scope"},
		{"permission context only on evidence route", h.signed(http.MethodGet, evidence, nil, func(c map[string]any) { c["permissions"] = []string{auth.ScopeContextRead} }), http.StatusForbidden, "insufficient_scope"},
		{"permission credential issue on read route", h.signed(http.MethodPost, goldenPacket, packet, func(c map[string]any) {
			c["permissions"] = []string{auth.WebAssertionPermissionCredentialIssue}
		}), http.StatusForbidden, "insufficient_scope"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			assertErrorResponse(t, h.do(row.request), row.status, row.code)
		})
	}
}

func TestWebAssertionGolden_refusesUserAndWorkloadTokens(t *testing.T) {
	h := newGoldenHarness(t)
	body := goldenPacketBody(t, hostedTestRepository)
	userClaims := map[string]any{"iss": goldenIssuer, "aud": goldenAudience, "sub": "user_123", "org_id": "org_1", "iat": h.now.Unix(), "exp": h.now.Add(time.Hour).Unix(), "jti": "user_session_1"}
	userByValid := goldenSign(t, h.valid, goldenHeader(), userClaims)
	userByForeign := goldenSign(t, h.foreign, goldenHeader(), userClaims)
	workload := goldenSign(t, h.foreign, map[string]any{"alg": "RS256", "typ": "JWT", "kid": "workload"}, map[string]any{
		"iss": "https://kubernetes.default.svc", "aud": []string{"acr-workload"}, "sub": "system:serviceaccount:ns:consumer", "exp": h.now.Add(time.Hour).Unix(),
	})
	workloadClaims := map[string]any{
		"iss": goldenIssuer, "aud": goldenAudience, "sub": "prn_" + strings.Repeat("A", 22), "principal_type": "workload", "jti": "000000000000000000000001",
		"iat": h.now.Unix(), "nbf": h.now.Unix(), "exp": h.now.Add(300 * time.Second).Unix(),
	}
	workloadHeader := map[string]any{"alg": "EdDSA", "typ": "workload+jwt", "kid": "current"}
	workloadByValid := goldenSign(t, h.valid, workloadHeader, workloadClaims)
	workloadByForeign := goldenSign(t, h.foreign, workloadHeader, workloadClaims)
	valid := h.signed(http.MethodPost, goldenPacket, body, nil).Header.Get(auth.WebAssertionHeader)
	rows := []struct {
		name   string
		header string
		bearer string
	}{
		{name: "user token as assertion, valid key", header: userByValid},
		{name: "user token as assertion, foreign key", header: userByForeign},
		{name: "user token as bearer", bearer: userByValid},
		{name: "mismatched-alg workload token as assertion", header: workload},
		{name: "workload-token.v1 as assertion, valid key", header: workloadByValid},
		{name: "workload-token.v1 as assertion, foreign key", header: workloadByForeign},
		{name: "workload-token.v1 as bearer", bearer: workloadByValid},
		{name: "workload-token.v1 bearer beside valid assertion", header: valid, bearer: workloadByValid},
		{name: "workload token as bearer", bearer: workload},
		{name: "workload bearer beside valid assertion", header: valid, bearer: workload},
		{name: "user bearer beside valid assertion", header: valid, bearer: userByValid},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			request := goldenRequest(http.MethodPost, goldenPacket, body)
			if row.header != "" {
				request.Header.Set(auth.WebAssertionHeader, row.header)
			}
			if row.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+row.bearer)
			}
			assertGoldenUniform401(t, h.do(request))
		})
	}
}

type goldenInvestigator struct{}

func (goldenInvestigator) Investigate(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	return contextfabric.InvestigationResult{}, errors.New("golden investigator: auth must refuse before this runs")
}

func newGoldenOAuthHarness(t *testing.T) *goldenHarness {
	t.Helper()
	app, _, err := newOAuthTestApp(t, &OAuthRuntime{Issuer: oauthTestIssuer, Resources: []string{oauthTestResource}}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, foreign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &goldenHarness{t: t, now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC), valid: oauthTestWebKey, foreign: foreign, app: app}
}

func TestWebAssertionGolden_routeToPermissionPin(t *testing.T) {
	hosted := newGoldenHarness(t)
	hosted.app.runtime.Investigator = goldenInvestigator{}
	oauth := newGoldenOAuthHarness(t)
	fixture := loadGolden(t)
	profiles, err := os.ReadFile(repopath.Path(t, "contracts", "auth", "v1", "endpoint-profiles.acr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Rows []struct {
			Method         string   `json:"method"`
			Route          string   `json:"route"`
			Classification string   `json:"classification"`
			Classes        []string `json:"accepted_credential_classes"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(profiles, &profile); err != nil {
		t.Fatal(err)
	}
	// Every protected acr route is in the golden, and its web-assertion flag is the profile's.
	want := map[string]bool{}
	for _, row := range profile.Rows {
		if row.Classification == "protected" {
			want[row.Method+" "+row.Route] = slices.Contains(row.Classes, "acr_web_assertion")
		}
	}
	got := map[string]bool{}
	for _, route := range fixture.Routes {
		got[route.Method+" "+route.Pattern] = route.WebAssertion
	}
	if !maps.Equal(want, got) {
		t.Fatalf("golden routes differ from the endpoint profile\nprofile: %v\ngolden:  %v", want, got)
	}
	for _, route := range fixture.Routes {
		t.Run(route.Method+" "+route.Pattern, func(t *testing.T) {
			h := hosted
			if route.Scope == auth.WebAssertionPermissionCredentialIssue || route.Scope == "" {
				h = oauth
			}
			var body []byte
			if route.Method == http.MethodPost {
				body = goldenPacketBody(t, hostedTestRepository)
			}
			grant := func(permission string) *http.Request {
				return h.signed(route.Method, route.Path, body, func(c map[string]any) {
					c["permissions"] = []string{permission}
					if permission == auth.WebAssertionPermissionCredentialIssue {
						c["repository_scopes"] = []string{"*"}
					}
				})
			}
			if !route.WebAssertion {
				assertGoldenUniform401(t, h.do(grant(auth.ScopeContextRead)))
				return
			}
			other := auth.ScopeContextRead
			if route.Scope == auth.ScopeContextRead {
				other = auth.ScopeEvidenceRead
			}
			assertErrorResponse(t, h.do(grant(other)), http.StatusForbidden, "insufficient_scope")
			response := h.do(grant(route.Scope))
			if response.Code == http.StatusUnauthorized || response.Code == http.StatusForbidden {
				t.Fatalf("scope %s refused on %s: %d %s", route.Scope, route.Path, response.Code, response.Body.String())
			}
		})
	}
}

// goldenVocabulary matches schema words and enum values shared by every packet; everything else in an org_1 packet is org_1 data.
var goldenVocabulary = regexp.MustCompile(`^[a-z][a-z_]*(\.v[0-9]+)?$`)

func goldenStringLeaves(value any, out *[]string) {
	switch v := value.(type) {
	case string:
		if len(v) >= 8 && !goldenVocabulary.MatchString(v) {
			*out = append(*out, v)
		}
	case []any:
		for _, item := range v {
			goldenStringLeaves(item, out)
		}
	case map[string]any:
		for _, item := range v {
			goldenStringLeaves(item, out)
		}
	}
}

// known gap: CHAOS-11278 (refuse instead of degraded)
func TestWebAssertionGolden_ForeignOrgRepoScopeReturnsNoForeignData(t *testing.T) {
	h := newGoldenHarness(t)
	packet := goldenPacketBody(t, hostedTestRepository)
	own := h.do(h.signed(http.MethodPost, goldenPacket, packet, nil))
	if own.Code != http.StatusOK {
		t.Fatalf("owner status = %d", own.Code)
	}
	var ownPacket map[string]any
	if err := json.Unmarshal(own.Body.Bytes(), &ownPacket); err != nil {
		t.Fatal(err)
	}
	items, _ := ownPacket["items"].([]any)
	if len(items) == 0 {
		t.Fatal("owner control returned no items; the scan below would prove nothing")
	}
	foreignValues := []string{"org_1", "fixture:"}
	identifying := len(foreignValues)
	goldenStringLeaves(items, &foreignValues)
	goldenStringLeaves(ownPacket["resolved_scope"], &foreignValues)
	foreignValues = slices.DeleteFunc(foreignValues, func(value string) bool { return bytes.Contains(packet, []byte(value)) })
	if len(foreignValues) <= identifying {
		t.Fatal("no org_1 values collected; the scan would prove nothing")
	}

	response := h.do(h.signed(http.MethodPost, goldenPacket, packet, func(c map[string]any) { c["org_id"] = "org_2" }))
	for _, value := range foreignValues {
		if bytes.Contains(response.Body.Bytes(), []byte(value)) {
			t.Fatalf("foreign-org response (status %d) contains an org_1 value %q", response.Code, value)
		}
	}
	if response.Code >= 500 {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Code != http.StatusOK {
		return
	}
	var got contractsv1.ContextPacket
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status == contractsv1.PacketComplete || len(got.Items) != 0 || strings.Contains(got.Repository.RepoID, "fixture:") || !strings.HasPrefix(got.Repository.RepoID, "unresolved:") {
		t.Fatalf("foreign-org packet carries data: status=%q items=%d", got.Status, len(got.Items))
	}
}
