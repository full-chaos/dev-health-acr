package sidecar

import (
	"errors"
	"runtime"
	"testing"
)

// The persistence preflight used to name Windows alone, so login on any third
// platform passed the gate, asked the server to mint a one-time credential,
// and only then found it had nowhere to store it. The decision is pure, so
// every GOOS this repository can be built for is checked here on any host --
// no build tags, no skips, no platform-specific runner.
//
// The supported set must stay exactly the set of platforms that compile
// credential_persistence_unix.go (//go:build darwin || linux); everything else
// gets credential_persistence_other.go's unsupported stubs.
func TestCredentialPersistenceSupportedForAllowsOnlyPlatformsWithARealWriter(t *testing.T) {
	supported := map[string]bool{"darwin": true, "linux": true}
	platforms := []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
		"ios", "js", "linux", "netbsd", "openbsd", "plan9", "solaris", "wasip1",
		"windows", "zos", "", "Linux", "DARWIN", "linux/amd64",
	}
	for _, platform := range platforms {
		t.Run(platform, func(t *testing.T) {
			// When
			err := credentialPersistenceSupportedFor(platform)

			// Then
			if supported[platform] {
				if err != nil {
					t.Fatalf("credentialPersistenceSupportedFor(%q) = %v, want nil for a platform with a real credential writer", platform, err)
				}
				return
			}
			if !errors.Is(err, ErrCredentialPersistenceUnsupported) {
				t.Fatalf("credentialPersistenceSupportedFor(%q) = %v, want ErrCredentialPersistenceUnsupported", platform, err)
			}
		})
	}
}

// CredentialPersistenceSupported is the exported preflight login calls. It
// must be the pure decision applied to this build's GOOS and nothing else, so
// a future caller cannot get a different answer than the table above proves.
func TestCredentialPersistenceSupportedMatchesThePlatformTableForThisBuild(t *testing.T) {
	// When
	got := CredentialPersistenceSupported()

	// Then
	want := credentialPersistenceSupportedFor(runtime.GOOS)
	if !errors.Is(got, want) && !(got == nil && want == nil) {
		t.Fatalf("CredentialPersistenceSupported() = %v, want %v for GOOS %q", got, want, runtime.GOOS)
	}
}

// The unsupported error is operator-facing. It must not claim the platform is
// Windows, because it is now returned on every platform without a writer.
func TestCredentialPersistenceUnsupportedErrorNamesNoSpecificPlatform(t *testing.T) {
	if message := ErrCredentialPersistenceUnsupported.Error(); message != "acr: credential persistence is unsupported on this platform" {
		t.Fatalf("unsupported persistence error = %q, want a platform-agnostic message", message)
	}
}
