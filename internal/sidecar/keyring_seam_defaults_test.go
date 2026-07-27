package sidecar

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestPackageDefaultsKeepEveryTestAwayFromTheHostKeychain asserts the
// panicking-seam guarantee TestMain's doc comment describes is actually
// installed, rather than trusting the comment alone.
//
// Unlike cmd/acr-mcp and internal/mcp, this package does not force
// ACR_API_TOKEN_KEYRING_DISABLED in TestMain -- several tests here assert
// enabled-keyring behavior without setting it, so forcing the flag would
// silently retarget them at a path they are not testing (see this package's
// own testmain_test.go). Only the seam itself is pinned here.
//
// currentKeyringLookup is called directly rather than through
// ProbeKeyringSeamForTesting: this test lives in the same package as both, so
// going through the exported wrapper would make this test partly a test of
// that wrapper instead of the seam TestMain actually installs.
func TestPackageDefaultsKeepEveryTestAwayFromTheHostKeychain(t *testing.T) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("the default keyring seam answered instead of panicking; an unintended keyring access would pass silently")
		}
		if !strings.Contains(fmt.Sprint(recovered), "without installing a stub") {
			t.Fatalf("keyring seam panic = %v, want the opt-in instruction", recovered)
		}
	}()
	_, _, _ = currentKeyringLookup(context.Background(), "acr-keyring-seam-probe", "acr-keyring-seam-probe")
}

// TestPackageDefaultsClearAmbientACRConfiguration asserts the ambient-clear
// guarantee TestMain's doc comment describes: no ACR_ variable exported by
// whoever runs `go test` may reach a test in this package. An exported
// ACR_API_URL alone gives the keyring lookup a non-empty default account,
// turning a test that never mentions the keyring into a real query against
// that developer's login keychain.
func TestPackageDefaultsClearAmbientACRConfiguration(t *testing.T) {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(name, "ACR_") {
			continue
		}
		t.Fatalf("ambient %s survived into the test process; the host's configuration would decide this suite's outcome", name)
	}
}
