package sidecar

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This file covers loadFromFile's bounded-read behavior specifically (the
// shared readBoundedRegularFile implementation, see boundedfile.go): type
// checks (regular file only, no FIFO/device/symlink/directory), the
// maxTokenFileBytes size ceiling, and the POSIX permission check. Token
// source precedence (environment/keyring/file) and shape-validation tests
// live in credential_test.go; validTestToken, fileToken, and
// writeTokenFile are defined there and shared across both files.

// The file source is only reached when nothing higher in the precedence order
// answers, so this asserts the resolved Source as well as the token: the right
// value from the wrong source is not the same result. It also names its
// dependency on an empty keyring address -- with an ambient ACR_API_URL
// exported, the default keyring account is non-empty and this test queries a
// secret store on its way to the file. TestMain clears the ambient value, and
// the seam panics if it is ever reached without a stub.
func TestLoadCredentialFromRestrictedFile(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(APIURLEnvironment, "")
	t.Setenv(TokenFileEnvironment, writeTokenFile(t, fileToken+"\n"))
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != fileToken {
		t.Fatalf("credential token = %q, want the token file's contents", result.Token)
	}
	if result.Source != "file" {
		t.Fatalf("credential source = %q, want file", result.Source)
	}
	if result.filePath == "" {
		t.Fatal("file credential carries no path, so nothing downstream can verify or remove it")
	}
}

func TestLoadCredentialRejectsLooseFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are not meaningful on Windows")
	}
	t.Setenv(TokenEnvironment, "")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenFileEnvironment, path)
	if _, err := LoadCredential(); err == nil {
		t.Fatal("loose credential-file permissions were accepted")
	}
}

// TestLoadCredentialRejectsOversizedTokenFile proves the documented
// maxTokenFileBytes ceiling is enforced before the file is read in full,
// mirroring TestLoadCACertPoolRejectsOversizedBundle in api_client_test.go
// for the shared readBoundedRegularFile implementation.
func TestLoadCredentialRejectsOversizedTokenFile(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	oversized := make([]byte, maxTokenFileBytes+1)
	for index := range oversized {
		oversized[index] = 'a'
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenFileEnvironment, path)
	if _, err := LoadCredential(); err == nil {
		t.Fatal("an oversized token file was accepted")
	}
}

// TestLoadCredentialRejectsTokenFileSymlink proves the token file check
// rejects a symlink even when it resolves to an otherwise-valid, correctly
// permissioned regular file: no symlink indirection is trusted for this
// security-sensitive path, closing off the class of TOCTOU attack where the
// link target is swapped between validation and the later read.
func TestLoadCredentialRejectsTokenFileSymlink(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	dir := t.TempDir()
	target := filepath.Join(dir, "real-token")
	if err := os.WriteFile(target, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "token-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenFileEnvironment, link)
	if _, err := LoadCredential(); err == nil {
		t.Fatal("a symlinked token file path was accepted")
	}
}

// TestLoadCredentialRejectsTokenFileDirectory proves a directory path is
// rejected rather than falling through to a read that would fail with a
// less specific error deeper in the call stack.
func TestLoadCredentialRejectsTokenFileDirectory(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, t.TempDir())
	if _, err := LoadCredential(); err == nil {
		t.Fatal("a directory token file path was accepted")
	}
}
