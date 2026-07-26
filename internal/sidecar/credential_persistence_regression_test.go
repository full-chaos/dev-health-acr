package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistCredentialRemovesStaleKeyringCredentialBeforeFileFallback(t *testing.T) {
	// Given
	home := t.TempDir()
	successor := validTestToken(41)
	keyringPresent := true
	deleteCalls := 0
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, filepath.Join(home, "token"))
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		return errors.New("keyring write failed")
	})
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return keyringToken, keyringPresent, nil
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		deleteCalls++
		keyringPresent = false
		return nil
	})

	// When
	_, err := PersistCredential(successor)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	credential, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if deleteCalls != 1 {
		t.Fatalf("keyring deletion calls = %d, want 1", deleteCalls)
	}
	if credential.Token != successor || credential.Source != "file" {
		t.Fatalf("loaded credential = %#v, want successor file credential", credential)
	}
}

func TestReplaceCredential_updatesOnlyExistingFileSource(t *testing.T) {
	// Given
	home := t.TempDir()
	path := filepath.Join(home, "token")
	original := fileToken
	successor := validTestToken(42)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv("HOME", home)
	if err := os.WriteFile(path, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		t.Fatal("file-source replacement wrote to keyring")
		return nil
	})

	// When
	err := ReplaceCredential(CredentialResult{Token: original, Source: "file"}, successor)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != successor+"\n" {
		t.Fatalf("replacement file = %q, want successor", contents)
	}
}

func TestDeleteCredential_returnsExactFileCleanupLocation(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing", "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)

	// When
	err := DeleteCredential()

	// Then
	var cleanupErr *CredentialCleanupError
	if !errors.As(err, &cleanupErr) {
		t.Fatalf("cleanup error = %v, want typed cleanup error", err)
	}
	if cleanupErr.Location != path {
		t.Fatalf("cleanup location = %q, want %q", cleanupErr.Location, path)
	}
}

func TestRestoreCredential_restoresOriginalFileAfterPostRenameSyncFailure(t *testing.T) {
	// Given
	home := t.TempDir()
	path := filepath.Join(home, "token")
	original := CredentialResult{Token: fileToken, Source: "file"}
	successor := validTestToken(43)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, path)
	if err := os.WriteFile(path, []byte(original.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalSync := credentialDirectorySync
	calls := 0
	credentialDirectorySync = func(directory int) error {
		calls++
		if calls == 1 {
			return errors.New("directory sync failed after rename")
		}
		return originalSync(directory)
	}
	t.Cleanup(func() { credentialDirectorySync = originalSync })

	// When
	err := ReplaceCredential(original, successor)
	if err == nil {
		t.Fatal("replacement unexpectedly succeeded after post-rename sync failure")
	}
	restoreErr := RestoreCredential(original)

	// Then
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != original.Token+"\n" {
		t.Fatalf("restored file = %q, want original credential", contents)
	}
}
