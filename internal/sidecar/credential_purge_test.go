package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
func TestPurgeCredentialMaterialLeavesUnobservedFileAndKeyring_whenEnvironmentCannotBeRemoved(t *testing.T) {
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
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("unobserved credential file was modified: %v", statErr)
	}
	if _, ok := keyring.entries[memoryKeyringAddress(service, account)]; !ok {
		t.Fatal("unobserved keyring entry was modified")
	}
	if len(keyring.deletes) != 0 {
		t.Fatalf("keyring deletions = %v, want none", keyring.deletes)
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
	locations := credentialCleanupLocations(PurgeCredentialMaterial(current))

	// Then
	want := []string{TokenEnvironment}
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
	if locations := credentialCleanupLocations(errors.New("unrelated")); locations != nil {
		t.Fatalf("locations = %v, want none for an error that reported no location", locations)
	}
	if locations := credentialCleanupLocations(nil); locations != nil {
		t.Fatalf("locations = %v, want none for a nil error", locations)
	}
}

func TestPurgeAllCredentialMaterialRetainsConflictingCapturedFileLocator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	first := validTestToken(81)
	second := validTestToken(82)
	if err := os.WriteFile(path, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := PurgeAllCredentialMaterial([]CredentialResult{
		{Token: first, Source: "file", filePath: path},
		{Token: second, Source: "file", filePath: path},
	})

	if !errors.Is(err, errCredentialPurgeDuplicateLocator) {
		t.Fatalf("purge error = %v, want duplicate locator failure", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil || string(contents) != first+"\n" {
		t.Fatalf("conflicted locator was modified: contents=%q err=%v", contents, readErr)
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
			locations := credentialCleanupLocations(PurgeCredentialMaterial(current))

			// Then
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("cleanup deleted a file that is not an ACR credential target: %v", readErr)
			}
			if string(contents) != testCase.contents {
				t.Fatalf("unrelated file contents = %q, want unchanged", contents)
			}
			for _, location := range locations {
				if location == path {
					t.Fatalf("unobserved target was reported: %v", locations)
				}
			}
		})
	}
}

// A keyring-sourced logout must clear both the secret-store entry and any
// token file sitting underneath it. Asserting only that a deleter was called
// leaves the second half untested, and an entry that survives the call reads
// as a successful logout.
func TestPurgeCredentialMaterialLeavesUnobservedFileUnderneathKeyringCredential(t *testing.T) {
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
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("unobserved token file was modified: %v", statErr)
	}
}

// A purge target's proof used to stop at "the file/keyring entry currently
// holds a shape-valid ACR token", which cannot distinguish the token this
// logout enumerated and already revoked from a DIFFERENT, still-live token a
// concurrent login or refresh wrote to the same location afterward. Deleting
// on shape alone in that window strands a credential the server still
// honours while logout reports success. These two tests simulate exactly
// that race directly (write the replacement before calling purge) rather than
// with real goroutines, because the assertion is about what purge does when
// handed a stale expectation, not about timing.
func TestPurgeAllCredentialMaterialRetainsAFileWhoseTokenChangedSinceEnumeration(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	tokenA := validTestToken(90)
	tokenB := validTestToken(91)
	if err := os.WriteFile(path, []byte(tokenA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// current simulates what CollectCredentialMaterial captured before this
	// logout revoked tokenA server-side.
	current := CredentialResult{Token: tokenA, Source: "file", filePath: path}
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	// A concurrent login or refresh replaces the file's contents with a
	// different, still-live credential in the window between enumeration and
	// purge -- exactly the race PurgeAllCredentialMaterial must not delete
	// through.
	if err := os.WriteFile(path, []byte(tokenB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	err := PurgeAllCredentialMaterial([]CredentialResult{current})

	// Then
	var purgeErr *CredentialPurgeError
	if !errors.As(err, &purgeErr) {
		t.Fatalf("purge error = %v, want a typed aggregate purge error", err)
	}
	if !errors.Is(err, errCredentialPurgeTargetChanged) {
		t.Fatalf("purge error = %v, want errCredentialPurgeTargetChanged", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read token file after purge: %v", readErr)
	}
	if got := strings.TrimSpace(string(contents)); got != tokenB {
		t.Fatalf("token file after purge = %q, want the untouched replacement token %q; a changed-target purge must never delete", got, tokenB)
	}
}

func TestPurgeAllCredentialMaterialRetainsAKeyringEntryWhoseTokenChangedSinceEnumeration(t *testing.T) {
	// Given
	service := "acr-sidecar-test"
	account := "agent-a"
	tokenA := validTestToken(92)
	tokenB := validTestToken(93)
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	address := KeyringAddress{Service: service, Account: account}
	keyring, restore, err := InstallMemoryKeyringForTesting(map[KeyringAddress]string{address: tokenA})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	// current simulates what CollectCredentialMaterial captured before this
	// logout revoked tokenA server-side.
	current := CredentialResult{Token: tokenA, Source: "keyring", keyringService: service, keyringAccount: account}
	// A concurrent login or refresh replaces the keyring entry with a
	// different, still-live credential in the window between enumeration and
	// purge. Same package as MemoryKeyring, so this reaches its unexported
	// store directly rather than through a seam meant to model a real backend
	// call -- the point here is the state the target observes, not a second
	// realistic write path.
	keyring.entries[address] = tokenB

	// When
	purgeErr := PurgeAllCredentialMaterial([]CredentialResult{current})

	// Then
	if !errors.Is(purgeErr, errCredentialPurgeTargetChanged) {
		t.Fatalf("purge error = %v, want errCredentialPurgeTargetChanged", purgeErr)
	}
	if got := keyring.entries[address]; got != tokenB {
		t.Fatalf("keyring entry after purge = %q, want the untouched replacement token %q; a changed-target purge must never delete", got, tokenB)
	}
	if len(keyring.deletes) != 0 {
		t.Fatalf("keyring deletes = %v, want none: a changed target must never reach the deleter", keyring.deletes)
	}
}

func TestPurgeAllCredentialMaterialDeletesKeyringEntryWhenOnlyNewlineDiffers(t *testing.T) {
	// Given
	service := "acr-sidecar-test"
	account := "agent-a"
	token := validTestToken(94)
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	address := KeyringAddress{Service: service, Account: account}
	keyring, restore, err := InstallMemoryKeyringForTesting(map[KeyringAddress]string{address: token + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	current := CredentialResult{Token: token, Source: "keyring", keyringService: service, keyringAccount: account}

	// When
	err = PurgeAllCredentialMaterial([]CredentialResult{current})

	// Then
	if err != nil {
		t.Fatalf("purge error = %v", err)
	}
	if _, ok := keyring.entries[address]; ok {
		t.Fatalf("keyring entry %v remains after purge", address)
	}
}
