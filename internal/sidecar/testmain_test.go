package sidecar

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestMain makes the host unable to influence, or be touched by, any test in
// this package. Two hazards, both of which are ordinarily invisible at the call
// site that depends on them.
//
// Ambient configuration: a developer who exports ACR_API_URL, ACR_API_TOKEN, or
// any other ACR_ variable in their shell changes what these tests resolve. An
// exported ACR_API_URL alone is enough to give the keyring lookup a non-empty
// default account, which turns a test that never mentions the keyring into a
// real `security` or `secret-tool` query against that developer's login
// keychain. Every ACR_ variable is therefore removed before the first test runs.
//
// The keyring seams: these three are the only path from credential resolution,
// persistence, deletion, and purge to an OS secret store. They are replaced with
// stubs that PANIC rather than with an empty in-memory store. An empty store is
// a silent answer -- a test that reaches the seam without meaning to gets
// "no entry" and passes, and the fact that it consulted a secret store at all is
// never reported. A panic makes reaching the seam a loud, unmissable failure, so
// keyring access in this package is opt-in per test (stubKeyringLookup,
// stubKeyringWriter, stubKeyringDeleter, newMemoryKeyring, or
// InstallMemoryKeyringForTesting) and never ambient.
//
// The disable flag is deliberately not forced: several tests assert
// enabled-keyring behavior without setting it, and forcing it would silently
// retarget them at a path they are not testing. The panicking seam is the
// guarantee; the flag stays a per-test choice.
//
// This does not weaken the tests that exercise the real backend command path:
// they call runKeyringCommand directly with an injected executable resolver, so
// they never resolve a real `security` or `secret-tool` either.
func TestMain(m *testing.M) {
	clearAmbientACREnvironment()
	installPanickingKeyringSeams()
	os.Exit(m.Run())
}

// clearAmbientACREnvironment removes every ACR_-prefixed variable inherited from
// the shell that started `go test`.
func clearAmbientACREnvironment() {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(name, "ACR_") {
			_ = os.Unsetenv(name)
		}
	}
}

const keyringSeamPanic = "acr test reached the OS keyring seam without installing a stub: " +
	"this would query the host's real secret store. Install one explicitly " +
	"(stubKeyringLookup/stubKeyringWriter/stubKeyringDeleter, newMemoryKeyring, " +
	"or InstallMemoryKeyringForTesting), or set ACR_API_TOKEN_KEYRING_DISABLED=true."

func installPanickingKeyringSeams() {
	currentKeyringLookup = func(context.Context, string, string) (string, bool, error) {
		panic(keyringSeamPanic + " [lookup]")
	}
	currentKeyringWriter = func(context.Context, string, string, string) error {
		panic(keyringSeamPanic + " [write]")
	}
	currentKeyringDeleter = func(context.Context, string, string) error {
		panic(keyringSeamPanic + " [delete]")
	}
}
