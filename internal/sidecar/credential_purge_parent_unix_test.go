//go:build darwin || linux

package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

// Removal proves the target is an ACR credential and then unlinks it by name.
// On a group- or world-writable parent any local user can swap the entry
// between those two steps, so the unlink lands on whatever they put there --
// the same arbitrary-file delete the content proof exists to prevent, reached
// through the directory instead of the path. The credential must be left in
// place and the location reported, so an operator fixes the directory rather
// than discovering the file silently gone.
func TestPurgeCredentialMaterialRefusesToRemoveUnderASharedWritableParent(t *testing.T) {
	modes := []os.FileMode{0o770, 0o707, 0o777}
	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			// Given
			home := t.TempDir()
			parent := filepath.Join(home, "shared")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "token")
			if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenKeyringDisabledEnvironment, "true")
			t.Setenv(TokenFileEnvironment, path)
			current, err := LoadCredential()
			if err != nil {
				t.Fatal(err)
			}

			// When
			purgeErr := PurgeCredentialMaterial(current)

			// Then
			if purgeErr == nil {
				t.Fatal("a refused removal must surface as an error, not as a silent success")
			}
	locations := credentialCleanupLocations(purgeErr)
			if len(locations) != 1 || locations[0] != path {
				t.Fatalf("failed locations = %v, want exactly the refused credential path %q", locations, path)
			}
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("credential under a shared-writable parent was removed anyway: %v", readErr)
			}
			if string(contents) != fileToken+"\n" {
				t.Fatalf("credential contents = %q, want unchanged", contents)
			}

		})
	}
}
