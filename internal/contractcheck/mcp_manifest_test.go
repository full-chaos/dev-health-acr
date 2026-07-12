package contractcheck

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dummySchemaRef is a manifest-relative reference that mcpManifestRoot
// always makes resolvable by writing an empty schema file at that path.
const dummySchemaRef = "../jsonschema/v1/dummy.schema.json"

// mcpManifestRoot creates a temporary repository root containing
// contracts/mcp/tools.v1.json with the given single tool, plus a real
// empty schema file resolvable via dummySchemaRef.
func mcpManifestRoot(t *testing.T, tool map[string]any) string {
	t.Helper()
	root := t.TempDir()
	mcpDir := filepath.Join(root, "contracts", "mcp")
	schemaDir := filepath.Join(root, "contracts", "jsonschema", "v1")
	if err := os.MkdirAll(mcpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "dummy.schema.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"schema_version": "mcp_tools.v1", "tools": []any{tool}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "tools.v1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func baseMCPTool() map[string]any {
	return map[string]any{
		"name":        "context_for_task",
		"description": "test tool",
		"read_only":   true,
	}
}

func TestValidateMCPRejectsMissingInputSchemaRef(t *testing.T) {
	tool := baseMCPTool()
	tool["output_schema_ref"] = dummySchemaRef
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.validateMCP(); err == nil {
		t.Fatal("expected a missing input_schema_ref to fail closed")
	}
}

func TestValidateMCPRejectsNonStringInputSchemaRef(t *testing.T) {
	tool := baseMCPTool()
	tool["input_schema_ref"] = 42
	tool["output_schema_ref"] = dummySchemaRef
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.validateMCP(); err == nil {
		t.Fatal("expected a non-string input_schema_ref to fail closed")
	}
}

func TestValidateMCPRejectsMissingOutputSchemaRef(t *testing.T) {
	tool := baseMCPTool()
	tool["input_schema_ref"] = dummySchemaRef
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.validateMCP(); err == nil {
		t.Fatal("expected a missing output_schema_ref to fail closed")
	}
}

func TestValidateMCPRejectsNonStringOutputSchemaRef(t *testing.T) {
	tool := baseMCPTool()
	tool["input_schema_ref"] = dummySchemaRef
	tool["output_schema_ref"] = []string{"not", "a", "string"}
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.validateMCP(); err == nil {
		t.Fatal("expected a non-string output_schema_ref to fail closed")
	}
}

func TestValidateMCPRejectsWhitespaceOnlySchemaRef(t *testing.T) {
	tool := baseMCPTool()
	tool["input_schema_ref"] = "   "
	tool["output_schema_ref"] = dummySchemaRef
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.validateMCP(); err == nil {
		t.Fatal("expected a whitespace-only input_schema_ref to fail closed")
	}
}

func TestValidateMCPRejectsUnresolvableSchemaRef(t *testing.T) {
	tool := baseMCPTool()
	tool["input_schema_ref"] = "../jsonschema/v1/does_not_exist.schema.json"
	tool["output_schema_ref"] = dummySchemaRef
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.validateMCP(); err == nil {
		t.Fatal("expected an unresolvable input_schema_ref to fail closed")
	}
}

// TestValidateMCPAcceptsResolvableStringRefs proves both schema-ref checks
// pass for a well-formed tool (execution reaches the unrelated manifest
// completeness check, which fails only because this fixture deliberately
// omits the other two required MCP tools).
func TestValidateMCPAcceptsResolvableStringRefs(t *testing.T) {
	tool := baseMCPTool()
	tool["input_schema_ref"] = dummySchemaRef
	tool["output_schema_ref"] = dummySchemaRef
	root := mcpManifestRoot(t, tool)
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	err := check.validateMCP()
	if err == nil {
		t.Fatal("expected the single-tool fixture to fail the manifest completeness check")
	}
	if !strings.Contains(err.Error(), "missing MCP tool") {
		t.Fatalf("expected the schema-ref checks to pass and only completeness to fail, got: %v", err)
	}
}
