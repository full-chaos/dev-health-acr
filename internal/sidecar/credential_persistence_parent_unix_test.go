//go:build darwin || linux

package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The credential file's parent is operator-supplied: ACR_API_TOKEN_FILE can
// name any path, so persistence used to chmod a directory ACR does not own.
// Pointing the token file at $HOME/token silently reduced the whole home
// directory to 0700 and would do the same to any other shared location.
func TestPersistCredentialLeavesPreexistingParentDirectoryUntouched(t *testing.T) {
	// Given
	parent := t.TempDir()
	unrelated := filepath.Join(parent, "unrelated.conf")
	if err := os.WriteFile(unrelated, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "token")
	token := validTestToken(56)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)

	// When
	persisted, err := PersistCredential(token)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Source != "file" || persisted.filePath != path {
		t.Fatalf("persisted credential = %+v, want the configured file locator", persisted)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if parentInfo.Mode().Perm() != 0o755 {
		t.Fatalf("pre-existing parent mode = %o, want 0755 unchanged", parentInfo.Mode().Perm())
	}
	unrelatedInfo, err := os.Stat(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedInfo.Mode().Perm() != 0o644 {
		t.Fatalf("unrelated file mode = %o, want 0644 unchanged", unrelatedInfo.Mode().Perm())
	}
	contents, err := os.ReadFile(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "keep me\n" {
		t.Fatalf("unrelated file contents = %q, want unchanged", contents)
	}
	tokenInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if tokenInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode = %o, want 0600", tokenInfo.Mode().Perm())
	}
}

// Leaving a pre-existing parent alone must not mean accepting any parent: a
// group- or world-writable directory lets another local user swap the
// credential file outright, so persistence fails closed instead of chmod-ing
// a directory it does not own.
func TestPersistCredentialRefusesGroupOrWorldWritableParentDirectory(t *testing.T) {
	for name, mode := range map[string]os.FileMode{"group writable": 0o770, "world writable": 0o707} {
		t.Run(name, func(t *testing.T) {
			// Given
			parent := t.TempDir()
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			path := filepath.Join(parent, "token")
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenKeyringDisabledEnvironment, "true")
			t.Setenv(TokenFileEnvironment, path)

			// When
			_, err := PersistCredential(validTestToken(57))

			// Then
			if err == nil {
				t.Fatalf("persistence accepted a %s credential directory", name)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("credential file written into a %s directory: %v", name, statErr)
			}
		})
	}
}

// The default ~/.acr directory is the one this package creates, so it is also
// the only one it may tighten.
func TestPersistCredentialRestrictsOnlyTheDirectoryItCreates(t *testing.T) {
	// Given
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, "")
	t.Setenv(APIURLEnvironment, "https://api.dev-health.example.com")
	t.Setenv("HOME", home)
	stubKeyringWriter(t, func(context.Context, string, string, string) error {
		return errKeyringWriteUnavailable
	})

	// When
	if _, err := PersistCredential(validTestToken(58)); err != nil {
		t.Fatal(err)
	}

	// Then
	createdInfo, err := os.Stat(filepath.Join(home, ".acr"))
	if err != nil {
		t.Fatal(err)
	}
	if createdInfo.Mode().Perm() != 0o700 {
		t.Fatalf("created credential directory mode = %o, want 0700", createdInfo.Mode().Perm())
	}
	homeInfo, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if homeInfo.Mode().Perm() != 0o755 {
		t.Fatalf("home directory mode = %o, want 0755 unchanged", homeInfo.Mode().Perm())
	}
}
