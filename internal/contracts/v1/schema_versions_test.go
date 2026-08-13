package v1

import "testing"

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
		ContextFabricProjectionBatchSchema,
		ContextFabricOrgModelConfigSchema,
		ContextFabricOrgModelConfigWriteRequestSchema,
		MCPContextForTaskRequestSchema,
		MCPContextForTaskResponseSchema,
		MCPSourceEvidenceRequestSchema,
		MCPSourceEvidenceResponseSchema,
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
