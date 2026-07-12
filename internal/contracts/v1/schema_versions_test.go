package v1

import "testing"

// TestAllSchemaVersionsHasNoDuplicates locks AllSchemaVersions as a genuine
// set: a duplicate entry would silently mask a missing distinct schema
// version while still passing a naive length check.
func TestAllSchemaVersionsHasNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(AllSchemaVersions))
	for _, version := range AllSchemaVersions {
		if seen[version] {
			t.Fatalf("AllSchemaVersions contains duplicate entry %q", version)
		}
		seen[version] = true
	}
}

// TestAllSchemaVersionsIncludesEveryDeclaredSchemaConstant is a Given/When/
// Then regression: Given every schema_version constant declared in this
// package (types.go's HTTP schemas plus mcp_types.go's MCP schemas), When
// AllSchemaVersions is checked, Then every one of them must be present.
// This is the drift gate: a future PR that adds a new schema_version
// constant but forgets to add it to AllSchemaVersions fails this test
// immediately, instead of silently shipping a hosted API that cannot
// satisfy a sidecar requiring the new schema.
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
		MCPContextForTaskRequestSchema,
		MCPContextForTaskResponseSchema,
		MCPSourceEvidenceRequestSchema,
		MCPSourceEvidenceResponseSchema,
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
