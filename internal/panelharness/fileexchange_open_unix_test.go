//go:build darwin || linux

package panelharness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestFileExchangeSelector_RejectsFIFOResponsePathWithoutHanging is a
// regression test for codex round-2 finding MEDIUM-3: opening a FIFO for
// reading blocks forever waiting for a writer unless O_NONBLOCK is set.
// Before the fix, a response path replaced with a named pipe would hang
// SelectReceipts past this test's own generous deadline; after the fix
// (openNoFollowNonBlocking + a regular-file fstat check), it must fail
// fast instead.
func TestFileExchangeSelector_RejectsFIFOResponsePathWithoutHanging(t *testing.T) {
	dir := t.TempDir()
	selector, err := NewFileExchangeSelector(dir, "anthropic/sol-max", 2*time.Second)
	if err != nil {
		t.Fatalf("NewFileExchangeSelector: %v", err)
	}

	go func() {
		// Wait for the request file to appear, then replace the expected
		// response path with a FIFO instead of a real response.
		requestsDir := filepath.Join(dir, "requests")
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			entries, err := os.ReadDir(requestsDir)
			if err == nil {
				for _, entry := range entries {
					// Skip the requester's own dot-prefixed temp-file-then-
					// rename artifact -- picking it up mid-flight would name
					// the wrong (temporary) response path, matching the
					// existing runFileExchangeResponder precedent's own skip.
					if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
						continue
					}
					respPath := filepath.Join(dir, "responses", entry.Name())
					_ = syscall.Mkfifo(respPath, 0o600)
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	_, err = selector.SelectReceipts(ctx, "Was Ask Dev ready to ship?", sampleStructureNeeds())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the response path is a FIFO, not a regular file")
	}
	// The whole test's own context is bounded at 4s and the selector's own
	// timeout at 2s -- if this took anywhere near either bound, os.Open
	// blocked on the FIFO instead of failing fast via O_NONBLOCK.
	if elapsed > 1500*time.Millisecond {
		t.Errorf("SelectReceipts took %s to reject a FIFO response path -- want a fast failure, not a block on open(2)", elapsed)
	}
}
