package mcpclientfixtures

import (
	"os"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestMain keeps any in-process credential call in this package off the
// host-wide lifecycle lock file. The spawned acr-mcp children are separate
// processes and are isolated by the lock fixture build tag instead.
func TestMain(m *testing.M) {
	restore, err := sidecar.InstallIsolatedCredentialLifecycleLockForTesting()
	if err != nil {
		panic("install the package-wide lifecycle lock guard: " + err.Error())
	}
	code := m.Run()
	restore()
	os.Exit(code)
}
