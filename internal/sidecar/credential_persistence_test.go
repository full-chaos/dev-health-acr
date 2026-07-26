package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func stubKeyringWriter(t *testing.T, writer KeyringWriter) {
	t.Helper()
	original := currentKeyringWriter
	currentKeyringWriter = writer
	t.Cleanup(func() { currentKeyringWriter = original })
}

func stubKeyringDeleter(t *testing.T, deleter KeyringDeleter) {
	t.Helper()
	original := currentKeyringDeleter
	currentKeyringDeleter = deleter
	t.Cleanup(func() { currentKeyringDeleter = original })
}

func TestLoadCredentialUsesDefaultKeyringServiceAndNormalizedAPIURLAccount(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "HTTPS://API.Dev-Health.Example.Com/")
	t.Setenv("HOME", t.TempDir())
	stubKeyringLookup(t, func(_ context.Context, service, account string) (string, bool, error) {
		if service != defaultKeyringService {
			t.Fatalf("unexpected default keyring service: %q", service)
		}
		if account != "https://api.dev-health.example.com" {
			t.Fatalf("unexpected normalized keyring account: %q", account)
		}
		return keyringToken, true, nil
	})

	// When
	result, err := LoadCredential()

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != keyringToken || result.Source != "keyring" {
		t.Fatalf("unexpected credential result: %#v", result)
	}
}

func TestLoadCredentialUsesDefaultTokenFileWhenKeyringMisses(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".acr"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".acr", "token"), []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return "", false, nil
	})

	// When
	result, err := LoadCredential()

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != fileToken || result.Source != "file" {
		t.Fatalf("unexpected credential result: %#v", result)
	}
}

func TestLoadCredentialSkipsKeyringWhenDisabled(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".acr"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".acr", "token"), []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		t.Fatal("disabled keyring lookup was called")
		return "", false, nil
	})

	// When
	credential, err := LoadCredential()

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if credential.Token != fileToken || credential.Source != "file" {
		t.Fatalf("unexpected credential result: %#v", credential)
	}
}

func TestPersistCredentialFallsBackToRestrictedDefaultFileWhenKeyringWriteFails(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		return errors.New("keyring unavailable")
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error { return nil })

	// When
	result, err := PersistCredential(fileToken)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != fileToken || result.Source != "file" {
		t.Fatalf("unexpected persistence result: %#v", result)
	}
	parentInfo, err := os.Stat(filepath.Join(home, ".acr"))
	if err != nil {
		t.Fatal(err)
	}
	if parentInfo.Mode().Perm() != 0o700 {
		t.Fatalf("unexpected parent mode: %o", parentInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(filepath.Join(home, ".acr", "token"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected token mode: %o", fileInfo.Mode().Perm())
	}
}

func TestPersistCredentialSkipsKeyringWhenDisabled(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", home)
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		t.Fatal("disabled keyring writer was called")
		return nil
	})

	// When
	credential, err := PersistCredential(fileToken)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if credential.Token != fileToken || credential.Source != "file" {
		t.Fatalf("unexpected persistence result: %#v", credential)
	}
}

func TestPersistCredentialRejectsSymlinkAtDefaultTokenPath(t *testing.T) {
	// Given
	home := t.TempDir()
	parent := filepath.Join(home, ".acr")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(parent, "token")); err != nil {
		t.Fatal(err)
	}
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		return errors.New("keyring unavailable")
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error { return nil })

	// When
	_, err := PersistCredential(fileToken)

	// Then
	if err == nil {
		t.Fatal("symlinked default token path was accepted")
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "unchanged" {
		t.Fatal("credential persistence followed a symlink")
	}
}

func TestPersistCredentialAtomicallyReplacesDefaultFallbackFile(t *testing.T) {
	// Given
	home := t.TempDir()
	parent := filepath.Join(home, ".acr")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "token")
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		return errors.New("keyring unavailable")
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error { return nil })
	newToken := validTestToken(7)

	// When
	_, err := PersistCredential(newToken)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != newToken+"\n" {
		t.Fatal("fallback token was not replaced")
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "token" {
		t.Fatalf("atomic replacement left temporary files: %#v", entries)
	}
}

func TestDeleteCredentialRemovesDefaultFallbackFileWhenKeyringIsUnavailable(t *testing.T) {
	// Given
	home := t.TempDir()
	parent := filepath.Join(home, ".acr")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "token")
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		return errors.New("keyring unavailable")
	})

	// When
	err := DeleteCredential()

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback credential file remains after deletion: %v", err)
	}
}

func TestDeleteCredentialSkipsKeyringWhenDisabled(t *testing.T) {
	// Given
	home := t.TempDir()
	path := filepath.Join(home, ".acr", "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		t.Fatal("disabled keyring lookup was called")
		return "", false, nil
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		t.Fatal("disabled keyring deleter was called")
		return nil
	})

	// When
	err := DeleteCredential()

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fallback credential file remains after deletion: %v", err)
	}
}

func TestDeleteCredentialReturnsKeyringDeletionFailure(t *testing.T) {
	// Given
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringServiceEnvironment, "")
	t.Setenv(TokenKeyringAccountEnvironment, "")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", t.TempDir())
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return keyringToken, true, nil
	})
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		return errors.New("keyring delete failed")
	})

	// When
	err := DeleteCredential()

	// Then
	if err == nil {
		t.Fatal("keyring deletion failure was accepted")
	}
}
