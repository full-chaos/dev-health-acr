package v1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// mcpManifestPath resolves contracts/mcp/tools.v1.json relative to this test file.
func mcpManifestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(fixturePath(t, ".."), "..", "mcp", "tools.v1.json")
}

// mcpToolInputSchemaName reads the MCP tool manifest and returns the base
// filename of the referenced input schema for the named tool. It fails the
// test if the tool is missing or does not use a schema-file reference.
func mcpToolInputSchemaName(t *testing.T, toolName string) string {
	t.Helper()
	raw, err := os.ReadFile(mcpManifestPath(t))
	if err != nil {
		t.Fatalf("read MCP manifest: %v", err)
	}
	var manifest struct {
		Tools []struct {
			Name           string `json:"name"`
			InputSchemaRef string `json:"input_schema_ref"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode MCP manifest: %v", err)
	}
	for _, tool := range manifest.Tools {
		if tool.Name == toolName {
			if tool.InputSchemaRef == "" {
				t.Fatalf("MCP tool %s has no input_schema_ref", toolName)
			}
			return filepath.Base(tool.InputSchemaRef)
		}
	}
	t.Fatalf("MCP tool %s not found in manifest", toolName)
	return ""
}

// TestHTTPContextPacketRequestSchemaRejectsGoalOnlyInput is a characterization
// test. It pins the pre-existing behavior of the hosted HTTP contract: a
// minimal, ergonomic "goal-only" payload (the shape an MCP client naturally
// wants to send) does NOT satisfy context_packet_request.v1.schema.json,
// because that schema requires request_id, repository, scope, options, and
// client. This is expected and correct for the HTTP surface; it documents
// why context_for_task needs its own MCP-specific input contract rather than
// reusing the HTTP schema directly. If this test ever starts failing (i.e.
// the HTTP schema starts accepting goal-only input), the HTTP contract has
// silently loosened its requiredness and MCP wiring must be re-verified.
func TestHTTPContextPacketRequestSchemaRejectsGoalOnlyInput(t *testing.T) {
	payload := []byte(`{"schema_version":"context_packet_request.v1","goal":"Add repository-scoped ACR credentials"}`)
	if err := contractcheck.ValidateSerialized("", "context_packet_request.v1.schema.json", payload); err == nil {
		t.Fatal("expected the HTTP context_packet_request schema to reject goal-only input; a direct HTTP-schema reference must not be reused for the MCP context_for_task tool")
	}
}

// TestMCPManifestContextForTaskInputAcceptsGoalOnly drives (and then locks)
// the ergonomic MCP contract: whatever schema contracts/mcp/tools.v1.json
// currently wires up as context_for_task's input_schema_ref must accept a
// goal-only payload (goal, nothing else -- no caller-supplied
// schema_version). Before the MCP-specific schema existed this resolved
// to the HTTP schema and failed,
// proving the direct HTTP-schema reference is unusable for MCP clients.
func TestMCPManifestContextForTaskInputAcceptsGoalOnly(t *testing.T) {
	schemaName := mcpToolInputSchemaName(t, "context_for_task")
	payload := []byte(`{"goal":"Add repository-scoped ACR credentials"}`)
	if err := contractcheck.ValidateSerialized("", schemaName, payload); err != nil {
		t.Fatalf("goal-only context_for_task input must validate against the manifest-referenced schema %s: %v", schemaName, err)
	}
}

// TestMCPManifestContextForTaskInputRejectsSchemaVersion locks the
// removal of schema_version from the context_for_task input contract: a
// payload that still sends it must be rejected as an unrecognized field,
// exactly like TestMCPManifestSourceEvidenceInputRejectsSchemaVersion
// below.
func TestMCPManifestContextForTaskInputRejectsSchemaVersion(t *testing.T) {
	schemaName := mcpToolInputSchemaName(t, "context_for_task")
	payload := []byte(`{"schema_version":"` + MCPContextForTaskRequestSchema + `","goal":"Add repository-scoped ACR credentials"}`)
	if err := contractcheck.ValidateSerialized("", schemaName, payload); err == nil {
		t.Fatal("expected schema_version to be rejected: context_for_task input must accept exactly goal plus optional repository/scope/budget")
	}
}

// TestMCPManifestSourceEvidenceInputAcceptsEvidenceRefIDOnly locks the
// ergonomic MCP contract for source_evidence: it must accept exactly
// evidence_ref_id, with no schema_version wire field.
func TestMCPManifestSourceEvidenceInputAcceptsEvidenceRefIDOnly(t *testing.T) {
	schemaName := mcpToolInputSchemaName(t, "source_evidence")
	payload := []byte(`{"evidence_ref_id":"ev_01J0ACR001"}`)
	if err := contractcheck.ValidateSerialized("", schemaName, payload); err != nil {
		t.Fatalf("evidence_ref_id-only source_evidence input must validate against the manifest-referenced schema %s: %v", schemaName, err)
	}
}

// TestMCPManifestSourceEvidenceInputRejectsSchemaVersion locks the removal
// of schema_version from the source_evidence input contract: a payload
// that still sends it must be rejected as an unrecognized field.
func TestMCPManifestSourceEvidenceInputRejectsSchemaVersion(t *testing.T) {
	schemaName := mcpToolInputSchemaName(t, "source_evidence")
	payload := []byte(`{"schema_version":"` + MCPSourceEvidenceRequestSchema + `","evidence_ref_id":"ev_01J0ACR001"}`)
	if err := contractcheck.ValidateSerialized("", schemaName, payload); err == nil {
		t.Fatal("expected schema_version to be rejected: source_evidence input must accept exactly evidence_ref_id")
	}
}

func TestMCPSourceEvidenceRequestFixture(t *testing.T) {
	request := loadFixture[MCPSourceEvidenceRequest](t, "mcp_source_evidence_request.v1.json")
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	assertSchemaParity(t, "mcp_source_evidence_request.v1.schema.json", request)
}

func TestMCPSourceEvidenceRequestRejectsUnknownField(t *testing.T) {
	payload := []byte(`{"evidence_ref_id":"ev_01J0ACR001","extra":"nope"}`)
	if err := contractcheck.ValidateSerialized("", "mcp_source_evidence_request.v1.schema.json", payload); err == nil {
		t.Fatal("expected unknown top-level field to be rejected")
	}
}
