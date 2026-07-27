package sidecar

import (
	"os"
	"testing"
)

// TestMain makes the host's real OS keychain unreachable for every test in this
// package, whether or not the individual test remembered to say so.
//
// Credential resolution consults the keyring whenever ACR_API_TOKEN is unset or
// empty and the disable flag is not set, and several tests do exactly that
// while pointing at a token file -- the restore-after-sync-failure test among
// them. On a developer's machine that is a real `security` or `secret-tool`
// lookup against the login keychain: a prompt at best, a host-dependent result
// at worst, and either way a test outcome decided by something outside the
// repository.
//
// The three keyring seams are the only path from credential resolution,
// persistence, deletion, and purge to an OS secret store, so replacing them
// with an empty in-memory store closes that path for the whole package. Tests
// that need keyring contents install their own store over this one; tests that
// need the keyring to appear absent get an empty store rather than the host's.
//
// The disable flag is deliberately NOT forced here. Several tests in this
// package assert what happens when the keyring is enabled and do not set the
// flag themselves, so defaulting it to true would silently retarget them at a
// path they are not testing. Closing the seam is the guarantee; the flag stays
// a per-test choice.
//
// This does not weaken the tests that exercise the real backend command path:
// they call runKeyringCommand directly with an injected executable resolver, so
// they never resolve a real `security` or `secret-tool` either.
func TestMain(m *testing.M) {

	_, restore, err := InstallMemoryKeyringForTesting(nil)
	if err != nil {
		panic("install the package-wide in-memory keyring: " + err.Error())
	}
	code := m.Run()
	restore()
	os.Exit(code)
}
