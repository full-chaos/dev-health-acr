package main

import (
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestMain makes two host resources unreachable for every test in this
// package, whether or not the individual test remembered to say so.
//
// The OS keychain: credential resolution consults the keyring whenever
// ACR_API_TOKEN is unset or empty and the disable flag is not set, which
// several doctor and diagnostics tests do while pointing at a token file.
// On a developer's machine that is a real `security`/`secret-tool` lookup
// against the real login keychain -- a prompt at best, a host-dependent
// result at worst, and either way a test outcome decided by something
// outside the repository. Two independent defences, because either alone can
// be undone by one test: the disable flag is exported, which compiled
// subprocess tests inherit because they build their environment from
// os.Environ(); and the sidecar's keyring seams -- the only in-process path
// to an OS secret store -- are replaced with an empty in-memory store, so a
// test that re-enables the keyring still reaches memory rather than the
// host. Tests needing keyring contents install their own store over this one.
//
// The desktop browser: lifecycleBrowserOpen defaults to the real hardened
// opener, so an in-process login test that reaches the launch would start a
// browser on whoever is running the suite. The default here is a recorder,
// so the launch is observable without ever being real. Compiled-binary
// tests cannot be covered by this seam and pass --no-browser instead.
func TestMain(m *testing.M) {
	_ = os.Setenv("ACR_LOCAL_INDEX_PROVIDER", "disabled")
	_ = os.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	_, restoreKeyring, err := sidecar.InstallMemoryKeyringForTesting(nil)
	if err != nil {
		panic("install the package-wide in-memory keyring: " + err.Error())
	}
	lifecycleBrowserOpen = func(string) error { return nil }
	code := m.Run()
	restoreKeyring()
	os.Exit(code)
}
