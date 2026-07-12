// Package mcp implements the CHAOS-2908 local STDIO MCP sidecar: exactly
// two read-only tools (context_for_task, source_evidence) backed by the
// hosted ACR read API. record_episode is never registered here.
package mcp

import (
	"embed"
	"encoding/json"
	"fmt"
)

// schemaFiles embeds byte-identical copies of the canonical MCP tool
// manifest and JSON Schema documents. The copies live under ./schemas and
// must stay in sync with contracts/mcp/tools.v1.json and
// contracts/jsonschema/v1/mcp_*.schema.json; schemas_parity_test.go fails
// the test suite if they ever drift. Embedding here (rather than reading
// contracts/ at runtime) is what makes tool schema availability
// deterministic for an installed acr-mcp binary with no repository on
// disk.
//
//go:embed schemas/*.json
var schemaFiles embed.FS

const (
	contextForTaskRequestSchemaFile  = "schemas/mcp_context_for_task_request.v1.schema.json"
	contextForTaskResponseSchemaFile = "schemas/mcp_context_for_task_response.v1.schema.json"
	sourceEvidenceRequestSchemaFile  = "schemas/mcp_source_evidence_request.v1.schema.json"
	sourceEvidenceResponseSchemaFile = "schemas/mcp_source_evidence_response.v1.schema.json"
	toolManifestFile                 = "schemas/tools.v1.json"
)

// toolManifestEntry mirrors one entry of contracts/mcp/tools.v1.json.
type toolManifestEntry struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	ReadOnly          bool   `json:"read_only"`
	DisabledByDefault bool   `json:"disabled_by_default"`
	InputSchemaRef    string `json:"input_schema_ref"`
	OutputSchemaRef   string `json:"output_schema_ref"`
}

type toolManifest struct {
	SchemaVersion string              `json:"schema_version"`
	Tools         []toolManifestEntry `json:"tools"`
}

// mustReadSchema reads an embedded schema file, panicking on failure since
// schemaFiles is a compile-time embed: any read failure is a build defect,
// never a runtime/operator condition.
func mustReadSchema(name string) json.RawMessage {
	data, err := schemaFiles.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("mcp: embedded schema %q missing: %v", name, err))
	}
	return json.RawMessage(data)
}

// manifestEntry returns the embedded manifest entry for the given tool
// name, panicking if the canonical manifest does not define it (a build
// defect, since the manifest is embedded and checked by
// schemas_parity_test.go).
func manifestEntry(name string) toolManifestEntry {
	data, err := schemaFiles.ReadFile(toolManifestFile)
	if err != nil {
		panic(fmt.Sprintf("mcp: embedded tool manifest missing: %v", err))
	}
	var manifest toolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		panic(fmt.Sprintf("mcp: embedded tool manifest invalid: %v", err))
	}
	for _, entry := range manifest.Tools {
		if entry.Name == name {
			return entry
		}
	}
	panic(fmt.Sprintf("mcp: embedded tool manifest has no entry for %q", name))
}
