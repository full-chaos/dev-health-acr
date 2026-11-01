package contractcheck

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mcpDefsSyncRoot copies the real repository's contracts/jsonschema/v1
// directory into a temp root, mirroring mcpManifestRoot's pattern: tests
// exercise the actual checked-in canonical schemas rather than a
// hand-built subset, so a real drift between a canonical file and an
// embedded $defs copy is caught the same way it would be in CI.
func mcpDefsSyncRoot(t *testing.T) string {
	t.Helper()
	realRoot, err := findRoot(".")
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	srcDir := filepath.Join(realRoot, "contracts", "jsonschema", "v1")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("read canonical schema directory: %v", err)
	}
	root := t.TempDir()
	dstDir := filepath.Join(root, "contracts", "jsonschema", "v1")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// loadedMCPDefsCheck returns a repositoryCheck with schemas already loaded
// from root, failing the test immediately on any load error.
func loadedMCPDefsCheck(t *testing.T, root string) *repositoryCheck {
	t.Helper()
	check := &repositoryCheck{root: root, out: io.Discard, quiet: true}
	if err := check.loadSchemas(); err != nil {
		t.Fatalf("load schemas: %v", err)
	}
	return check
}

// TestValidateMCPSchemaDefsSyncPassesForCanonicalSchemas locks the
// positive path: the checked-in embedded $defs copies are, right now,
// exactly in sync with their canonical source files.
func TestValidateMCPSchemaDefsSyncPassesForCanonicalSchemas(t *testing.T) {
	check := loadedMCPDefsCheck(t, mcpDefsSyncRoot(t))
	if err := check.validateMCPSchemaDefsSync(); err != nil {
		t.Fatalf("expected canonical schemas to be in sync: %v", err)
	}
}

// TestValidateMCPSchemaDefsSyncRejectsDriftedCanonical proves the drift
// direction that matters most: someone edits context_packet.v1.schema.json
// (the canonical file) without regenerating the embedded $defs copy inside
// mcp_context_for_task_response.v1.schema.json.
func TestValidateMCPSchemaDefsSyncRejectsDriftedCanonical(t *testing.T) {
	root := mcpDefsSyncRoot(t)
	canonicalPath := filepath.Join(root, "contracts", "jsonschema", "v1", "context_packet.v1.schema.json")
	drifted := mustReplace(t, canonicalPath, `"maxLength": 4000`, `"maxLength": 4001`)
	if err := os.WriteFile(canonicalPath, drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	check := loadedMCPDefsCheck(t, root)
	if err := check.validateMCPSchemaDefsSync(); err == nil {
		t.Fatal("expected a drifted canonical schema to fail the $defs sync check")
	}
}

// TestValidateMCPSchemaDefsSyncRejectsMissingDefs proves the embedded
// response schema is itself required to carry the $defs entry at all,
// rather than silently passing when it is dropped.
func TestValidateMCPSchemaDefsSyncRejectsMissingDefs(t *testing.T) {
	root := mcpDefsSyncRoot(t)
	responsePath := filepath.Join(root, "contracts", "jsonschema", "v1", "mcp_context_for_task_response.v1.schema.json")
	drifted := mustReplace(t, responsePath, `"$ref": "#/$defs/context_packet.v1"`, `"$ref": "context_packet.v1.schema.json"`)
	if err := os.WriteFile(responsePath, drifted, 0o644); err != nil {
		t.Fatal(err)
	}
	check := loadedMCPDefsCheck(t, root)
	if err := check.validateMCPSchemaDefsSync(); err == nil {
		t.Fatal("expected a response schema that stopped using #/$defs to fail the self-containment check")
	}
}

func mustReplace(t *testing.T, path, old, new string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced := strings.ReplaceAll(string(data), old, new)
	if replaced == string(data) {
		t.Fatalf("fixture mutation %q not found in %s", old, path)
	}
	return []byte(replaced)
}

// mutateResponseProperty rewrites one property of a response schema in root.
func mutateResponseProperty(t *testing.T, root, responseFile, property string, mutate func(map[string]any) any) {
	t.Helper()
	path := filepath.Join(root, "contracts", "jsonschema", "v1", responseFile)
	value, err := decodeJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	document := value.(map[string]any)
	properties := document["properties"].(map[string]any)
	node, ok := properties[property].(map[string]any)
	if !ok {
		t.Fatalf("%s has no properties.%s", responseFile, property)
	}
	properties[property] = mutate(node)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

// CHAOS-12334: investigate_question's full_result and investigation_result's
// structured are each exactly an anyOf of the two embedded result versions,
// in order. The check refuses every other shape, each of which either narrows
// a v2 result away or admits a body neither result schema admits.
func TestValidateMCPSchemaDefsSyncRequiresTheExactResultUnion(t *testing.T) {
	const v1 = "#/$defs/context_fabric_investigation_result.v1"
	const v2 = "#/$defs/context_fabric_investigation_result.v2"
	ref := func(target string) map[string]any { return map[string]any{"$ref": target} }
	union := func(keyword string, alternatives ...any) map[string]any {
		return map[string]any{"description": "d", keyword: alternatives}
	}
	cells := []struct {
		name   string
		mutate func(map[string]any) any
	}{
		{"v1 only", func(map[string]any) any { return ref(v1) }},
		{"oneOf", func(map[string]any) any { return union("oneOf", ref(v1), ref(v2)) }},
		{"v2 dropped", func(map[string]any) any { return union("anyOf", ref(v1)) }},
		{"extra open alternative", func(map[string]any) any { return union("anyOf", ref(v1), ref(v2), map[string]any{}) }},
		{"v2 as an inline object", func(map[string]any) any { return union("anyOf", ref(v1), map[string]any{"type": "object"}) }},
		{"reordered", func(map[string]any) any { return union("anyOf", ref(v2), ref(v1)) }},
		{"sibling type", func(node map[string]any) any { node["type"] = "object"; return node }},
		{"sibling ref", func(node map[string]any) any { node["$ref"] = v1; return node }},
		{"alternative with a sibling keyword", func(map[string]any) any {
			return union("anyOf", ref(v1), map[string]any{"$ref": v2, "required": []any{"schema_version"}})
		}},
		{"alternative names another def", func(map[string]any) any {
			return union("anyOf", ref(v1), ref("#/$defs/context_fabric_common.v1"))
		}},
	}
	surfaces := []struct{ responseFile, property string }{
		{"mcp_investigate_question_response.v1.schema.json", "full_result"},
		{"mcp_investigation_result_response.v1.schema.json", "structured"},
	}
	for _, surface := range surfaces {
		// The cells must start from the real shape, or they prove nothing.
		real := loadedMCPDefsCheck(t, mcpDefsSyncRoot(t)).registry.byName[surface.responseFile]
		node := real["properties"].(map[string]any)[surface.property].(map[string]any)
		if err := requireStructuredRefsDefs(real, surface.property, []string{"context_fabric_investigation_result.v1", "context_fabric_investigation_result.v2"}); err != nil {
			t.Fatalf("%s: premise: the checked-in %s is refused: %v (%v)", surface.responseFile, surface.property, err, node)
		}
		for _, cell := range cells {
			t.Run(surface.property+"/"+cell.name, func(t *testing.T) {
				root := mcpDefsSyncRoot(t)
				mutateResponseProperty(t, root, surface.responseFile, surface.property, cell.mutate)
				check := loadedMCPDefsCheck(t, root)
				if err := check.validateMCPSchemaDefsSync(); err == nil {
					t.Fatalf("%s: properties.%s %s passed the defs sync check", surface.responseFile, surface.property, cell.name)
				}
			})
		}
	}
}

// CHAOS-12334: removing the embedded v2 result copy, or the StructureNeedsV2
// rewrite its embedded copy depends on, fails the check.
func TestValidateMCPSchemaDefsSyncRequiresTheEmbeddedV2Result(t *testing.T) {
	for _, responseFile := range []string{"mcp_investigate_question_response.v1.schema.json", "mcp_investigation_result_response.v1.schema.json"} {
		t.Run(responseFile+"/v2 copy removed", func(t *testing.T) {
			root := mcpDefsSyncRoot(t)
			path := filepath.Join(root, "contracts", "jsonschema", "v1", responseFile)
			value, err := decodeJSONFile(path)
			if err != nil {
				t.Fatal(err)
			}
			document := value.(map[string]any)
			defs := document["$defs"].(map[string]any)
			if _, ok := defs["context_fabric_investigation_result.v2"]; !ok {
				t.Fatal("premise: no embedded v2 copy")
			}
			delete(defs, "context_fabric_investigation_result.v2")
			encoded, err := json.MarshalIndent(document, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			check := &repositoryCheck{root: root, out: io.Discard, quiet: true}
			if check.loadSchemas() == nil && check.validateMCPSchemaDefsSync() == nil {
				t.Fatal("a response schema without its embedded v2 result copy passed")
			}
		})
	}
	t.Run("StructureNeedsV2 rewrite removed", func(t *testing.T) {
		const key = "context_fabric_common.v1.schema.json#/$defs/StructureNeedsV2"
		saved, ok := contextFabricResultDefsRewrites[key]
		if !ok {
			t.Fatal("premise: no StructureNeedsV2 rewrite")
		}
		delete(contextFabricResultDefsRewrites, key)
		defer func() { contextFabricResultDefsRewrites[key] = saved }()
		check := loadedMCPDefsCheck(t, mcpDefsSyncRoot(t))
		if err := check.validateMCPSchemaDefsSync(); err == nil {
			t.Fatal("the defs sync check passed without the StructureNeedsV2 rewrite")
		}
	})
}
