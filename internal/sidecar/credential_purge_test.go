package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// memoryKeyring is an in-memory stand-in for the OS secret store so a purge
// test can assert the entry is actually gone rather than that a deleter was
// merely called. Nothing here touches a host keychain.
type memoryKeyring struct {
	entries map[string]string
	deletes []string
}

func newMemoryKeyring(t *testing.T, seeded map[string]string) *memoryKeyring {
	t.Helper()
	keyring := &memoryKeyring{entries: map[string]string{}}
	for address, token := range seeded {
		keyring.entries[address] = token
	}
	stubKeyringDeleter(t, func(_ context.Context, service, account string) error {
		address := service + "\x00" + account
		keyring.deletes = append(keyring.deletes, address)
		if _, ok := keyring.entries[address]; !ok {
			return errors.New("keyring entry not found")
		}
		delete(keyring.entries, address)
		return nil
	})
	return keyring
}

func memoryKeyringAddress(service, account string) string { return service + "\x00" + account }

// An environment credential cannot be unset in the parent shell, but a process
// that exports ACR_API_TOKEN can still have a stale token file and keyring
// entry underneath it. Returning early on the environment source left both
// behind while logout reported that cleanup had failed.
func TestPurgeCredentialMaterialCleansFileAndKeyring_whenEnvironmentSourceCannotBeRemoved(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	service := "acr-sidecar-test"
	account := "agent-a"
	t.Setenv(TokenEnvironment, validTestToken(52))
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring := newMemoryKeyring(t, map[string]string{memoryKeyringAddress(service, account): keyringToken})
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if current.Source != "environment" {
		t.Fatalf("credential source = %q, want environment", current.Source)
	}

	// When
	err = PurgeCredentialMaterial(current)

	// Then
	var purgeErr *CredentialPurgeError
	if !errors.As(err, &purgeErr) {
		t.Fatalf("purge error = %v, want a typed aggregate purge error", err)
	}
	if got := purgeErr.Locations(); len(got) != 1 || got[0] != TokenEnvironment {
		t.Fatalf("failed locations = %v, want exactly [%s]", got, TokenEnvironment)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("configured credential file remains after purge: %v", statErr)
	}
	if _, ok := keyring.entries[memoryKeyringAddress(service, account)]; ok {
		t.Fatal("configured keyring entry remains after purge")
	}
	if len(keyring.deletes) != 1 {
		t.Fatalf("keyring deletions = %v, want exactly one", keyring.deletes)
	}
}

// The aggregate must name every failure, not the first one: an operator who
// is told about one stranded location stops looking for the others.
func TestCredentialCleanupLocationsReportsEveryFailedLocation(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "missing-directory", "token")
	service := "acr-sidecar-test"
	account := "agent-a"
	t.Setenv(TokenEnvironment, validTestToken(53))
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		return errors.New("keyring delete failed")
	})
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}

	// When
	locations := CredentialCleanupLocations(PurgeCredentialMaterial(current))

	// Then
	want := []string{TokenEnvironment, credentialKeyringLocation(service, account)}
	sort.Strings(locations)
	sort.Strings(want)
	if len(locations) != len(want) {
		t.Fatalf("failed locations = %v, want %v", locations, want)
	}
	for index := range want {
		if locations[index] != want[index] {
			t.Fatalf("failed locations = %v, want %v", locations, want)
		}
	}
}

func TestCredentialCleanupLocationsReturnsNothingForAnUnreportedError(t *testing.T) {
	if locations := CredentialCleanupLocations(errors.New("unrelated")); locations != nil {
		t.Fatalf("locations = %v, want none for an error that reported no location", locations)
	}
	if locations := CredentialCleanupLocations(nil); locations != nil {
		t.Fatalf("locations = %v, want none for a nil error", locations)
	}
}

// ACR_API_TOKEN_FILE is an operator-supplied path and cleanup used to unlink
// whatever regular file sat at it, so a mistyped or hostile value turned
// logout into an arbitrary-file delete. Every boundary that admits a file as
// an ACR credential for reading must also gate its removal.
func TestPurgeCredentialMaterialLeavesFileThatIsNotAnACRCredentialTarget(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		mode     os.FileMode
		skip     bool
	}{
		{name: "wrong token shape", contents: "-----BEGIN OPENSSH PRIVATE KEY-----\n", mode: 0o600},
		{name: "acr shape but group readable", contents: validTestToken(54) + "\n", mode: 0o640, skip: runtime.GOOS == "windows"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.skip {
				t.Skip("POSIX permission boundary")
			}

			// Given
			path := filepath.Join(t.TempDir(), "unrelated")
			if err := os.WriteFile(path, []byte(testCase.contents), testCase.mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv(TokenEnvironment, validTestToken(55))
			t.Setenv(TokenKeyringDisabledEnvironment, "true")
			t.Setenv(TokenFileEnvironment, path)
			current, err := LoadCredential()
			if err != nil {
				t.Fatal(err)
			}

			// When
			locations := CredentialCleanupLocations(PurgeCredentialMaterial(current))

			// Then
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("cleanup deleted a file that is not an ACR credential target: %v", readErr)
			}
			if string(contents) != testCase.contents {
				t.Fatalf("unrelated file contents = %q, want unchanged", contents)
			}
			found := false
			for _, location := range locations {
				if location == path {
					found = true
				}
			}
			if !found {
				t.Fatalf("failed locations = %v, want the refused target %q reported", locations, path)
			}
		})
	}
}

// A keyring-sourced logout must clear both the secret-store entry and any
// token file sitting underneath it. Asserting only that a deleter was called
// leaves the second half untested, and an entry that survives the call reads
// as a successful logout.
func TestPurgeCredentialMaterialClearsBothTheKeyringEntryAndTheFileUnderneathIt(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	service := "acr-sidecar-test"
	account := "agent-a"
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenFileEnvironment, path)
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring := newMemoryKeyring(t, map[string]string{memoryKeyringAddress(service, account): keyringToken})
	stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
		return keyringToken, true, nil
	})
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if current.Source != "keyring" {
		t.Fatalf("credential source = %q, want keyring", current.Source)
	}

	// When
	err = PurgeCredentialMaterial(current)

	// Then
	if err != nil {
		t.Fatalf("purge of a keyring credential failed: %v", err)
	}
	if len(keyring.entries) != 0 {
		t.Fatalf("keyring entries remain after purge: %v", keyring.deletes)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("token file underneath the keyring credential remains: %v", statErr)
	}
}
