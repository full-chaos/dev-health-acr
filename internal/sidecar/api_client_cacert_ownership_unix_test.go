//go:build darwin || linux

package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadCACertPoolRejectsWorldWritableBundle proves loadCACertPool --
// the authoritative CA-loading path NewClient uses -- enforces the same
// ownership/write-access invariant Config.Validate does
// (config_ca_ownership_unix_test.go), so a Client built directly (bypassing
// doctor) cannot trust a CA bundle any local user could tamper with.
func TestLoadCACertPoolRejectsWorldWritableBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, generateTestCACertPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCACertPool(path); err == nil {
		t.Fatal("a world-writable CA bundle was accepted by loadCACertPool")
	}
}
