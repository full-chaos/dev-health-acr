package contractcheck

import (
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
