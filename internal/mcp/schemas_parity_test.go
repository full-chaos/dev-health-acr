package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// findRepoRoot walks up from the current package directory to the
// repository root (identified by go.mod plus a contracts/ directory),
// mirroring internal/contractcheck/util.go's findRoot. This is a
// test-only, repo-checkout-relative lookup: the embedded schemas
// themselves (schemas.go's schemaFiles) never depend on this at runtime.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	current, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, "contracts")); err == nil {
				return current
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatalf("could not find repository root above %s", current)
		}
		current = parent
	}
}

// TestEmbeddedSchemasMatchCanonicalSource pins byte-for-byte parity between
// the embedded internal/mcp/schemas copies (baked into the acr-mcp binary
// so tool schemas are available with no repository on disk) and the
// canonical contracts/mcp + contracts/jsonschema/v1 source files. If a
// canonical file is edited without updating its embedded copy, this test
// fails.
func TestEmbeddedSchemasMatchCanonicalSource(t *testing.T) {
	root := findRepoRoot(t)

	cases := []struct {
		embedded  string
		canonical string
	}{
		{toolManifestFile, "contracts/mcp/tools.v1.json"},
		{contextForTaskRequestSchemaFile, "contracts/jsonschema/v1/mcp_context_for_task_request.v1.schema.json"},
		{contextForTaskResponseSchemaFile, "contracts/jsonschema/v1/mcp_context_for_task_response.v1.schema.json"},
		{sourceEvidenceRequestSchemaFile, "contracts/jsonschema/v1/mcp_source_evidence_request.v1.schema.json"},
		{sourceEvidenceResponseSchemaFile, "contracts/jsonschema/v1/mcp_source_evidence_response.v1.schema.json"},
		{recordEpisodeRequestSchemaFile, "contracts/jsonschema/v1/mcp_record_episode_request.v1.schema.json"},
		{recordEpisodeResponseSchemaFile, "contracts/jsonschema/v1/mcp_record_episode_response.v1.schema.json"},
	}

	for _, tc := range cases {
		t.Run(tc.embedded, func(t *testing.T) {
			embeddedBytes, err := schemaFiles.ReadFile(tc.embedded)
			if err != nil {
				t.Fatalf("read embedded %s: %v", tc.embedded, err)
			}
			canonicalBytes, err := os.ReadFile(filepath.Join(root, tc.canonical))
			if err != nil {
				t.Fatalf("read canonical %s: %v", tc.canonical, err)
			}
			if string(embeddedBytes) != string(canonicalBytes) {
				t.Fatalf("internal/mcp/%s has drifted from canonical %s; re-copy the canonical file", tc.embedded, tc.canonical)
			}
		})
	}
}

// TestManifestEntryMatchesRegisteredTools pins that the embedded manifest
// still describes exactly context_for_task and source_evidence as
// read-only, plus record_episode as a disabled-by-default write tool.
func TestManifestEntryMatchesRegisteredTools(t *testing.T) {
	for _, name := range []string{toolContextForTask, toolSourceEvidence} {
		entry := manifestEntry(name)
		if !entry.ReadOnly {
			t.Fatalf("manifest entry %q must be read_only", name)
		}
		if entry.DisabledByDefault {
			t.Fatalf("manifest entry %q must not be disabled_by_default", name)
		}
		if entry.Description == "" {
			t.Fatalf("manifest entry %q has no description", name)
		}
	}

	episode := manifestEntry("record_episode")
	if episode.ReadOnly {
		t.Fatal("record_episode must not be read_only")
	}
	if !episode.DisabledByDefault {
		t.Fatal("record_episode must be disabled_by_default")
	}
}

func TestManifestEntryPanicsForUnknownTool(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected manifestEntry to panic for an unknown tool name")
		}
	}()
	manifestEntry("does_not_exist")
}
