package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "examples", "v1", name)
}

func loadFixture[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return value
}

func TestContextPacketRequestFixture(t *testing.T) {
	request := loadFixture[ContextPacketRequest](t, "context_packet_request.v1.json")
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestContextPacketFixture(t *testing.T) {
	packet := loadFixture[ContextPacket](t, "context_packet.v1.json")
	if packet.SchemaVersion != ContextPacketSchema {
		t.Fatalf("unexpected schema version: %s", packet.SchemaVersion)
	}
	if len(packet.Items) == 0 {
		t.Fatal("fixture must contain packet items")
	}
	if packet.ResolvedScope.Resolution != ScopeExactCommit {
		t.Fatalf("unexpected scope resolution: %s", packet.ResolvedScope.Resolution)
	}
}

func TestContextPacketItemFixture(t *testing.T) {
	item := loadFixture[ContextPacketItem](t, "context_packet_item.v1.json")
	if item.SchemaVersion != ContextPacketItemSchema {
		t.Fatalf("unexpected schema version: %s", item.SchemaVersion)
	}
	if item.ClaimKind == ClaimObserved && len(item.EvidenceRefIDs) == 0 {
		t.Fatal("observed fixture must include evidence")
	}
}

func TestEvidenceFixtures(t *testing.T) {
	ref := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
	if ref.SchemaVersion != EvidenceRefSchema || ref.EvidenceRefID == "" {
		t.Fatal("invalid evidence reference fixture")
	}
	expanded := loadFixture[ExpandedEvidence](t, "expanded_evidence.v1.json")
	if expanded.SchemaVersion != ExpandedEvidenceSchema {
		t.Fatalf("unexpected schema version: %s", expanded.SchemaVersion)
	}
	if expanded.Evidence.EvidenceRefID != ref.EvidenceRefID {
		t.Fatal("expanded evidence must contain the source evidence ref")
	}
}

func TestCapabilitiesFixtureSeparatesEntitlementAndPermissions(t *testing.T) {
	caps := loadFixture[Capabilities](t, "capabilities.v1.json")
	if caps.SchemaVersion != CapabilitiesSchema {
		t.Fatalf("unexpected schema version: %s", caps.SchemaVersion)
	}
	if !caps.Entitlements.AgentContextRuntime {
		t.Fatal("fixture must include the product entitlement")
	}
	if !caps.Permissions.ContextRead || !caps.Permissions.EvidenceRead {
		t.Fatal("fixture must grant the read permissions")
	}
	if caps.Permissions.EpisodeWrite {
		t.Fatal("fixture must keep episode write disabled")
	}
}

func TestAgentEpisodeFixtures(t *testing.T) {
	create := loadFixture[AgentEpisodeCreate](t, "agent_episode_create.v1.json")
	if err := create.Validate(); err != nil {
		t.Fatal(err)
	}
	response := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
	if response.SchemaVersion != AgentEpisodeSchema {
		t.Fatalf("unexpected schema version: %s", response.SchemaVersion)
	}
	if response.EpisodeID == "" {
		t.Fatal("response fixture must include episode_id")
	}
}

func TestClientCredentialFixture(t *testing.T) {
	credential := loadFixture[ClientCredential](t, "acr_client_credential.v1.json")
	if credential.SchemaVersion != ClientCredentialSchema {
		t.Fatalf("unexpected schema version: %s", credential.SchemaVersion)
	}
	if credential.TokenPrefix == "" || credential.OrgID == "" {
		t.Fatal("credential fixture must include safe identifying metadata")
	}
}

func TestErrorFixture(t *testing.T) {
	envelope := loadFixture[ErrorEnvelope](t, "error.v1.json")
	if envelope.SchemaVersion != ErrorSchema || envelope.Error.HTTPStatus < 400 {
		t.Fatal("invalid error fixture")
	}
}
