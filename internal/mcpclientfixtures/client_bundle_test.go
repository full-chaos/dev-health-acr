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
	if err := ValidateClientPackageRoots(filepath.Join("..", ".."), bundle); err != nil {
		t.Fatal(err)
	}
}

func TestClientBundle_rejects_invalid_contracts(t *testing.T) {
	for _, name := range []string{"invalid-bare-acr-mcp", "invalid-direct-api", "invalid-writeback-default"} {
		if _, err := LoadClientBundle(filepath.Join("..", "..", "clients", "conformance", "fixtures", name, "client-bundle.v1.json")); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
