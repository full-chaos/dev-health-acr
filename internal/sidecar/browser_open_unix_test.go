//go:build darwin || linux

package sidecar

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// browserOpenerName is the opener production selects for this platform.
func browserOpenerName() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

// The opener launches an arbitrary user-configured browser, so two things must
// hold at the exec boundary: the binary comes from trusted resolution and never
// from PATH, and the child sees an allowlisted environment rather than this
// process's own -- which carries the ACR API bearer credential.
//
// No host browser is started here: the resolver seam is injected to point at a
// recording shell script, and the PATH entry planted alongside it is a tripwire
// that must never run.
func TestOpenVerificationURIUsesTrustedResolutionAndAMinimalEnvironment(t *testing.T) {
	// Given
	requireSh(t)
	home := t.TempDir()
	recorded := filepath.Join(home, "opener.record")
	tripwire := filepath.Join(home, "tripwire.record")
	trustedDirectory := t.TempDir()
	pathDirectory := t.TempDir()
	opener := filepath.Join(trustedDirectory, browserOpenerName())
	if err := os.WriteFile(opener, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HOME/opener.record\"\nenv >> \"$HOME/opener.record\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"open", "xdg-open"} {
		planted := filepath.Join(pathDirectory, name)
		if err := os.WriteFile(planted, []byte("#!/bin/sh\nprintf 'launched\\n' > \"$HOME/tripwire.record\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", pathDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ACR_API_TOKEN", validTestToken(61))
	t.Setenv("ACR_API_TOKEN_FILE", filepath.Join(home, "token"))
	injectExecutableResolver(t, browserOpenerName(), opener)
	uri := "https://acr.example.com/device?code=ABCDEFGH"

	// When
	if err := OpenVerificationURI(uri); err != nil {
		t.Fatalf("OpenVerificationURI: %v", err)
	}

	// Then
	contents := waitForOpenerRecord(t, recorded)
	lines := strings.Split(strings.TrimRight(contents, "\n"), "\n")
	if len(lines) == 0 || lines[0] != uri {
		t.Fatalf("opener argv = %q, want exactly the verification address", lines)
	}
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "ACR_") {
			t.Fatalf("opener environment carried an ACR variable: %q", strings.SplitN(line, "=", 2)[0])
		}
	}
	if _, err := os.Stat(tripwire); !os.IsNotExist(err) {
		t.Fatalf("a PATH-resolved opener was launched: %v", err)
	}
}

// waitForOpenerRecord blocks until the recording script has written its output.
// A timeout here is a failure, never a silently accepted empty result: an
// unobserved launch would read as coverage while proving nothing.
func waitForOpenerRecord(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(contents), "\n") {
			return string(contents)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("browser opener fixture never recorded an invocation at %s", path)
	return ""
}
