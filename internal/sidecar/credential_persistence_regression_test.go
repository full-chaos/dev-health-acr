package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistCredentialFallsBackToFileWhenKeyringIsUnavailable(t *testing.T) {
	// Given
	home := t.TempDir()
	successor := validTestToken(41)
	deleteCalls := 0
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, filepath.Join(home, "token"))
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		return errKeyringWriteUnavailable
	})
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, nil
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		deleteCalls++
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
	if deleteCalls != 0 {
		t.Fatalf("keyring deletion calls = %d, want 0", deleteCalls)
	}
	if credential.Token != successor || credential.Source != "file" {
		t.Fatalf("loaded credential = %#v, want successor file credential", credential)
	}
}

func TestPersistCredentialRejectsOperationalKeyringFailure(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	stubKeyringWriter(t, func(context.Context, string, string, string) error { return errKeyringWriteFailed })

	// When
	_, err := PersistCredential(validTestToken(42))

	// Then
	if err == nil {
		t.Fatal("operational keyring failure fell back to the credential file")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file exists after operational keyring failure: %v", statErr)
	}
}

func TestPersistCredentialRejectsUntrustedKeyringWriter_whenFallbackExists(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	stubKeyringWriter(t, func(context.Context, string, string, string) error { return ErrUntrustedExecutable })

	// When
	_, err := PersistCredential(validTestToken(43))

	// Then
	if !errors.Is(err, ErrUntrustedExecutable) {
		t.Fatalf("persist error = %v, want ErrUntrustedExecutable", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file exists after untrusted keyring writer: %v", statErr)
	}
}

func TestPersistCredentialRejectsTimedOutKeyringWriter_whenFallbackExists(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	stubKeyringWriter(t, func(context.Context, string, string, string) error { return context.DeadlineExceeded })

	// When
	_, err := PersistCredential(validTestToken(44))

	// Then
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persist error = %v, want context deadline exceeded", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file exists after timed-out keyring writer: %v", statErr)
	}
}

func TestReplaceCredentialUsesCapturedFileLocator_whenConfigurationChanges(t *testing.T) {
	// Given
	originalPath := filepath.Join(t.TempDir(), "original-token")
	otherPath := filepath.Join(t.TempDir(), "other-token")
	original := validTestToken(45)
	successor := validTestToken(46)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, originalPath)
	if err := os.WriteFile(originalPath, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenFileEnvironment, otherPath)

	// When
	err = ReplaceCredential(current, successor)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != successor+"\n" {
		t.Fatalf("captured credential file = %q, want successor", contents)
	}
	otherContents, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(otherContents) != fileToken+"\n" {
		t.Fatalf("reconfigured credential file = %q, want unchanged", otherContents)
	}
}

func TestVerifyCredentialRejectsWrongSource_whenTokensMatch(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	successor := validTestToken(47)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenKeyringAccountEnvironment, "agent-a")
	if err := os.WriteFile(path, []byte(successor+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return successor, true, nil
	})

	// When
	err = VerifyCredential(current, successor)

	// Then
	if err == nil {
		t.Fatal("same-token keyring result masked the selected file source")
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
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	err = ReplaceCredential(current, successor)

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

func TestDeleteCredentialIsIdempotent_whenCredentialFileIsMissing(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing", "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)

	// When
	err := DeleteCredential()

	// Then
	if err != nil {
		t.Fatalf("delete missing credential = %v, want nil", err)
	}
}

func TestRestoreCredential_restoresOriginalFileAfterPostRenameSyncFailure(t *testing.T) {
	// Given
	home := t.TempDir()
	path := filepath.Join(home, "token")
	originalToken := fileToken
	successor := validTestToken(43)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenFileEnvironment, path)
	if err := os.WriteFile(path, []byte(originalToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original, err := LoadCredential()
	if err != nil {
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
	err = ReplaceCredential(original, successor)
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
