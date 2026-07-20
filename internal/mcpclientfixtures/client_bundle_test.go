package mcpclientfixtures

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	if _, err := os.Stat(filepath.Join(fixtureRoot, "fixture.v1.json")); err == nil {
		fixture, err := loadClientFixture(filepath.Join(fixtureRoot, "fixture.v1.json"))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("CLIENT_INVALID_FIXTURE classification=%s", fixture.ExpectedClassification)
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

func TestClientPackageRoots_rejects_client_contract_fork_path(t *testing.T) {
	// Given
	root := t.TempDir()
	bundle := validClientBundle()
	writeValidClientTree(t, root, bundle)
	writeFile(t, filepath.Join(root, "clients", "opencode", "client-bundle.v1.json"), `{}`)

	// When
	err := ValidateClientPackageRoots(root, bundle)

	// Then
	assertClientBundleClassification(t, err, "package.contract_fork")
}

func TestClientPackageRoots_rejects_enabled_package_defaults(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		contents string
		class    string
	}{
		{"writeback", `{"writeback_enabled_by_default":true}`, "package.writeback_default"},
		{"preplan", `{"preplan_enabled_by_default":true}`, "package.preplan_default"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			// Given
			root := t.TempDir()
			bundle := validClientBundle()
			writeValidClientTree(t, root, bundle)
			writeFile(t, filepath.Join(root, "clients", "opencode", "config.json"), fixture.contents)

			// When
			err := ValidateClientPackageRoots(root, bundle)

			// Then
			assertClientBundleClassification(t, err, fixture.class)
		})
	}
}

func TestClientPackageRoots_allows_only_canonical_OpenCode_schema_URL(t *testing.T) {

	// Given
	root := t.TempDir()
	bundle := validClientBundle()
	writeValidClientTree(t, root, bundle)
	writeFile(t, filepath.Join(root, "clients", "opencode", "config", "opencode.json"), `{"$schema":"https://opencode.ai/config.json","mcp":{"acr":{"type":"local","command":["acr-mcp","serve"]}}}`)

	// When
	err := ValidateClientPackageRoots(root, bundle)

	// Then
	if err != nil {
		t.Fatalf("canonical OpenCode schema URL rejected: %v", err)
	}
}

func TestClientPackageRoots_rejects_noncanonical_OpenCode_schema_URL(t *testing.T) {

	// Given
	root := t.TempDir()
	bundle := validClientBundle()
	writeValidClientTree(t, root, bundle)
	writeFile(t, filepath.Join(root, "clients", "opencode", "config", "opencode.json"), `{"$schema":"https://example.invalid/config.json"}`)

	// When
	err := ValidateClientPackageRoots(root, bundle)

	// Then
	assertClientBundleClassification(t, err, "package.direct_api")
}

func TestClientPackageRoots_rejects_canonical_OpenCode_schema_outside_OpenCode_package(t *testing.T) {

	// Given
	root := t.TempDir()
	bundle := validClientBundle()
	writeValidClientTree(t, root, bundle)
	writeFile(t, filepath.Join(root, "clients", "codex", "config", "opencode.json"), `{"$schema":"https://opencode.ai/config.json"}`)

	// When
	err := ValidateClientPackageRoots(root, bundle)

	// Then
	assertClientBundleClassification(t, err, "package.direct_api")
}

func TestClientPackageVerifier_distinguishes_aggregate_and_invalid_fixture_modes(t *testing.T) {
	// Given
	verifier := filepath.Join("..", "..", "scripts", "clients", "verify-packages.sh")

	// When
	aggregate := exec.Command("bash", verifier, "--contract", "clients/conformance/client-bundle.v1.json")
	aggregateOutput, aggregateErr := aggregate.CombinedOutput()
	invalid := exec.Command("bash", verifier, "--fixture", "clients/conformance/fixtures/invalid-client-fork")
	invalidOutput, invalidErr := invalid.CombinedOutput()

	// Then
	if aggregateErr != nil || !strings.Contains(string(aggregateOutput), "CLIENT_FIXTURES_OK") {
		t.Fatalf("aggregate verifier: %v\n%s", aggregateErr, aggregateOutput)
	}
	exitError, ok := invalidErr.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 1 || !strings.Contains(string(invalidOutput), "CLIENT_INVALID_FIXTURE classification=package.contract_fork") {
		t.Fatalf("invalid verifier: %v\n%s", invalidErr, invalidOutput)
	}
}
