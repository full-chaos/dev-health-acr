package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestMain makes the host unable to influence, or be touched by, any test in
// this package. Three hazards, each ordinarily invisible at the call site that
// depends on it.
//
// Ambient configuration: a developer who exports ACR_API_URL, ACR_API_TOKEN, or
// any other ACR_ variable changes what these tests resolve. An exported
// ACR_API_URL alone gives the keyring lookup a non-empty default account, which
// turns a doctor or diagnostics test that never mentions the keyring into a real
// `security` or `secret-tool` query against that developer's login keychain.
// Every ACR_ variable is removed before the first test runs; the two this
// package's own defaults depend on are then set explicitly.
//
// The OS keychain: the sidecar's three keyring seams are the only in-process
// path to a secret store, and they are replaced with stubs that PANIC rather
// than with an empty in-memory store. An empty store answers "no entry" and the
// test passes without ever reporting that it consulted a secret store; a panic
// makes reaching the seam a loud, unmissable failure. Keyring access is opt-in
// per test through installLifecycleMemoryKeyring. The disable flag is exported
// as well, because compiled subprocess tests build their environment from
// os.Environ() and no seam of this process applies to them.
//
// The desktop browser: lifecycleBrowserOpen defaults to the real hardened
// opener, so an in-process login test that reaches the launch would start a
// browser on whoever is running the suite. The default here is an inert stub;
// compiled-binary tests cannot be covered by a seam and pass --no-browser.
func TestMain(m *testing.M) {
	clearAmbientACREnvironment()
	_ = os.Setenv("ACR_LOCAL_INDEX_PROVIDER", "disabled")
	_ = os.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	restoreKeyring := installPanickingKeyringSeams()
	lifecycleBrowserOpen = func(string) error { return nil }
	code := m.Run()
	restoreKeyring()
	os.Exit(code)
}

// isSubprocessEntryPointMarker reports whether name selects a CLI entry point
// in a re-exec of this test binary rather than configuring the sidecar.
//
// These markers must survive clearAmbientACREnvironment. Clearing them in the
// child's own TestMain made every subprocess test run the Go test harness
// instead of the command under test, and then compare its output against
// "PASS". No sidecar configuration variable has this shape, so the pattern
// cannot accidentally preserve one.
func isSubprocessEntryPointMarker(name string) bool {
	return strings.HasPrefix(name, "ACR_MCP_") && strings.HasSuffix(name, "_PROCESS")
}

func clearAmbientACREnvironment() {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && strings.HasPrefix(name, "ACR_") && !isSubprocessEntryPointMarker(name) {
			_ = os.Unsetenv(name)
		}
	}
}

const keyringSeamPanic = "acr test reached the OS keyring seam without installing a stub: " +
	"this would query the host's real secret store. Install one explicitly with " +
	"installLifecycleMemoryKeyring, or leave ACR_API_TOKEN_KEYRING_DISABLED=true."

func installPanickingKeyringSeams() func() {
	restore, err := sidecar.SetKeyringSeamsForTesting(
		func(context.Context, string, string) (string, bool, error) { panic(keyringSeamPanic + " [lookup]") },
		func(context.Context, string, string, string) error { panic(keyringSeamPanic + " [write]") },
		func(context.Context, string, string) error { panic(keyringSeamPanic + " [delete]") },
	)
	if err != nil {
		panic("install the package-wide keyring guard: " + err.Error())
	}
	return restore
}
