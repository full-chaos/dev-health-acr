//go:build darwin || linux

package sidecar

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestLoadCACertPoolRejectsFIFOWithoutBlocking proves loadCACertPool's own
// lstat-before-open rejects a named pipe immediately: it must never reach
// os.Open, which would block indefinitely waiting for a writer on a FIFO
// with none connected. The timeout guard turns a regression here into a
// fast test failure instead of a hung test run.
//
// syscall.Mkfifo is defined only on Unix-like platforms, so this test
// lives in its own darwin/linux-tagged file -- matching
// boundedfile_unix.go's platform support -- instead of a runtime
// GOOS=="windows" skip inside the shared test file: an unconditional call
// to it fails to *compile* for any other GOOS, including windows, which a
// runtime skip cannot fix.
func TestLoadCACertPoolRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ca.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := loadCACertPool(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO CA bundle path was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loadCACertPool blocked opening a FIFO with no writer instead of rejecting it via lstat")
	}
}
