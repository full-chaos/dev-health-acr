package mcpclientfixtures

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type clientFixture struct {
	ExpectedClassification string `json:"expected_classification"`
	SymlinkEscape          bool   `json:"symlink_escape"`
}

// ValidateClientFixtures executes every checked-in negative bundle and package
// tree fixture, requiring its declared safe classification.
func ValidateClientFixtures(fixturesRoot string) error {
	if _, err := os.Stat(filepath.Join(fixturesRoot, "fixture.v1.json")); err == nil {
		return validateClientFixture(fixturesRoot)
	}
	entries, err := os.ReadDir(fixturesRoot)
	if err != nil {
		return invalidBundle("fixture.missing")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return invalidBundle("fixture.malformed")
		}
		if err := validateClientFixture(filepath.Join(fixturesRoot, entry.Name())); err != nil {
			return fmt.Errorf("fixture %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func validateClientFixture(fixtureRoot string) error {
	fixture, err := loadClientFixture(filepath.Join(fixtureRoot, "fixture.v1.json"))
	if err != nil {
		return err
	}
	bundle, err := LoadClientBundle(filepath.Join(fixtureRoot, "client-bundle.v1.json"))
	if err == nil {
		tree, materializeErr := materializeClientFixtureTree(fixtureRoot, bundle, fixture.SymlinkEscape)
		if materializeErr != nil {
			return materializeErr
		}
		defer os.RemoveAll(tree)
		err = ValidateClientPackageRoots(tree, bundle)
	}
	if classification := clientBundleClassification(err); classification != fixture.ExpectedClassification {
		return fmt.Errorf("expected classification %q, got %q", fixture.ExpectedClassification, classification)
	}
	return nil
}

func loadClientFixture(path string) (clientFixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return clientFixture{}, invalidBundle("fixture.missing")
	}
	var fixture clientFixture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil || fixture.ExpectedClassification == "" {
		return clientFixture{}, invalidBundle("fixture.malformed")
	}
	return fixture, nil
}

func materializeClientFixtureTree(fixtureRoot string, bundle ClientBundle, symlinkEscape bool) (string, error) {
	root, err := os.MkdirTemp("", "acr-client-fixture-")
	if err != nil {
		return "", fmt.Errorf("create fixture tree: %w", err)
	}
	for _, client := range bundle.SupportedClients {
		packageRoot := filepath.Join(root, "clients", client)
		if err := os.MkdirAll(packageRoot, 0o755); err != nil {
			return "", fmt.Errorf("create fixture package: %w", err)
		}
		manifest := fmt.Sprintf(`{"bundle_version":%q,"minimum_sidecar_version":%q,"command":"acr-mcp","args":["serve"],"mcp_commands":["context_for_task","source_evidence"]}`+"\n", bundle.BundleVersion, bundle.MinimumSidecarVersion)
		if err := os.WriteFile(filepath.Join(packageRoot, "package.v1.json"), []byte(manifest), 0o600); err != nil {
			return "", fmt.Errorf("write fixture manifest: %w", err)
		}
	}
	if err := copyFixtureFiles(filepath.Join(fixtureRoot, "clients"), filepath.Join(root, "clients")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if symlinkEscape {
		if err := os.RemoveAll(filepath.Join(root, "clients", "opencode")); err != nil {
			return "", fmt.Errorf("remove fixture package: %w", err)
		}
		if err := os.Symlink(filepath.Join(root, "clients", "cursor"), filepath.Join(root, "clients", "opencode")); err != nil {
			return "", invalidBundle("fixture.symlink_unsupported")
		}
	}
	return root, nil
}

func copyFixtureFiles(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o600)
	})
}

func clientBundleClassification(err error) string {
	var typed *ClientBundleError
	if errors.As(err, &typed) {
		return typed.Field
	}
	return ""
}
