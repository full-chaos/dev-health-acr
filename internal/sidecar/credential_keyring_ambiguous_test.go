package sidecar

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func installTestMemoryKeyring(t *testing.T, entries map[KeyringAddress]string) *MemoryKeyring {
	t.Helper()
	keyring, restore, err := InstallMemoryKeyringForTesting(entries)
	if err != nil {
		t.Fatalf("install memory keyring: %v", err)
	}
	t.Cleanup(restore)
	return keyring
}

// A secret-store backend can commit an entry and still fail afterwards: the
// mutation lands and the collection write-out, the reply, or the process exit
// does not. PersistCredential used to answer that with a bare error and an
// empty result, which tells the caller "nothing was stored" while a readable
// credential sits in the keyring -- with no locator to purge it by, because the
// caller was never told which address was attempted.
func TestPersistCredentialReturnsTheCandidateKeyringLocator_whenTheStoreFailsAfterCommitting(t *testing.T) {
	// Given
	service := "acr-sidecar-test"
	account := "agent-a"
	address := KeyringAddress{Service: service, Account: account}
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	keyring := installTestMemoryKeyring(t, nil)
	commitFailure := errors.New("secret collection could not be written out")
	keyring.FailStoreAfterCommit(address, commitFailure)
	token := validTestToken(71)

	// When
	persisted, err := PersistCredential(token)

	// Then
	if err == nil {
		t.Fatal("an ambiguous keyring store reported success")
	}
	if !errors.Is(err, commitFailure) {
		t.Fatalf("persist error = %v, want the backend failure wrapped", err)
	}
	if stored := keyring.Entries()[address]; stored != token {
		t.Fatalf("keyring entry = %q, want the fixture to have committed the token; the ambiguity under test does not exist otherwise", stored)
	}
	if persisted.Source != "keyring" || persisted.keyringService != service || persisted.keyringAccount != account {
		t.Fatalf("returned locator = %+v, want the candidate keyring address so the caller can purge exactly it", persisted)
	}
	if persisted.Token != token {
		t.Fatal("returned locator lost the token, so the caller cannot revoke what it may have stored")
	}

	// And the returned locator is sufficient to purge the entry that was left
	// behind. This is the whole point of returning it: a caller that receives
	// only an error has nothing to clean up with.
	if purgeErr := PurgeCredentialMaterial(persisted); purgeErr != nil {
		t.Fatalf("purge of the returned locator failed: %v", purgeErr)
	}
	if _, remains := keyring.Entries()[address]; remains {
		t.Fatal("the ambiguously committed keyring entry survived the purge")
	}
}

// An unavailable backend is a different outcome: nothing was attempted, so
// there is no candidate address, and persistence must fall through to the
// token file rather than report a keyring locator that holds nothing.
func TestPersistCredentialFallsBackToTheFile_whenTheKeyringIsUnavailable(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenKeyringAccountEnvironment, "agent-a")
	t.Setenv(TokenFileEnvironment, path)
	stubKeyringWriter(t, func(_ context.Context, _, _, _ string) error { return errKeyringWriteUnavailable })
	token := validTestToken(72)

	// When
	persisted, err := PersistCredential(token)

	// Then
	if err != nil {
		t.Fatalf("persist with an unavailable keyring failed: %v", err)
	}
	if persisted.Source != "file" || persisted.filePath != path {
		t.Fatalf("persisted locator = %+v, want the fallback token file", persisted)
	}
}
