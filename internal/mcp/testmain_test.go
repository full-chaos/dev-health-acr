package mcp

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestMain makes the host unable to influence, or be touched by, any test in
// this package.
//
// Ambient configuration: a developer who exports ACR_API_URL or any other ACR_
// variable changes what these tests resolve, and an exported ACR_API_URL alone
// gives the keyring lookup a non-empty default account -- enough to turn a test
// that never mentions the keyring into a real `security` or `secret-tool` query
// against that developer's login keychain. Every ACR_ variable is removed before
// the first test runs.
//
// The OS keychain, through two boundaries. In-process tests reach a secret store
// only through the sidecar's keyring seams, which are replaced with stubs that
// PANIC: an empty in-memory store would answer "no entry" and the test would
// pass without ever reporting that it consulted a secret store, while a panic
// makes reaching the seam a loud failure. The real-binary tests spawn
// cmd/acr-mcp as a child built from os.Environ(), where no seam of this process
// applies, so the disable flag is exported for the child to inherit.
//
// Today every subprocess test also supplies a shape-valid ACR_API_TOKEN, which
// wins precedence before the keyring is consulted at all. That is a property of
// those tests, not a guarantee: one test that stops setting it would otherwise
// put a real keychain lookup into the suite.
func TestMain(m *testing.M) {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(name, "ACR_") {
			_ = os.Unsetenv(name)
		}
	}
	_ = os.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	const seamPanic = "acr test reached the OS keyring seam without installing a stub: " +
		"this would query the host's real secret store."
	restore, err := sidecar.SetKeyringSeamsForTesting(
		func(context.Context, string, string) (string, bool, error) { panic(seamPanic + " [lookup]") },
		func(context.Context, string, string, string) error { panic(seamPanic + " [write]") },
		func(context.Context, string, string) error { panic(seamPanic + " [delete]") },
	)
	if err != nil {
		panic("install the package-wide keyring guard: " + err.Error())
	}
	code := m.Run()
	restore()
	os.Exit(code)
}
