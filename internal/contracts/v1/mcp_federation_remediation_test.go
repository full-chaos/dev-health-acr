package v1

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestMCPFederationContractRejects_NullWarning(t *testing.T) {
	// Given
	payload := federationPayload(t)
	local := payload["local_context"].(map[string]any)
	local["warnings"] = []any{nil}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal null warning payload: %v", err)
	}

	// When
	schemaErr := contractcheck.ValidateSerialized("", "mcp_context_for_task_response.v1.schema.json", raw)
	var decoded MCPContextForTaskResponse
	decodeErr := json.Unmarshal(raw, &decoded)

	// Then
	if schemaErr == nil || decodeErr == nil {
		t.Fatal("local_context.warnings entries must reject explicit JSON null at both contract boundaries")
	}
}

func TestMCPFederationContractRejects_NestedCodeGraphPayloadLeak(t *testing.T) {
	// Given
	payload := federationPayload(t)
	local := payload["local_context"].(map[string]any)
	evidence := local["evidence_refs"].([]any)[0].(map[string]any)
	evidence["metadata"] = map[string]any{"nested": map[string]any{"codegraph_payload": "forbidden"}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal nested payload leak: %v", err)
	}

	// When
	schemaErr := contractcheck.ValidateSerialized("", "mcp_context_for_task_response.v1.schema.json", raw)
	var decoded MCPContextForTaskResponse
	decodeErr := json.Unmarshal(raw, &decoded)

	// Then
	if schemaErr == nil || decodeErr == nil {
		t.Fatal("nested provider payload metadata must be rejected at both contract boundaries")
	}
}

func TestMCPLocalContextValidateRejects_NestedCodeGraphPayloadLeak(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.LocalContext.EvidenceRefs[0].Metadata = map[string]any{"nested": map[string]any{"codegraph_payload": "forbidden"}}

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("local evidence validation must reject nested provider payload metadata")
	}
}

func TestMCPFederationContractRejects_TruncationMismatch(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.FederatedBudget.LocalTruncated = true

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("federated truncation must equal hosted or local truncation")
	}
}

func TestMCPFederationContractRejects_TruncatedWithoutSource(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.FederatedBudget.Truncated = true

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("federated truncation must not be true when no hosted or local content truncated")
	}
}

func TestMCPFederationContractRejects_LocalSerializedBytesMismatch(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.FederatedBudget.LocalSerializedBytes = 1
	response.FederatedBudget.TotalSerializedBytes = response.FederatedBudget.HostedSerializedBytes + 1

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("local serialized bytes must match compact items and evidence_refs content")
	}
}

func TestMCPLocalContextSerializedBytes_matchesMixedFixture(t *testing.T) {
	// Given
	response := loadFixture[MCPContextForTaskResponse](t, "mcp_context_for_task_response_mixed.v1.json")
	content := struct {
		Items        []ContextPacketItem `json:"items"`
		EvidenceRefs []EvidenceRef       `json:"evidence_refs"`
	}{Items: response.LocalContext.Items, EvidenceRefs: response.LocalContext.EvidenceRefs}

	// When
	encoded, err := json.Marshal(content)

	// Then
	if err != nil {
		t.Fatalf("marshal local packet content: %v", err)
	}
	if len(encoded) != 1081 {
		t.Fatalf("compact local packet content = %d bytes, want 1081", len(encoded))
	}
	if response.FederatedBudget.LocalSerializedBytes != len(encoded) {
		t.Fatalf("fixture local_serialized_bytes = %d, want %d", response.FederatedBudget.LocalSerializedBytes, len(encoded))
	}
}

func federationPayload(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(validFederatedResponse(t))
	if err != nil {
		t.Fatalf("marshal federation response: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode federation response: %v", err)
	}
	return payload
}
