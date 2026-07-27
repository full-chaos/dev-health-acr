//go:build darwin || linux

package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failFirstCredentialDirectorySync makes the next directory fsync fail and
// every later one succeed, which is exactly the post-rename ambiguity shape:
// the replacement is already visible at the target name, only its durability
// is unconfirmed. Later syncs must still work so the same test can prove the
// follow-up cleanup actually removes the file.
func failFirstCredentialDirectorySync(t *testing.T) *int {
	t.Helper()
	original := credentialDirectorySync
	calls := 0
	credentialDirectorySync = func(directory int) error {
		calls++
		if calls == 1 {
			return errors.New("directory sync failed after rename")
		}
		return original(directory)
	}
	t.Cleanup(func() { credentialDirectorySync = original })
	return &calls
}

// A directory fsync that fails after the rename is the only write failure
// that can leave a readable credential at the configured path. Reporting it
// with an empty CredentialResult told login "nothing was written", so the
// issued credential stayed on disk after login gave up on it. The persisted
// locator returned alongside the error is what makes that file purgeable.
func TestPersistCredentialReturnsPurgeableLocator_whenDirectorySyncFailsAfterRename(t *testing.T) {
	// Given
	home := t.TempDir()
	path := filepath.Join(home, "token")
	token := validTestToken(51)
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv("HOME", home)
	failFirstCredentialDirectorySync(t)

	// When
	persisted, err := PersistCredential(token)

	// Then
	if err == nil {
		t.Fatal("post-rename directory sync failure was reported as success")
	}
	if persisted.Source != "file" || persisted.filePath != path || persisted.Token != token {
		t.Fatalf("ambiguous persistence locator = %+v, want the candidate file locator at %q", persisted, path)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ambiguous write left no file to purge, so the locator would be untestable: %v", readErr)
	}
	if strings.TrimSpace(string(contents)) != token {
		t.Fatal("ambiguous write did not publish the issued credential")
	}
	if purgeErr := PurgeCredentialMaterial(persisted); purgeErr != nil {
		t.Fatalf("purge of the returned locator failed: %v", purgeErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file remains after purging the returned locator: %v", statErr)
	}
}
