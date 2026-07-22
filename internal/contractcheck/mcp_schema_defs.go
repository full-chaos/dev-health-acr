package contractcheck

import (
	"fmt"
	"path/filepath"
	"reflect"
)

// mcpResponseDefsSync declares, for one self-contained MCP response schema,
// that its $defs[defKey] entry must stay byte-for-byte structurally
// synchronized with canonicalFile (minus $schema/$id, with any cross-file
// $ref inside canonicalFile rewritten per refRewrites to the local
// #/$defs/<key> pointer an offline client resolves without fetching a
// sibling schema file). See contracts/jsonschema/v1/mcp_context_for_task_response.v1.schema.json
// and mcp_source_evidence_response.v1.schema.json's own $defs for the
// embedded copies this keeps honest.
type mcpResponseDefsSync struct {
	responseFile  string
	defKey        string
	canonicalFile string
	refRewrites   map[string]string
	// structuredRoot marks the entry the response schema's own
	// "properties.structured.$ref" must point at (via "#/$defs/<defKey>"),
	// as opposed to a nested $defs entry (e.g. context_packet_item.v1)
	// that is only ever reached indirectly through the root entry.
	structuredRoot bool
}

var mcpResponseDefsSyncs = []mcpResponseDefsSync{
	{
		responseFile:   "mcp_context_for_task_response.v1.schema.json",
		defKey:         "context_packet.v1",
		canonicalFile:  "context_packet.v1.schema.json",
		refRewrites:    map[string]string{"context_packet_item.v1.schema.json": "#/$defs/context_packet_item.v1"},
		structuredRoot: true,
	},
	{
		responseFile:  "mcp_context_for_task_response.v1.schema.json",
		defKey:        "context_packet_item.v1",
		canonicalFile: "context_packet_item.v1.schema.json",
	},
	{
		responseFile:  "mcp_context_for_task_response.v1.schema.json",
		defKey:        "evidence_ref.v1",
		canonicalFile: "evidence_ref.v1.schema.json",
	},
	{
		responseFile:   "mcp_source_evidence_response.v1.schema.json",
		defKey:         "expanded_evidence.v1",
		canonicalFile:  "expanded_evidence.v1.schema.json",
		refRewrites:    map[string]string{"evidence_ref.v1.schema.json": "#/$defs/evidence_ref.v1"},
		structuredRoot: true,
	},
	{
		responseFile:  "mcp_source_evidence_response.v1.schema.json",
		defKey:        "evidence_ref.v1",
		canonicalFile: "evidence_ref.v1.schema.json",
	},
}

// validateMCPSchemaDefsSync proves every self-contained MCP response
// schema's embedded $defs entry is structurally identical to its canonical
// source file, so an offline client holding only the single response
// schema file can still fully resolve "structured" without fetching
// context_packet.v1.schema.json / context_packet_item.v1.schema.json /
// expanded_evidence.v1.schema.json / evidence_ref.v1.schema.json
// separately. If a canonical schema changes without its embedded $defs
// copy being regenerated, this fails closed rather than letting the two
// silently drift apart.
func (c *repositoryCheck) validateMCPSchemaDefsSync() error {
	directory := filepath.Join(c.root, "contracts", "jsonschema", "v1")
	for _, sync := range mcpResponseDefsSyncs {
		response, ok := c.registry.byName[sync.responseFile]
		if !ok {
			return fmt.Errorf("MCP response schema %s not loaded", sync.responseFile)
		}
		defs, ok := response["$defs"].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: missing $defs", sync.responseFile)
		}
		actual, ok := defs[sync.defKey].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: $defs.%s is missing or not an object", sync.responseFile, sync.defKey)
		}
		canonicalValue, err := decodeJSONFile(filepath.Join(directory, sync.canonicalFile))
		if err != nil {
			return fmt.Errorf("decode canonical %s: %w", sync.canonicalFile, err)
		}
		canonical, ok := canonicalValue.(map[string]any)
		if !ok {
			return fmt.Errorf("canonical %s must be an object", sync.canonicalFile)
		}
		expected := localizeMCPSchemaRefs(stripSchemaIdentity(canonical), sync.refRewrites)
		if !reflect.DeepEqual(expected, actual) {
			return fmt.Errorf("%s: $defs.%s has drifted from canonical %s; regenerate the embedded copy", sync.responseFile, sync.defKey, sync.canonicalFile)
		}
		if sync.structuredRoot {
			if err := requireStructuredRefsDefs(response, sync.defKey); err != nil {
				return fmt.Errorf("%s: %w", sync.responseFile, err)
			}
		}
	}
	c.ok("MCP response schemas are self-contained (%d embedded $defs in sync)", len(mcpResponseDefsSyncs))
	return nil
}

// requireStructuredRefsDefs proves response's "properties.structured" is a
// local "#/$defs/<defKey>" pointer rather than an external filename ref:
// the byte-for-byte $defs sync check above only proves the embedded copy
// is correct, not that the schema actually resolves "structured" through
// it, so this closes that gap and is what makes the document genuinely
// offline-resolvable end to end.
func requireStructuredRefsDefs(response map[string]any, defKey string) error {
	properties, ok := response["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing properties")
	}
	structured, ok := properties["structured"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing properties.structured")
	}
	ref, _ := structured["$ref"].(string)
	want := "#/$defs/" + defKey
	if ref != want {
		return fmt.Errorf("properties.structured.$ref is %q, want %q", ref, want)
	}
	return nil
}

// stripSchemaIdentity drops the top-level document-identity keywords that
// only make sense for a standalone schema file, never for a $defs entry
// embedded inside another document.
func stripSchemaIdentity(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		if key == "$schema" || key == "$id" {
			continue
		}
		out[key] = value
	}
	return out
}

// localizeMCPSchemaRefs recursively rewrites every "$ref" value found in
// node that matches a key in refRewrites, leaving every other value
// (including non-matching $refs, already-local "#/..." pointers, and
// unrelated keywords) untouched.
func localizeMCPSchemaRefs(node any, refRewrites map[string]string) any {
	switch value := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			if key == "$ref" {
				if ref, ok := child.(string); ok {
					if rewritten, exists := refRewrites[ref]; exists {
						out[key] = rewritten
						continue
					}
				}
			}
			out[key] = localizeMCPSchemaRefs(child, refRewrites)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = localizeMCPSchemaRefs(item, refRewrites)
		}
		return out
	default:
		return value
	}
}
