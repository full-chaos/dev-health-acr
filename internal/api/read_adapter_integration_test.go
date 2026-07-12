package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	postgresstore "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
)

func TestHostedReadRoutesUsePostgresAndClickHouseAdapters(t *testing.T) {
	database, _, err := openFixturePostgres()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	credentials, err := postgresstore.NewCredentialStore(database)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := postgresstore.NewAuditStore(database)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	credentialService, err := auth.NewService(credentials, audit, auth.ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := credentialService.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: "11111111-1111-4111-8111-111111111111", Name: "adapter fixture",
		RepositoryScopes: []string{hostedTestRepository}, Scopes: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	clickHouse := fixtureClickHouseClient{repoID: "22222222-2222-4222-8222-222222222222", repository: hostedTestRepository}
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "fixture", Keys: map[string][]byte{"fixture": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatal(err)
	}
	rows := contextpacket.NewCatalogClickHouseRows(clickHouse)
	evidence, err := contextpacket.NewClickHouseEvidenceStoreWithOptions(rows, contextpacket.EvidenceStoreOptions{Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	assembler := contextpacket.NewAssembler(evidence, contextpacket.Options{Now: func() time.Time { return now }, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0"})
	manager, err := limits.NewManager(limits.Options{Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 20},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 20, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 20},
	}})
	if err != nil {
		t.Fatal(err)
	}
	entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Value: hostedCapabilities()}, Limits: manager,
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: entitlements, Assembler: assembler, Evidence: evidence,
			ReadinessChecks: []ReadinessCheck{
				CheckFunc{CheckName: "credential_store", Fn: database.PingContext},
				CheckFunc{CheckName: "entitlement_provider"},
				CheckFunc{CheckName: "evidence_store", Fn: fixtureClickHouseReadiness(clickHouse)},
			},
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	packetResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(packetResponse, contextPacketRequest(t, app, issued.Token, hostedContextRequest()))
	if packetResponse.Code != http.StatusOK {
		t.Fatalf("packet status = %d body=%s", packetResponse.Code, packetResponse.Body.String())
	}
	var packet contractsv1.ContextPacket
	if err := json.Unmarshal(packetResponse.Body.Bytes(), &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.Items) == 0 || len(packet.Items[0].EvidenceRefIDs) == 0 {
		t.Fatalf("packet = %#v", packet)
	}
	referenceID := packet.Items[0].EvidenceRefIDs[0]
	evidenceRequest := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/evidence/"+referenceID, nil)
	evidenceRequest.Header.Set("Authorization", "Bearer "+issued.Token)
	evidenceRequest.Header.Set("X-ACR-Client-Version", "1.0.0")
	evidenceResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(evidenceResponse, evidenceRequest)
	if evidenceResponse.Code != http.StatusOK {
		t.Fatalf("evidence status = %d body=%s", evidenceResponse.Code, evidenceResponse.Body.String())
	}
}

func fixtureClickHouseReadiness(client fixtureClickHouseClient) func(context.Context) error {
	return func(ctx context.Context) error {
		rows, err := client.Query(ctx, "SELECT 1", nil)
		if err != nil {
			return err
		}
		return rows.Close()
	}
}
