package mcp

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// fixtureToken is a fake, but shape-valid ("fcacr_" + 32-byte base64url),
// bearer credential used only against local httptest fixtures in this
// package's tests. It is never a real credential.
func fixtureToken(fill byte) string {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = fill
	}
	return "fcacr_" + base64.RawURLEncoding.EncodeToString(secret)
}

func fixedCredentialSource(token string) sidecar.CredentialSource {
	return func() (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{Token: token, Source: "test-fixture"}, nil
	}
}

func writeJSONFixture(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func writeErrorFixture(t *testing.T, w http.ResponseWriter, status int, code string, retryable bool) {
	t.Helper()
	writeJSONFixture(t, w, status, contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema,
		RequestID:     "req_fixture",
		Error: contractsv1.ErrorDetail{
			Code:       code,
			Message:    "fixture " + code,
			HTTPStatus: status,
			Retryable:  retryable,
		},
	})
}

// fixtureConfig builds a sidecar.Config that trusts server's TLS
// certificate (httptest.NewTLSServer) through the CA bundle seam, matching
// production TLS verification with no InsecureSkipVerify anywhere.
func fixtureConfig(t *testing.T, server *httptest.Server) sidecar.Config {
	t.Helper()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := sidecar.Config{
		APIBaseURL:          base,
		Timeout:             5 * time.Second,
		MaxResponseBytes:    1 << 20,
		MaxRequestBodyBytes: 256 << 10,
		ClientName:          "test-sidecar",
		ClientVersion:       "1.0.0",
		SidecarVersion:      "1.0.0",
	}
	if server.Certificate() != nil {
		caPath := filepath.Join(t.TempDir(), "fixture-ca.pem")
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.CACertPath = caPath
	} else {
		cfg.AllowInsecureLoopback = true
	}
	return cfg
}

func validCapabilitiesFixture() contractsv1.Capabilities {
	return contractsv1.Capabilities{
		SchemaVersion:           contractsv1.CapabilitiesSchema,
		Service:                 "dev-health-acr",
		ServiceVersion:          "1.2.3",
		MinimumSidecarVersion:   "0.1.0",
		SupportedSchemaVersions: ourSchemaVersions,
		EnabledTools:            []string{toolContextForTask, toolSourceEvidence, "record_episode"},
		Entitlements:            contractsv1.CapabilityEntitlements{AgentContextRuntime: true},
		Permissions:             contractsv1.CapabilityPermissions{ContextRead: true, EvidenceRead: true},
		Limits:                  contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60},
		GeneratedAt:             time.Now().UTC(),
	}
}

func validContextPacketFixture(requestID string) contractsv1.ContextPacket {
	now := time.Now().UTC()
	return contractsv1.ContextPacket{
		SchemaVersion:   contractsv1.ContextPacketSchema,
		ContextPacketID: "packet_1",
		RequestID:       requestID,
		GeneratedAt:     now,
		Status:          contractsv1.PacketComplete,
		Goal:            "investigate flaky checkout tests",
		Repository:      contractsv1.RepositoryRef{Slug: "acme/widgets"},
		RequestedScope:  contractsv1.RequestedScope{Branch: "main"},
		ResolvedScope: contractsv1.ResolvedScope{
			RepoID: "repo_1", RepoSlug: "acme/widgets", Branch: "main",
			Resolution: contractsv1.ScopeExactCommit, FallbackReasons: []string{},
		},
		QueryVersion:         "v1",
		RankingVersion:       "v1",
		Summary:              "checkout flakiness summary",
		Items:                []contractsv1.ContextPacketItem{},
		RequiredChecks:       []contractsv1.RequiredCheck{},
		RecommendedNextSteps: []contractsv1.RecommendedStep{},
		Freshness:            contractsv1.Freshness{AsOf: now, StaleAfterSeconds: 3600, Watermarks: []contractsv1.SourceWatermark{}},
		Coverage:             contractsv1.Coverage{SourcesConsidered: []string{"github"}, SourcesAvailable: []string{"github"}, SourcesUnavailable: []contractsv1.UnavailableSource{}, DegradedReasons: []string{}},
		Budget:               contractsv1.PacketBudget{MaxItems: 10, MaxOutputTokens: 2000, MaxSerializedBytes: 65536},
		Warnings:             []string{},
		Compatibility: contractsv1.Compatibility{
			ServiceVersion: "1.2.3", MinimumSidecarVersion: "0.1.0", SupportedSchemaVersions: []string{contractsv1.ContextPacketSchema},
		},
	}
}

func validExpandedEvidenceFixture(id string) contractsv1.ExpandedEvidence {
	now := time.Now().UTC()
	return contractsv1.ExpandedEvidence{
		SchemaVersion: contractsv1.ExpandedEvidenceSchema,
		Evidence: contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema,
			EvidenceRefID: id,
			Source:        contractsv1.EvidenceSource{System: "github_actions", EntityType: "workflow_run", EntityID: "12345", DisplayLabel: "CI run #12345"},
			Provenance:    "heuristic",
			Confidence:    0.9,
			Citation:      "log line 42",
			ObservedAt:    now,
			Availability:  contractsv1.EvidenceAvailable,
		},
		ResolvedAt:   now,
		Excerpt:      "log line 42: checkout step failed after 3 retries",
		Availability: contractsv1.EvidenceAvailable,
		Structured:   map[string]any{"attempt": 3},
	}
}

// fixtureServer wires GET capabilities, POST context-packets, and GET
// evidence handlers onto one httptest.NewTLSServer, each independently
// overridable so error-matrix tests can substitute a failure response for
// exactly one endpoint.
type fixtureServer struct {
	Server               *httptest.Server
	CapabilitiesHandler  http.HandlerFunc
	ContextPacketHandler http.HandlerFunc
	EvidenceHandler      http.HandlerFunc
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fx := &fixtureServer{}
	fx.CapabilitiesHandler = func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}
	fx.ContextPacketHandler = func(w http.ResponseWriter, r *http.Request) {
		var received contractsv1.ContextPacketRequest
		_ = json.NewDecoder(r.Body).Decode(&received)
		writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(received.RequestID))
	}
	fx.EvidenceHandler = func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/api/v1/agent-context/evidence/"):]
		writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture(id))
	}
	fx.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent-context/capabilities":
			fx.CapabilitiesHandler(w, r)
		case r.URL.Path == "/api/v1/agent-context/context-packets":
			fx.ContextPacketHandler(w, r)
		case len(r.URL.Path) > len("/api/v1/agent-context/evidence/") && r.URL.Path[:len("/api/v1/agent-context/evidence/")] == "/api/v1/agent-context/evidence/":
			fx.EvidenceHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fx.Server.Close)
	return fx
}

// newFixtureBootstrap constructs a *Bootstrap talking to a freshly created
// fixtureServer with a valid, compatible capabilities response, for tests
// that need a ready-to-use handler dependency without exercising
// NewBootstrap's environment-variable loading path.
func newFixtureBootstrap(t *testing.T, fx *fixtureServer) *Bootstrap {
	t.Helper()
	cfg := fixtureConfig(t, fx.Server)
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	return &Bootstrap{Config: cfg, Client: client, Capabilities: validCapabilitiesFixture()}
}
