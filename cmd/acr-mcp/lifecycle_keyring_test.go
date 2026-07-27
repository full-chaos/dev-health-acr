package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

const (
	lifecycleKeyringService = "acr-sidecar-test"
	lifecycleKeyringAccount = "agent-a"
)

func installLifecycleMemoryKeyring(t *testing.T, entries map[sidecar.KeyringAddress]string) *sidecar.MemoryKeyring {
	t.Helper()
	keyring, restore, err := sidecar.InstallMemoryKeyringForTesting(entries)
	if err != nil {
		t.Fatalf("install memory keyring: %v", err)
	}
	t.Cleanup(restore)
	return keyring
}

// A keyring store that commits and then fails leaves a readable credential
// behind. Login must treat that as a persistence failure -- the credential is
// not trustworthy as the selected source -- revoke it server-side, and then
// purge the exact entry the store reported as its candidate. Before
// PersistCredential returned that locator there was nothing to purge by, so a
// revoked-but-readable secret stayed in the secret store.
func TestLoginRevokesThenPurgesTheKeyringEntry_whenTheStoreFailsAfterCommitting(t *testing.T) {
	// Given
	token := validDoctorToken(97)
	address := sidecar.KeyringAddress{Service: lifecycleKeyringService, Account: lifecycleKeyringAccount}
	server, state := newLifecycleServerWithState(t, token, []string{"success"}, nil)
	withImmediateDevicePoll(t)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "false")
	t.Setenv(sidecar.TokenKeyringServiceEnvironment, lifecycleKeyringService)
	t.Setenv(sidecar.TokenKeyringAccountEnvironment, lifecycleKeyringAccount)
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	keyring := installLifecycleMemoryKeyring(t, nil)
	keyring.FailStoreAfterCommit(address, errors.New("secret collection could not be written out"))

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d for an ambiguous keyring store", code, lifecycleExitFailure)
	}
	_, _, revocations, _ := state.counts()
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want exactly one for a credential that may be readable locally", revocations)
	}
	if _, remains := keyring.Entries()[address]; remains {
		t.Fatalf("the ambiguously committed keyring entry survived login's cleanup: %v", keyring.Entries())
	}
	if len(keyring.Deletes()) != 1 || keyring.Deletes()[0] != address {
		t.Fatalf("keyring deletions = %v, want exactly the candidate address", keyring.Deletes())
	}
	if !strings.Contains(stderr, "could not be stored securely") {
		t.Fatalf("login stderr = %q, want the storage failure reported", stderr)
	}
	if strings.Contains(stderr, token) {
		t.Fatal("login stderr leaked the issued credential")
	}
}

// When the purge itself fails, the operator has to be told exactly where the
// revoked-but-readable material is. Discarding the purge result reported only
// "could not be stored securely" and named nothing at all.
func TestLoginReportsTheExactCleanupLocation_whenPurgingTheAmbiguousKeyringEntryFails(t *testing.T) {
	// Given
	token := validDoctorToken(98)
	address := sidecar.KeyringAddress{Service: lifecycleKeyringService, Account: lifecycleKeyringAccount}
	server, state := newLifecycleServerWithState(t, token, []string{"success"}, nil)
	withImmediateDevicePoll(t)
	t.Setenv(sidecar.APIURLEnvironment, server.URL)
	t.Setenv(sidecar.AllowInsecureLoopbackEnvironment, "true")
	t.Setenv(sidecar.TokenEnvironment, "")
	t.Setenv(sidecar.TokenKeyringDisabledEnvironment, "false")
	t.Setenv(sidecar.TokenKeyringServiceEnvironment, lifecycleKeyringService)
	t.Setenv(sidecar.TokenKeyringAccountEnvironment, lifecycleKeyringAccount)
	t.Setenv(sidecar.TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	keyring := installLifecycleMemoryKeyring(t, nil)
	keyring.FailStoreAfterCommit(address, errors.New("secret collection could not be written out"))
	keyring.FailDelete(address, errors.New("secret collection is locked"))

	// When
	code, stderr := captureStderr(t, func() int { return runCLI([]string{"login", "--no-browser"}) })

	// Then
	if code != lifecycleExitFailure {
		t.Fatalf("login exit code = %d, want %d", code, lifecycleExitFailure)
	}
	_, _, revocations, _ := state.counts()
	if revocations != 1 {
		t.Fatalf("remote revocations = %d, want exactly one", revocations)
	}
	if !strings.Contains(stderr, "local cleanup requires operator action at") {
		t.Fatalf("login stderr = %q, want the failed cleanup surfaced", stderr)
	}
	if !strings.Contains(stderr, lifecycleKeyringService) || !strings.Contains(stderr, lifecycleKeyringAccount) {
		t.Fatalf("login stderr = %q, want the exact keyring service and account named", stderr)
	}
	if strings.Contains(stderr, token) {
		t.Fatal("login stderr leaked the issued credential")
	}
}
