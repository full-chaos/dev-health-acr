package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	uniformOwnerOrg   = "org_a"
	uniformCallerOrg  = "org_b"
	uniformLeakMarker = "run-4821"
)

// orgBoundClickHouse answers like a tenant table: a row is visible only when
// the statement binds the owning organization. Every statement must bind an
// organization.
type orgBoundClickHouse struct {
	t          *testing.T
	owner      string
	repository string
	inner      fixtureClickHouseClient
	mu         sync.Mutex
}

func (c *orgBoundClickHouse) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	org, slug, hasOrg, hasNames := "", "", false, false
	for _, binding := range bindings {
		switch binding.Name {
		case "org_id":
			org, hasOrg = binding.Value.(string), true
		case "repo_slug":
			slug, _ = binding.Value.(string)
		case "names":
			hasNames = true
		}
	}
	if !hasOrg && !hasNames {
		c.mu.Lock()
		c.t.Errorf("statement without an org_id binding: %.120s", statement)
		c.mu.Unlock()
		return &fixtureClickHouseRows{}, nil
	}
	if hasNames {
		return c.inner.Query(ctx, statement, bindings)
	}
	if org != c.owner || (slug != "" && slug != c.repository) {
		return &fixtureClickHouseRows{}, nil
	}
	return c.inner.Query(ctx, statement, bindings)
}

func uniformEvidenceStore(t *testing.T, owner, repository string) storage.EvidenceStore {
	t.Helper()
	client := &orgBoundClickHouse{t: t, owner: owner, repository: repository, inner: fixtureClickHouseClient{repoID: "22222222-2222-4222-8222-222222222222", repository: repository}}
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "fixture", Keys: map[string][]byte{"fixture": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(contextpacket.NewCatalogClickHouseRows(client), contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type uniformSigner struct {
	t       *testing.T
	private ed25519.PrivateKey
	now     time.Time
	jti     int
}

func newUniformSigner(t *testing.T, now time.Time) (*uniformSigner, *auth.WebAssertionVerifier) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return &uniformSigner{t: t, private: private, now: now}, verifier
}

func (s *uniformSigner) request(method, path string, body []byte, org string, scopes []string) *http.Request {
	s.t.Helper()
	s.jti++
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	digest := sha256.Sum256(body)
	claims := map[string]any{
		"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": org,
		"repository_scopes": scopes, "permissions": []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
		"iat": s.now.Unix(), "nbf": s.now.Unix(), "exp": s.now.Add(30 * time.Second).Unix(), "jti": "uniform_" + org + "_" + strings.Repeat("x", s.jti),
		"method": method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
	if err != nil {
		s.t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		s.t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	request.Header.Set(auth.WebAssertionHeader, input+"."+base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.private, []byte(input))))
	return request
}

func (s *uniformSigner) contextPacket(org string, slug string) *http.Request {
	s.t.Helper()
	body, err := json.Marshal(contextRequestFor(slug))
	if err != nil {
		s.t.Fatal(err)
	}
	return s.request(http.MethodPost, "/api/v1/agent-context/context-packets", body, org, []string{slug})
}

func uniformHostedApp(t *testing.T, store storage.EvidenceStore) (*App, *uniformSigner) {
	t.Helper()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	signer, verifier := newUniformSigner(t, now)
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, store, verifier)
	return app, signer
}

func serve(app *App, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

var (
	requestIDPattern = regexp.MustCompile(`req_[0-9a-f]{32}`)
	packetIDPattern  = regexp.MustCompile(`pkt_[0-9a-f]{24}`)
)

// answerOf masks the per-request ids; everything else must match byte for byte.
func answerOf(response *httptest.ResponseRecorder) string {
	body := packetIDPattern.ReplaceAllString(requestIDPattern.ReplaceAllString(response.Body.String(), "req_X"), "pkt_X")
	return response.Result().Status + "\n" + body
}

// A web assertion of one organization that names a repository of another
// organization, an unknown repository, or its own not yet synced repository
// gets one answer, and none of the other organization's values.
func TestWebAssertionForeignOrg_contextPacketsAnswerUniformly(t *testing.T) {
	// Given
	const slug = hostedTestRepository
	controlApp, controlSigner := uniformHostedApp(t, uniformEvidenceStore(t, uniformOwnerOrg, slug))
	control := serve(controlApp, controlSigner.contextPacket(uniformOwnerOrg, slug))
	if control.Code != http.StatusOK || !strings.Contains(control.Body.String(), uniformLeakMarker) {
		t.Fatalf("owner control must read its own data: %d %s", control.Code, control.Body.String())
	}
	cases := map[string]storage.EvidenceStore{
		"foreign-org-slug":  uniformEvidenceStore(t, uniformOwnerOrg, slug),
		"unknown-slug":      uniformEvidenceStore(t, uniformOwnerOrg, "other-org/other-repo"),
		"own-org-unsynced":  uniformEvidenceStore(t, uniformCallerOrg, "other-org/other-repo"),
		"empty-store-owner": uniformEvidenceStore(t, "org_z", "other-org/other-repo"),
	}

	// When
	answers := map[string]string{}
	for name, store := range cases {
		app, signer := uniformHostedApp(t, store)
		response := serve(app, signer.contextPacket(uniformCallerOrg, slug))
		answers[name] = answerOf(response)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body=%s", name, response.Code, response.Body.String())
		}
		for _, foreign := range []string{uniformLeakMarker, "22222222-2222-4222-8222-222222222222", "acr:v1:"} {
			if strings.Contains(response.Body.String(), foreign) {
				t.Fatalf("%s: answer carries a value of another organization (%s): %s", name, foreign, response.Body.String())
			}
		}
	}

	// Then
	want := answers["own-org-unsynced"]
	for name, got := range answers {
		if got != want {
			t.Fatalf("%s answers differently from own-org-unsynced:\n%s\n--- vs ---\n%s", name, got, want)
		}
	}
}

func TestWebAssertionForeignOrg_evidenceAnswersUniformly(t *testing.T) {
	// Given
	const slug = hostedTestRepository
	store := uniformEvidenceStore(t, uniformOwnerOrg, slug)
	app, signer := uniformHostedApp(t, store)
	packet := serve(app, signer.contextPacket(uniformOwnerOrg, slug))
	handle := firstEvidenceRef(t, packet.Body.Bytes())
	ownerRead := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/evidence/"+handle, nil, uniformOwnerOrg, []string{slug}))
	if ownerRead.Code != http.StatusOK {
		t.Fatalf("owner control status = %d body=%s", ownerRead.Code, ownerRead.Body.String())
	}

	// When
	foreign := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/evidence/"+handle, nil, uniformCallerOrg, []string{slug}))
	unknown := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/evidence/acr-unknown-handle-0000", nil, uniformCallerOrg, []string{slug}))

	// Then
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign handle status = %d body=%s", foreign.Code, foreign.Body.String())
	}
	if strings.Contains(foreign.Body.String(), uniformLeakMarker) || answerOf(foreign) != answerOf(unknown) {
		t.Fatalf("foreign handle answer differs from unknown:\n%s\n--- vs ---\n%s", answerOf(foreign), answerOf(unknown))
	}
}

func TestWebAssertionForeignOrg_capabilitiesAnswerUniformly(t *testing.T) {
	// Given
	app, signer := uniformHostedApp(t, uniformEvidenceStore(t, uniformOwnerOrg, hostedTestRepository))

	// When
	owner := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/capabilities", nil, uniformOwnerOrg, []string{hostedTestRepository}))
	other := serve(app, signer.request(http.MethodGet, "/api/v1/agent-context/capabilities", nil, uniformCallerOrg, []string{hostedTestRepository}))

	// Then
	if owner.Code != http.StatusOK || answerOf(owner) != answerOf(other) {
		t.Fatalf("capabilities differ by organization:\n%s\n--- vs ---\n%s", answerOf(owner), answerOf(other))
	}
}

func TestWebAssertionForeignOrg_investigationResultAnswersUniformly(t *testing.T) {
	// Given
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	signer, verifier := newUniformSigner(t, now)
	store := memoryinvestigation.NewStore()
	seeded := seedResult(t, store, uniformOwnerOrg, "result_uniform_owned_01")
	app := uniformResultApp(t, now, verifier, store)
	path := func(id string) string {
		return strings.Replace(ContextFabricInvestigationResultPath, "{result_id}", id, 1)
	}
	owner := serve(app, signer.request(http.MethodGet, path(seeded.ResultID), nil, uniformOwnerOrg, []string{hostedTestRepository}))
	if owner.Code != http.StatusOK {
		t.Fatalf("owner control status = %d body=%s", owner.Code, owner.Body.String())
	}

	// When
	foreign := serve(app, signer.request(http.MethodGet, path(seeded.ResultID), nil, uniformCallerOrg, []string{hostedTestRepository}))
	unknown := serve(app, signer.request(http.MethodGet, path("result_uniform_unknown_1"), nil, uniformCallerOrg, []string{hostedTestRepository}))

	// Then
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign result status = %d body=%s", foreign.Code, foreign.Body.String())
	}
	if answerOf(foreign) != answerOf(unknown) {
		t.Fatalf("foreign result answer differs from unknown:\n%s\n--- vs ---\n%s", answerOf(foreign), answerOf(unknown))
	}
}

func uniformResultApp(t *testing.T, now time.Time, verifier *auth.WebAssertionVerifier, results contextfabric.InvestigationResultStore) *App {
	t.Helper()
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	investigator := investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, nil
	})
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: hostedCapabilities()}, Limits: manager, Now: func() time.Time { return now }, WebAssertions: verifier,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now })),
			ReadinessChecks:            exactRuntimeChecks(), DataStoreChecks: exactDataStoreChecks(),
			Investigator: investigator, InvestigationResults: results, StoredResultGate: testStoredResultGate(investigator),
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func firstEvidenceRef(t *testing.T, packet []byte) string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(packet, &decoded); err != nil {
		t.Fatal(err)
	}
	var found string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if ids, ok := typed["evidence_ref_ids"].([]any); ok && len(ids) > 0 && found == "" {
				found, _ = ids[0].(string)
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decoded)
	if found == "" {
		t.Fatalf("owner packet carries no evidence ref: %s", packet)
	}
	return found
}
