package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/full-chaos/dev-health-go/authverify"
)

// CHAOS-12094 (F1): an MCP-bound bearer is admitted on acr-api only on a call
// acr-mcp forwards, proven by its projected ServiceAccount token.

const (
	hopTestIssuer      = "https://acr.example.test"
	hopTestMCPResource = "https://mcp.example.test/mcp"
	hopTestNamespace   = "acr"
	hopTestSA          = "acr-mcp"
	hopTestToken       = "hop.valid.token"
)

// fakeHopValidator stands in for TokenReview: tokens map to identities, an
// unknown token is invalid, and err (when set) is an outage.
type fakeHopValidator struct {
	identities map[string]authverify.SubjectIdentity
	err        error
	calls      atomic.Int64
}

func (f *fakeHopValidator) Validate(_ context.Context, token string) (authverify.SubjectIdentity, error) {
	f.calls.Add(1)
	if f.err != nil {
		return authverify.SubjectIdentity{}, f.err
	}
	identity, ok := f.identities[token]
	if !ok {
		return authverify.SubjectIdentity{}, authverify.ErrSubjectTokenInvalid
	}
	return identity, nil
}

type hopFixture struct {
	handler   http.Handler
	reached   *atomic.Int64
	clock     *time.Time
	validator *fakeHopValidator
	logs      *bytes.Buffer
	tokens    map[string]string // resource -> bearer
}

func newHopFixture(t *testing.T, policyOn bool) hopFixture {
	t.Helper()
	now := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	clock := now
	audit := memory.NewAuditStore()
	store := newMemoryCredentialStoreAt(t, now.Add(-time.Minute), audit)
	service, err := NewService(store, ServiceOptions{Now: func() time.Time { return now.Add(-time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, resource := range []string{"", hopTestMCPResource, hopTestIssuer} {
		request := CreateCredentialRequest{OrgID: "org_1", Name: "hop", RepositoryScopes: []string{"owner/repo"}, Scopes: []string{ScopeContextRead}, CreatedBy: "test_actor"}
		if resource != "" {
			request.Resource = resource // the rule keys on the bound resource, whatever the class
		}
		issued, err := service.Create(context.Background(), request)
		if err != nil {
			t.Fatalf("create credential bound to %q: %v", resource, err)
		}
		tokens[resource] = issued.Token
	}
	validator := &fakeHopValidator{identities: map[string]authverify.SubjectIdentity{
		hopTestToken:        {Namespace: hopTestNamespace, ServiceAccountName: hopTestSA, ExpiresAt: now.Add(10 * time.Minute)},
		"hop.other.account": {Namespace: hopTestNamespace, ServiceAccountName: "acr-api", ExpiresAt: now.Add(10 * time.Minute)},
		"hop.other.ns":      {Namespace: "default", ServiceAccountName: hopTestSA, ExpiresAt: now.Add(10 * time.Minute)},
		"hop.expired":       {Namespace: hopTestNamespace, ServiceAccountName: hopTestSA, ExpiresAt: now.Add(-time.Second)},
	}}
	var policy *MCPHopPolicy
	if policyOn {
		policy, err = NewMCPHopPolicy(MCPHopPolicyOptions{
			Validator: validator, Namespace: hopTestNamespace, ServiceAccount: hopTestSA, Issuer: hopTestIssuer,
			Now: func() time.Time { return clock },
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	authenticator, err := NewAuthenticator(store, audit, AuthenticatorOptions{
		Now: func() time.Time { return clock }, Limiter: NewMemoryLimiter(time.Minute, 1000, 1000),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)), MCPHop: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	reached := &atomic.Int64{}
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	return hopFixture{handler: handler, reached: reached, clock: &clock, validator: validator, logs: &logs, tokens: tokens}
}

func (f hopFixture) call(bearer, resource string, hops ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/context-fabric/investigations", nil)
	request.RemoteAddr = "192.0.2.20:4000"
	request.Header.Set("Authorization", "Bearer "+bearer)
	if resource != "" {
		request.Header.Set(ResourceHeader, resource)
	}
	for _, hop := range hops {
		request.Header.Add(MCPHopHeader, hop)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

// Red on main: with no hop policy (today's acr-api) a caller who holds an
// MCP-bound bearer and sets the MCP resource header itself is admitted.
func TestMCPBoundBearerWithoutAHopIsAdmittedWhenTheHopIsNotRequired(t *testing.T) {
	f := newHopFixture(t, false)
	if got := f.call(f.tokens[hopTestMCPResource], hopTestMCPResource).Code; got != http.StatusOK {
		t.Fatalf("MCP-bound bearer, caller-set resource, hop not required = %d, want 200 (today's behavior)", got)
	}
}

func TestMCPBoundBearerNeedsTheHopWhenRequired(t *testing.T) {
	f := newHopFixture(t, true)
	mcp := f.tokens[hopTestMCPResource]
	// No hop, an unknown token, two hops, another ServiceAccount, another
	// namespace, an expired token: each is the same existence-neutral 401.
	unknown := f.call("fcacr_"+strings.Repeat("A", 43), hopTestMCPResource)
	for name, hops := range map[string][]string{
		"no hop":                 nil,
		"blank hop":              {" "},
		"unknown token":          {"hop.unknown"},
		"two hops":               {hopTestToken, hopTestToken},
		"another serviceaccount": {"hop.other.account"},
		"another namespace":      {"hop.other.ns"},
		"expired token":          {"hop.expired"},
	} {
		response := f.call(mcp, hopTestMCPResource, hops...)
		if response.Code != http.StatusUnauthorized || response.Body.String() != unknown.Body.String() {
			t.Fatalf("%s: %d %s, want the unknown-credential 401 %s", name, response.Code, response.Body.String(), unknown.Body.String())
		}
	}
	if f.reached.Load() != 0 {
		t.Fatalf("a refused hop reached the handler %d time(s)", f.reached.Load())
	}

	if got := f.call(mcp, hopTestMCPResource, hopTestToken).Code; got != http.StatusOK {
		t.Fatalf("MCP-bound bearer with a valid hop = %d, want 200", got)
	}
	// The browser class (bound to acr-api's own resource) and an unbound
	// credential need no hop.
	if got := f.call(f.tokens[hopTestIssuer], hopTestIssuer).Code; got != http.StatusOK {
		t.Fatalf("issuer-bound bearer without a hop = %d, want 200", got)
	}
	if got := f.call(f.tokens[""], "").Code; got != http.StatusOK {
		t.Fatalf("unbound bearer without a hop = %d, want 200", got)
	}
	// The resource check still runs first: a hop does not admit a bearer on
	// another resource.
	if got := f.call(mcp, hopTestIssuer, hopTestToken).Code; got != http.StatusUnauthorized {
		t.Fatalf("MCP-bound bearer on the issuer resource with a hop = %d, want 401", got)
	}
}

// A verified hop is cached for no longer than its own life.
func TestMCPHopVerificationIsCachedForTheTokenLifeOnly(t *testing.T) {
	f := newHopFixture(t, true)
	mcp := f.tokens[hopTestMCPResource]
	for range 5 {
		if got := f.call(mcp, hopTestMCPResource, hopTestToken).Code; got != http.StatusOK {
			t.Fatalf("valid hop = %d", got)
		}
	}
	if got := f.validator.calls.Load(); got != 1 {
		t.Fatalf("TokenReview calls for 5 requests with one live token = %d, want 1", got)
	}
	*f.clock = f.clock.Add(10 * time.Minute) // the token's expiry
	if got := f.call(mcp, hopTestMCPResource, hopTestToken).Code; got != http.StatusUnauthorized {
		t.Fatalf("hop at its expiry = %d, want 401", got)
	}
	if got := f.validator.calls.Load(); got != 2 {
		t.Fatalf("an expired cache entry was used: TokenReview calls = %d, want 2", got)
	}
}

// A hop check that cannot run fails closed as unavailable, never as the
// caller's fault, and says why at Error.
func TestMCPHopValidatorOutageIsUnavailable(t *testing.T) {
	f := newHopFixture(t, true)
	f.validator.err = errors.New("kubernetes token review: request failed: dial tcp: connection refused")
	response := f.call(f.tokens[hopTestMCPResource], hopTestMCPResource, hopTestToken)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "upstream_unavailable") {
		t.Fatalf("validator outage = %d %s, want 503 upstream_unavailable", response.Code, response.Body.String())
	}
	if !strings.Contains(f.logs.String(), `msg="mcp hop check failed"`) || !strings.Contains(f.logs.String(), "failure_class=mcp_hop_validator") {
		t.Fatalf("outage not logged: %s", f.logs.String())
	}
	if strings.Contains(f.logs.String(), hopTestToken) {
		t.Fatal("the hop token reached the log")
	}
}

func TestNewMCPHopPolicyRefusesAnIncompleteConfiguration(t *testing.T) {
	valid := MCPHopPolicyOptions{Validator: &fakeHopValidator{}, Namespace: "acr", ServiceAccount: "acr-mcp", Issuer: hopTestIssuer}
	for name, mutate := range map[string]func(*MCPHopPolicyOptions){
		"no validator":       func(o *MCPHopPolicyOptions) { o.Validator = nil },
		"no namespace":       func(o *MCPHopPolicyOptions) { o.Namespace = " " },
		"no serviceaccount":  func(o *MCPHopPolicyOptions) { o.ServiceAccount = "" },
		"no issuer resource": func(o *MCPHopPolicyOptions) { o.Issuer = "" },
	} {
		options := valid
		mutate(&options)
		if _, err := NewMCPHopPolicy(options); err == nil {
			t.Fatalf("%s: NewMCPHopPolicy succeeded", name)
		}
	}
	if _, err := NewMCPHopPolicy(valid); err != nil {
		t.Fatal(err)
	}
}
