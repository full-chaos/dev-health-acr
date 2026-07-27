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
// outside the repository. Disabling the keyring by default is a floor, not
// a ceiling: a test that specifically exercises keyring behavior overrides
// the variable and injects its own in-memory store
// (sidecar.InstallMemoryKeyringForTesting), which is still not the host's.
//
// The desktop browser: lifecycleBrowserOpen defaults to the real hardened
// opener, so an in-process login test that reaches the launch would start a
// browser on whoever is running the suite. The default here is a recorder,
// so the launch is observable without ever being real. Compiled-binary
// tests cannot be covered by this seam and pass --no-browser instead.
func TestMain(m *testing.M) {
	_ = os.Setenv("ACR_LOCAL_INDEX_PROVIDER", "disabled")
	_ = os.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	lifecycleBrowserOpen = func(string) error { return nil }
	os.Exit(m.Run())
}
