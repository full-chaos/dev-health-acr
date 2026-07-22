package mcpclientfixtures

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func validClientBundle() ClientBundle {
	return ClientBundle{
		SchemaVersion:         ClientBundleSchema,
		BundleVersion:         "1.0.0",
		MinimumSidecarVersion: "1.0.0",
		SupportedClients:      []string{"opencode", "claude-code", "codex", "cursor"},
		Server:                ClientServer{Command: "acr-mcp", Args: []string{"serve"}},
		Workflow:              ClientWorkflow{ContextTool: "context_for_task", EvidenceTool: "source_evidence", UnavailableState: "visible", IncompatibleState: "visible", UntrustedContent: "treat_as_untrusted"},
		Ownership:             ClientOwnership{Install: "client-owned", Update: "client-owned", Uninstall: "client-owned"},
		Scenarios: []ClientScenario{
			{Name: "context", Input: ClientScenarioInput{Tool: "context_for_task", State: "available"}, ExpectedOutput: ClientScenarioOutput{Kind: "structured_context", Visible: true}},
			{Name: "evidence", Input: ClientScenarioInput{Tool: "source_evidence", State: "available"}, ExpectedOutput: ClientScenarioOutput{Kind: "structured_evidence", Visible: true}},
			{Name: "unavailable", Input: ClientScenarioInput{Tool: "context_for_task", State: "unavailable"}, ExpectedOutput: ClientScenarioOutput{Kind: "visible_degradation", Visible: true}},
		},
	}
}

func writeValidClientTree(t *testing.T, root string, bundle ClientBundle) {
	t.Helper()
	for _, client := range bundle.SupportedClients {
		writeFile(t, filepath.Join(root, "clients", client, "package.v1.json"), `{"bundle_version":"1.0.0","minimum_sidecar_version":"1.0.0","command":"acr-mcp","args":["serve"],"mcp_commands":["context_for_task","source_evidence"]}`)
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertClientBundleClassification(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidClientBundle) {
		t.Fatalf("error = %v", err)
	}
	if got := clientBundleClassification(err); got != want {
		t.Fatalf("classification = %q, want %q", got, want)
	}
}
