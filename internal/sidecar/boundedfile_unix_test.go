//go:build darwin || linux

package sidecar

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This file exercises readBoundedRegularFile (boundedfile.go) with a
// syscall.Mkfifo-backed named pipe: the atomic O_NOFOLLOW|O_NONBLOCK
// open this package relies on rejects a FIFO without ever blocking on a
// missing writer. syscall.Mkfifo is defined only on Unix-like platforms,
// so these two tests live in their own darwin/linux-tagged file --
// matching boundedfile_unix.go's platform support -- instead of a
// runtime GOOS=="windows" skip inside boundedfile_test.go: an
// unconditional call to syscall.Mkfifo fails to *compile* for any other
// GOOS, including windows, which a runtime skip cannot fix.

// TestReadBoundedRegularFileRejectsFIFOWithoutBlocking proves the atomic
// O_NOFOLLOW|O_NONBLOCK open rejects a named pipe immediately: it must
// never reach a blocking read, which would hang indefinitely waiting for
// a writer on a FIFO with none connected. The timeout guard turns a
// regression here into a fast test failure instead of a hung test run.
func TestReadBoundedRegularFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := readBoundedRegularFile(path, 4096)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO path was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readBoundedRegularFile blocked opening a FIFO with no writer instead of rejecting it via O_NONBLOCK")
	}
}

// TestReadBoundedRegularFileFIFOSwapRaceNeverBlocks proves that under
// real, continuous concurrent path swapping between a regular file and a
// named pipe with no reader or writer connected, readBoundedRegularFile
// never blocks: every call must return (successfully or with an error)
// well within the test's overall deadline, even though some calls will
// race against the path being a FIFO at the exact moment of open(2).
func TestReadBoundedRegularFileFIFOSwapRaceNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "path")
	legit := []byte("legit-regular-content")
	if err := os.WriteFile(path, legit, 0o600); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			tmpFIFO := filepath.Join(dir, "tmp-fifo")
			if err := syscall.Mkfifo(tmpFIFO, 0o600); err != nil {
				continue
			}
			if err := os.Rename(tmpFIFO, path); err != nil {
				continue
			}
			tmpFile := filepath.Join(dir, "tmp-file")
			if err := os.WriteFile(tmpFile, legit, 0o600); err != nil {
				continue
			}
			_ = os.Rename(tmpFile, path)
		}
	}()

	done := make(chan struct{})
	go func() {
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			_, _, _ = readBoundedRegularFile(path, 4096)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(stop)
		wg.Wait()
		t.Fatal("readBoundedRegularFile blocked during a FIFO-swap race instead of rejecting the FIFO state via O_NONBLOCK")
	}
	close(stop)
	wg.Wait()
}
