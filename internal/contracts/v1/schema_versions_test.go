package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestAllSchemaVersionsHasNoDuplicates locks AllSchemaVersions as a genuine
// set: a duplicate entry would silently mask a missing distinct schema version
// while still passing a naive length check.
func TestAllSchemaVersionsHasNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(AllSchemaVersions))
	for _, version := range AllSchemaVersions {
		if seen[version] {
			t.Fatalf("AllSchemaVersions contains duplicate entry %q", version)
		}
		seen[version] = true
	}
}

// TestAllSchemaVersionsIncludesEveryDeclaredSchemaConstant is the drift gate:
// every schema_version constant declared in this package must be represented in
// the canonical advertised set, including published-but-reserved Context
// Fabric contracts whose endpoint availability remains independently gated.
func TestAllSchemaVersionsIncludesEveryDeclaredSchemaConstant(t *testing.T) {
	declared := []string{
		ContextPacketRequestSchema,
		ContextPacketSchema,
		ContextPacketItemSchema,
		EvidenceRefSchema,
		ExpandedEvidenceSchema,
		CapabilitiesSchema,
		AgentEpisodeCreateSchema,
		AgentEpisodeSchema,
		ClientCredentialSchema,
		ErrorSchema,
		ContextFabricInvestigationRequestSchema,
		ContextFabricInvestigationResultSchema,
		ContextFabricAnswerProjectionSchema,
		ContextFabricProjectionBatchSchema,
		ContextFabricOrgModelConfigSchema,
		ContextFabricOrgModelConfigWriteRequestSchema,
		MCPContextForTaskRequestSchema,
		MCPContextForTaskResponseSchema,
		MCPSourceEvidenceRequestSchema,
		MCPSourceEvidenceResponseSchema,
		MCPInvestigateQuestionRequestSchema,
		MCPInvestigateQuestionResponseSchema,
		MCPInvestigationResultRequestSchema,
		MCPInvestigationResultResponseSchema,
		MCPRecordEpisodeRequestSchema,
		MCPRecordEpisodeResponseSchema,
	}
	present := make(map[string]bool, len(AllSchemaVersions))
	for _, version := range AllSchemaVersions {
		present[version] = true
	}
	for _, want := range declared {
		if !present[want] {
			t.Fatalf("AllSchemaVersions is missing declared schema constant %q", want)
		}
	}
	if len(AllSchemaVersions) != len(declared) {
		t.Fatalf("AllSchemaVersions has %d entries, want exactly %d declared constants", len(AllSchemaVersions), len(declared))
	}
}

// TestCapabilitiesExampleAdvertisesEverySchemaVersion binds the published
// capabilities example to AllSchemaVersions.
//
// The example is what the hosted API's handshake looks like on the wire,
// and the e2e harness's stub hosted API now serves its
// supported_schema_versions straight out of it (scripts/e2e/mcp-codegraph.sh
// and mcp-codegraph-live.sh). Before that the stub restated a nine-entry
// list by hand, which went stale the moment CHAOS-3746 added the answer
// surface to internal/mcp's required set: acr-mcp refused to start against
// the stub with "hosted API does not support a schema version this sidecar
// requires", and every canonical-receipts scenario failed behind it.
//
// Deriving the stub from the example moved the drift risk here, so this
// closes it: the example must advertise exactly what the real hosted API
// advertises (internal/runtime/hosted/open.go passes AllSchemaVersions
// verbatim). Order is not compared -- the wire contract is a set, and
// nothing reads it positionally.
func TestCapabilitiesExampleAdvertisesEverySchemaVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "examples", "v1", "capabilities.v1.json"))
	if err != nil {
		t.Fatalf("read capabilities example: %v", err)
	}
	var example struct {
		SupportedSchemaVersions []string `json:"supported_schema_versions"`
	}
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatalf("decode capabilities example: %v", err)
	}

	advertised := make(map[string]bool, len(example.SupportedSchemaVersions))
	for _, version := range example.SupportedSchemaVersions {
		advertised[version] = true
	}
	for _, version := range AllSchemaVersions {
		if !advertised[version] {
			t.Errorf("the capabilities example does not advertise %q, so a sidecar requiring it fails startup against any hosted API that looks like this example", version)
		}
	}
	declared := make(map[string]bool, len(AllSchemaVersions))
	for _, version := range AllSchemaVersions {
		declared[version] = true
	}
	for _, version := range example.SupportedSchemaVersions {
		if !declared[version] {
			t.Errorf("the capabilities example advertises %q, which AllSchemaVersions does not declare", version)
		}
	}
}
