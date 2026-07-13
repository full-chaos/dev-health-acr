package entitlements

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRestrictedFile_rejects_secret_symlink(t *testing.T) {
	// Given
	directory := t.TempDir()
	target := filepath.Join(directory, "token")
	if err := os.WriteFile(target, []byte("token"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(directory, "token-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	// When
	_, err := readRestrictedFile(link, 1024)

	// Then
	if err == nil {
		t.Fatal("readRestrictedFile() error = nil; want symlink rejection")
	}
}
