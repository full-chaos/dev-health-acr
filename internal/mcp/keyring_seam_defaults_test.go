package mcp

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// TestPackageDefaultsKeepEveryTestAwayFromTheHostKeychain asserts the
// panicking-seam guarantee TestMain's doc comment describes is actually
// installed, rather than trusting the comment alone. Deleting the
// SetKeyringSeamsForTesting call in TestMain -- or replacing it with an empty
// in-memory store -- currently satisfies nothing in this package: nothing
// here asserted the seam, so a real `security`/`secret-tool` query against a
// developer's keychain would have read as coverage.
func TestPackageDefaultsKeepEveryTestAwayFromTheHostKeychain(t *testing.T) {
	if os.Getenv(sidecar.TokenKeyringDisabledEnvironment) != "true" {
		t.Fatalf("%s = %q at test start, want \"true\": subprocess tests spawned from this package inherit this and would otherwise query the host keychain", sidecar.TokenKeyringDisabledEnvironment, os.Getenv(sidecar.TokenKeyringDisabledEnvironment))
	}
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("the default keyring seam answered instead of panicking; an unintended keyring access would pass silently")
		}
		if !strings.Contains(fmt.Sprint(recovered), "without installing a stub") {
			t.Fatalf("keyring seam panic = %v, want the opt-in instruction", recovered)
		}
	}()
	_ = sidecar.ProbeKeyringSeamForTesting()
}

// TestPackageDefaultsClearAmbientACRConfiguration mirrors cmd/acr-mcp's guard
// of the same name: no ACR_ variable exported by whoever runs the suite may
// reach a test. An exported ACR_API_URL alone gives the keyring lookup a
// non-empty default account, which turns a test that never mentions the
// keyring into a real query against that developer's login keychain.
func TestPackageDefaultsClearAmbientACRConfiguration(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(name, "ACR_") {
			continue
		}
		if name == sidecar.TokenKeyringDisabledEnvironment {
			continue
		}
		t.Fatalf("ambient %s survived into the test process; the host's configuration would decide this suite's outcome", name)
	}
}
