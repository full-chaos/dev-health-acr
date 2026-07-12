//go:build darwin || linux

package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigCACertPathRejectsWorldWritableBundle is the end-to-end
// adversarial canary against a real file and a real fstat(2), not the
// synthetic fakeFileInfo unit tests in boundedfile_ownership_unix_test.go:
// a CA bundle any local user can modify must be rejected by
// Config.Validate (and therefore by `acr-mcp doctor`, which only ever
// calls LoadConfig), even though it is otherwise a perfectly valid,
// correctly-owned, well-formed PEM certificate.
func TestLoadConfigCACertPathRejectsWorldWritableBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, generateTestCACertPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile's requested mode is intersected with the process umask,
	// so an explicit os.Chmod is required to guarantee the exact
	// world-writable bit this test targets regardless of umask.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	})); err == nil {
		t.Fatal("a world-writable CA bundle was accepted by Config.Validate")
	}
}

// TestLoadConfigCACertPathRejectsGroupWritableBundle mirrors the
// world-writable canary above for the group-write bit specifically.
func TestLoadConfigCACertPathRejectsGroupWritableBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, generateTestCACertPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	})); err == nil {
		t.Fatal("a group-writable CA bundle was accepted by Config.Validate")
	}
}

// TestLoadConfigCACertPathAcceptsTrustedOwnerWorldReadableBundle proves the
// ownership check does not regress the common, legitimate case: a bundle
// owned by the current user (as every freshly-written temp file is) and
// world-*readable* -- not writable -- must still be accepted.
func TestLoadConfigCACertPathAcceptsTrustedOwnerWorldReadableBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, generateTestCACertPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	})); err != nil {
		t.Fatalf("a trusted-owner, world-readable CA bundle was rejected: %v", err)
	}
}
