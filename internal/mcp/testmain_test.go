package mcp

import (
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestMain makes the host's real OS keychain unreachable for every test in this
// package.
//
// Two mechanisms, because this package tests through two boundaries. In-process
// tests reach the OS secret store only through the sidecar's keyring seams, so
// an empty in-memory store closes that path. The real-binary tests spawn
// cmd/acr-mcp as a child built from os.Environ(), where no seam of this process
// applies, so the disable flag is exported for the child to inherit.
//
// Today every subprocess test also supplies a shape-valid ACR_API_TOKEN, which
// wins precedence before the keyring is consulted at all. That is a property of
// those tests, not a guarantee: one test that stops setting it, or one code
// path that consults the keyring first, would put a real `security` or
// `secret-tool` lookup into the suite.
func TestMain(m *testing.M) {
	_ = os.Setenv(sidecar.TokenKeyringDisabledEnvironment, "true")
	_, restore, err := sidecar.InstallMemoryKeyringForTesting(nil)
	if err != nil {
		panic("install the package-wide in-memory keyring: " + err.Error())
	}
	code := m.Run()
	restore()
	os.Exit(code)
}
