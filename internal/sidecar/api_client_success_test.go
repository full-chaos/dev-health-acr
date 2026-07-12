package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// validCapabilitiesFixture, validContextPacketFixture, and
// validExpandedEvidenceFixture return complete, semantically valid
// responses (every field validateCapabilities/validateContextPacket/
// validateExpandedEvidence requires is populated), so success-path tests
// exercise the real post-decode validation rather than accidentally
// bypassing it with a sparse fixture. Tests that specifically want to
// prove an incomplete response is rejected (api_client_validate_test.go)
// start from one of these and zero out exactly the field under test.
func validCapabilitiesFixture() contractsv1.Capabilities {
	return contractsv1.Capabilities{
		SchemaVersion:           contractsv1.CapabilitiesSchema,
		Service:                 "dev-health-acr",
		ServiceVersion:          "1.2.3",
		MinimumSidecarVersion:   "0.1.0",
		SupportedSchemaVersions: []string{contractsv1.CapabilitiesSchema},
		EnabledTools:            []string{"context_for_task", "source_evidence"},
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
		Availability: contractsv1.EvidenceAvailable,
		Structured:   map[string]any{},
	}
}

func TestClientCapabilitiesSuccessSendsBearerAndClientVersion(t *testing.T) {
	var gotAuth, gotClientVersion, gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotClientVersion = r.Header.Get("X-ACR-Client-Version")
		gotPath = r.URL.Path
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := client.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+testBearerCanary {
		t.Fatalf("bearer header not sent correctly: %q", gotAuth)
	}
	if gotClientVersion != "1.0.0" {
		t.Fatalf("client version header not sent: %q", gotClientVersion)
	}
	if gotPath != capabilitiesPath {
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if capabilities.Service != "dev-health-acr" || capabilities.ServiceVersion != "1.2.3" {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}

func TestClientContextPacketSuccessStampsRequestFields(t *testing.T) {
	var received contractsv1.ContextPacketRequest
	var gotMethod, gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := decodeExact(mustReadAll(t, r), &received); err != nil {
			t.Fatal(err)
		}
		writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(received.RequestID))
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	packet, err := client.ContextPacket(context.Background(), validContextPacketRequest())
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != contextPacketsPath {
		t.Fatalf("unexpected request: method=%s path=%s", gotMethod, gotPath)
	}
	if received.SchemaVersion != contractsv1.ContextPacketRequestSchema {
		t.Fatalf("client did not stamp schema_version: %q", received.SchemaVersion)
	}
	if len(received.RequestID) < 8 {
		t.Fatalf("client did not stamp a contract-valid request id: %q", received.RequestID)
	}
	if received.Client.Name != "test-sidecar" || received.Client.Version != "1.0.0" {
		t.Fatalf("client did not stamp its own identity: %#v", received.Client)
	}
	if packet.ContextPacketID != "packet_1" {
		t.Fatalf("unexpected packet: %#v", packet)
	}
}

func TestClientEvidenceSuccess(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != evidencePathPrefix+"ev_abc123" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		writeJSONFixture(t, w, http.StatusOK, validExpandedEvidenceFixture("ev_abc123"))
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := client.Evidence(context.Background(), "ev_abc123")
	if err != nil {
		t.Fatal(err)
	}
	if expanded.Evidence.EvidenceRefID != "ev_abc123" {
		t.Fatalf("unexpected evidence: %#v", expanded)
	}
}

func TestClientAllowsInsecureLoopbackFixtureModeOverPlainHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONFixture(t, w, http.StatusOK, validCapabilitiesFixture())
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := client.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Service != "dev-health-acr" {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}
}
