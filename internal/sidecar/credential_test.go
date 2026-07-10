package sidecar

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadCredentialPrefersEnvironment(t *testing.T) {
	t.Setenv(TokenEnvironment, "fcacr_environment")
	t.Setenv(TokenFileEnvironment, "/does/not/exist")
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "fcacr_environment" || result.Source != "environment" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialFromRestrictedFile(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fcacr_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenFileEnvironment, path)
	result, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != "fcacr_file" || result.Source != "file" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLoadCredentialRejectsLooseFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are not meaningful on Windows")
	}
	t.Setenv(TokenEnvironment, "")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("fcacr_file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenFileEnvironment, path)
	if _, err := LoadCredential(); err == nil {
		t.Fatal("loose credential-file permissions were accepted")
	}
}

func TestLoadCredentialRequiresConfiguration(t *testing.T) {
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	if _, err := LoadCredential(); err == nil {
		t.Fatal("missing credential configuration was accepted")
	}
}
