//go:build darwin || linux

package sidecar

import (
	"path/filepath"
	"syscall"
	"testing"
)

// TestLoadConfigCACertPathRejectsFIFO proves the CA bundle path check
// rejects a named pipe outright rather than falling through to a read
// that would block indefinitely waiting for a writer (see
// loadCACertPool's own lstat-before-open in api_client.go).
//
// syscall.Mkfifo is defined only on Unix-like platforms, so this test
// lives in its own darwin/linux-tagged file -- matching
// boundedfile_unix.go's platform support -- instead of a runtime
// GOOS=="windows" skip inside config_ca_test.go: an unconditional call to
// it fails to *compile* for any other GOOS, including windows, which a
// runtime skip cannot fix.
func TestLoadConfigCACertPathRejectsFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(lookupFromMap(map[string]string{
		APIURLEnvironment:     "https://acr.example.com",
		CACertPathEnvironment: path,
	})); err == nil {
		t.Fatal("a FIFO CA bundle path was accepted")
	}
}
