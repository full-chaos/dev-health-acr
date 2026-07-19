package v1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestMCPContextForTaskRequestRequestedCategories(t *testing.T) {
	// Given
	request := loadFixture[MCPContextForTaskRequest](t, "mcp_context_for_task_request_full.v1.json")
	request.RequestedCategories = []PacketCategory{CategoryState, CategoryAction}

	// When
	err := request.Validate()

	// Then
	if err != nil {
		t.Fatalf("validate requested categories: %v", err)
	}
	assertSchemaParity(t, "mcp_context_for_task_request.v1.schema.json", request)
}

func TestMCPContextForTaskResponseMixedFixture(t *testing.T) {
	// Given
	response := loadFixture[MCPContextForTaskResponse](t, "mcp_context_for_task_response_mixed.v1.json")

	// When
	err := response.Validate()

	// Then
	if err != nil {
		t.Fatalf("validate mixed fixture: %v", err)
	}
	if response.LocalContext == nil || response.FederatedBudget == nil {
		t.Fatal("mixed fixture must contain generic local provenance and its federated budget")
	}
	assertSchemaParity(t, "mcp_context_for_task_response.v1.schema.json", response)
}

func TestMCPPacketContentAccounting_excludesRenderedMarkdown(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.FederatedBudget.MaxSerializedBytes = 8192
	response.RenderedMarkdown.Markdown = strings.Repeat("m", mcpRenderedMarkdownMaxLength)
	if response.FederatedBudget.TotalSerializedBytes >= response.FederatedBudget.MaxSerializedBytes {
		t.Fatal("fixture packet content must fit while rendered markdown would exceed the caller maximum")
	}

	// When
	err := response.Validate()

	// Then
	if err != nil {
		t.Fatalf("rendered markdown must stay outside federated packet-content accounting: %v", err)
	}
}

func TestMCPFederationContractRejects_MissingBudget(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.FederatedBudget = nil

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("local_context must require federated_budget")
	}
}

func TestMCPFederationContractRejects_InvalidStatus(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.LocalContext.Status = MCPLocalContextStatus("invalid")

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("invalid local status must be rejected")
	}
}

func TestMCPFederationContractRejects_UnknownCategory(t *testing.T) {
	// Given
	request := MCPContextForTaskRequest{Goal: "Inspect the local index", RequestedCategories: []PacketCategory{"unknown"}}

	// When
	err := request.Validate()

	// Then
	if err == nil {
		t.Fatal("unknown requested category must be rejected")
	}
}

func TestMCPFederationContractRejects_OverBudget(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	response.FederatedBudget.MaxItems = 1

	// When
	err := response.Validate()

	// Then
	if err == nil {
		t.Fatal("combined packet content above the caller maximum must be rejected")
	}
}

func TestMCPFederationContractRejects_CodeGraphPayloadLeak(t *testing.T) {
	// Given
	response := validFederatedResponse(t)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal mixed response: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode mixed response: %v", err)
	}
	local := document["local_context"].(map[string]any)
	local["codegraph_payload"] = map[string]any{"raw": "forbidden"}
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal payload leak mutation: %v", err)
	}

	// When
	schemaErr := contractcheck.ValidateSerialized("", "mcp_context_for_task_response.v1.schema.json", mutated)
	var decoded MCPContextForTaskResponse
	decodeErr := json.Unmarshal(mutated, &decoded)

	// Then
	if schemaErr == nil || decodeErr == nil {
		t.Fatal("local context must reject provider-specific payload fields at both contract boundaries")
	}
}

func validFederatedResponse(t *testing.T) MCPContextForTaskResponse {
	t.Helper()
	response := loadFixture[MCPContextForTaskResponse](t, "mcp_context_for_task_response.v1.json")
	item := response.Structured.Items[0]
	item.PacketItemID = "local_item_001"
	evidence := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
	evidence.EvidenceRefID = "local_evidence_001"
	response.LocalContext = &MCPLocalContext{
		Provider:        "local-index",
		Status:          MCPLocalContextAvailable,
		ProviderVersion: "1.2.0",
		QueryVersion:    "local-query.v1",
		Freshness:       MCPLocalFreshnessFresh,
		Warnings:        []string{},
		Items:           []ContextPacketItem{item},
		EvidenceRefs:    []EvidenceRef{evidence},
	}
	localBytes, err := response.LocalContext.packetContentSerializedBytes()
	if err != nil {
		t.Fatalf("serialize local packet content: %v", err)
	}
	hosted := response.Structured.Budget
	response.FederatedBudget = &MCPFederatedBudget{
		MaxItems:              hosted.MaxItems,
		MaxOutputTokens:       hosted.MaxOutputTokens,
		MaxSerializedBytes:    hosted.MaxSerializedBytes,
		HostedItemsUsed:       hosted.ItemsUsed,
		LocalItemsUsed:        1,
		TotalItemsUsed:        hosted.ItemsUsed + 1,
		HostedEstimatedTokens: hosted.EstimatedTokens,
		LocalEstimatedTokens:  20,
		TotalEstimatedTokens:  hosted.EstimatedTokens + 20,
		HostedSerializedBytes: hosted.SerializedBytes,
		LocalSerializedBytes:  localBytes,
		TotalSerializedBytes:  hosted.SerializedBytes + localBytes,
		HostedTruncated:       hosted.Truncated,
		LocalTruncated:        false,
		Truncated:             false,
	}
	return response
}
