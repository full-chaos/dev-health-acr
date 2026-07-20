package mcpclientfixtures

import (
	"errors"
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
	if err := ValidateClientPackageRoots(filepath.Join("..", ".."), bundle); err != nil {
		t.Fatal(err)
	}
}

func TestClientBundle_rejects_invalid_contracts(t *testing.T) {
	for _, name := range []string{"invalid-bare-acr-mcp", "invalid-direct-api", "invalid-writeback-default", "invalid-preplan-default", "invalid-semver", "invalid-unsupported-command", "invalid-missing-clients", "invalid-credential-storage", "invalid-codegraph-command", "invalid-client-fork", "invalid-mutable-installer", "invalid-out-of-namespace"} {
		_, err := LoadClientBundle(filepath.Join("..", "..", "clients", "conformance", "fixtures", name, "client-bundle.v1.json"))
		if !errors.Is(err, ErrInvalidClientBundle) {
			t.Fatalf("%s error = %v", name, err)
		}
		var typed *ClientBundleError
		if !errors.As(err, &typed) {
			t.Fatalf("%s error is not typed", name)
		}
	}
}
