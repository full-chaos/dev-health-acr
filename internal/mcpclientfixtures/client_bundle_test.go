package mcpclientfixtures

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientBundle_validates_shared_contract(t *testing.T) {
	path := os.Getenv("MCP_CLIENT_BUNDLE_PATH")
	if path == "" {
		path = filepath.Join("..", "..", "clients", "conformance", "client-bundle.v1.json")
	}
	bundle, err := LoadClientBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("MCP_CLIENT_ROOT")
	if root == "" {
		root = filepath.Join("..", "..")
	}
	if err := ValidateClientPackageRoots(root, bundle); err != nil {
		t.Fatal(err)
	}
	t.Logf("CLIENT_BUNDLE_OK root=%s", root)
}

func TestClientFixtureRunner_rejects_exact_classifications(t *testing.T) {
	fixtureRoot := os.Getenv("MCP_CLIENT_FIXTURE_ROOT")
	if fixtureRoot == "" {
		fixtureRoot = filepath.Join("..", "..", "clients", "conformance", "fixtures")
	}
	if err := ValidateClientFixtures(fixtureRoot); err != nil {
		t.Fatal(err)
	}
	t.Logf("CLIENT_FIXTURES_OK root=%s", fixtureRoot)
}

func TestClientBundle_exposes_typed_scenario_expectations(t *testing.T) {
	// Given
	bundle := validClientBundle()

	// When
	expectations := bundle.ConformanceExpectations()

	// Then
	if got := expectations["context"]; got != (ClientScenarioOutput{Kind: "structured_context", Visible: true}) {
		t.Fatalf("context expectation = %#v", got)
	}
	if got := expectations["evidence"]; got != (ClientScenarioOutput{Kind: "structured_evidence", Visible: true}) {
		t.Fatalf("evidence expectation = %#v", got)
	}
	if got := expectations["unavailable"]; got != (ClientScenarioOutput{Kind: "visible_degradation", Visible: true}) {
		t.Fatalf("unavailable expectation = %#v", got)
	}
}

func TestClientPackageRoots_rejects_package_manifest_unknown_fields(t *testing.T) {
	// Given
	root := t.TempDir()
	bundle := validClientBundle()
	writeValidClientTree(t, root, bundle)
	writeFile(t, filepath.Join(root, "clients", "opencode", "package.v1.json"), `{"bundle_version":"1.0.0","minimum_sidecar_version":"1.0.0","command":"acr-mcp","args":["serve"],"mcp_commands":["context_for_task","source_evidence"],"unexpected":true}`)

	// When
	err := ValidateClientPackageRoots(root, bundle)

	// Then
	assertClientBundleClassification(t, err, "package.manifest_decode")
}

func TestClientPackageRoots_rejects_symlinked_package_directory(t *testing.T) {
	// Given
	root := t.TempDir()
	bundle := validClientBundle()
	writeValidClientTree(t, root, bundle)
	if err := os.RemoveAll(filepath.Join(root, "clients", "opencode")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "clients", "cursor"), filepath.Join(root, "clients", "opencode")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	// When
	err := ValidateClientPackageRoots(root, bundle)

	// Then
	assertClientBundleClassification(t, err, "package.symlink")
}
