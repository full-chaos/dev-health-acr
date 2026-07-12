package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadCACertPoolRejectsOversizedBundle proves the documented
// maxCACertBundleBytes ceiling is enforced before the bundle is handed to
// AppendCertsFromPEM, not left to an unbounded read.
func TestLoadCACertPoolRejectsOversizedBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	oversized := make([]byte, maxCACertBundleBytes+1)
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCACertPool(path); err == nil {
		t.Fatal("an oversized CA bundle was accepted")
	}
}

// TestLoadCACertPoolFailsClosedOnInvalidPEM proves a bundle containing no
// parseable PEM certificate is rejected rather than silently producing an
// empty-of-that-bundle (but still system-trust-backed) pool.
func TestLoadCACertPoolFailsClosedOnInvalidPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, []byte("this is not PEM data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCACertPool(path); err == nil {
		t.Fatal("a CA bundle with no valid PEM certificates was accepted")
	}
}
