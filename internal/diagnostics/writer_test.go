package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestWriteBundleRejectsEmptyPath proves an empty destination path fails
// closed rather than, say, defaulting to a well-known filename.
func TestWriteBundleRejectsEmptyPath(t *testing.T) {
	if err := WriteBundle("", []byte("data")); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for an empty path, got: %v", err)
	}
}

// TestWriteBundleRejectsOversizedData proves the writer bounds size
// defensively even though Build already enforces MaxBundleBytes.
func TestWriteBundleRejectsOversizedData(t *testing.T) {
	dir := realTempDir(t)
	oversized := make([]byte, MaxBundleBytes+1)
	path := filepath.Join(dir, "bundle.tar")
	if err := WriteBundle(path, oversized); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for an oversized payload, got: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected no file to be written for a rejected oversized payload")
	}
}

// TestWriteBundleRejectsExistingDirectoryDestination proves a destination
// path that is already a directory fails closed instead of erroring
// deep inside a partially-completed rename.
func TestWriteBundleRejectsExistingDirectoryDestination(t *testing.T) {
	dir := realTempDir(t)
	destDir := filepath.Join(dir, "bundle.tar")
	if err := os.Mkdir(destDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(destDir, []byte("data")); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for a directory destination, got: %v", err)
	}
}

// TestWriteBundleRejectsExistingRegularFileAtDestination is the direct
// regression lock for the no-clobber fix: a destination that already
// exists as an ordinary regular file (not a symlink, not a directory)
// must fail closed rather than be silently overwritten by the atomic
// hard-link finalization, and its original content must survive
// byte-for-byte, with no stray temporary file left behind.
func TestWriteBundleRejectsExistingRegularFileAtDestination(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "bundle.tar")
	original := []byte("original bundle contents that must survive untouched")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteBundle(path, []byte("new contents that must never land")); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for a pre-existing regular file, got: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(original) {
		t.Fatal("expected the pre-existing regular file's content to be preserved untouched")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected no leftover temporary file alongside the untouched destination, got: %v", entries)
	}
}

// TestWriteBundleRejectsMissingParentDirectory proves a nonexistent parent
// directory fails closed rather than being silently created.
func TestWriteBundleRejectsMissingParentDirectory(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "does-not-exist", "bundle.tar")
	if err := WriteBundle(path, []byte("data")); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for a missing parent directory, got: %v", err)
	}
}

// TestWriteBundleRejectsSymlinkAtDestination is the canary for "no
// symlink overwrite": a symlink sitting at the destination path must
// never be silently replaced or followed, even though os.Rename would
// itself already refuse to follow it through to whatever it points at.
func TestWriteBundleRejectsSymlinkAtDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	dir := realTempDir(t)
	target := filepath.Join(dir, "real-target")
	if err := os.WriteFile(target, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bundle.tar")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := WriteBundle(link, []byte("data")); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for a symlinked destination, got: %v", err)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "do not touch" {
		t.Fatal("expected the symlink target to be left untouched")
	}
}

// TestWriteBundleRejectsSymlinkParentDirectory proves a symlinked parent
// directory component is also rejected, not just a symlinked leaf.
func TestWriteBundleRejectsSymlinkParentDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	root := realTempDir(t)
	realDir := filepath.Join(root, "real-dir")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked-dir")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(linkedDir, "bundle.tar")
	if err := WriteBundle(path, []byte("data")); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for a symlinked parent directory, got: %v", err)
	}
}

// TestWriteBundleWritesAtomicallyWithRestrictedMode proves a successful
// write lands the exact bytes at the destination with mode 0600 and
// leaves no temporary file behind in the destination directory.
func TestWriteBundleWritesAtomicallyWithRestrictedMode(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "bundle.tar")
	payload := []byte("bundle contents")

	if err := WriteBundle(path, payload); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(payload) {
		t.Fatal("expected the destination file to contain exactly the written payload")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".acr-diagnostics-") {
			t.Fatalf("expected no leftover temporary file, found: %s", entry.Name())
		}
	}
}

// TestWriteBundleRejectsEmptyData proves an empty payload -- which Build
// never produces, but a hypothetical future caller could pass directly --
// fails closed rather than writing a zero-byte file.
func TestWriteBundleRejectsEmptyData(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "bundle.tar")
	if err := WriteBundle(path, nil); !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("expected ErrDestinationInvalid for empty data, got: %v", err)
	}
}

// TestWriteBundleConcurrentWritersOnlyOneSucceeds is the concurrency
// regression lock: many goroutines racing to WriteBundle the same
// destination path simultaneously must produce exactly one success and
// every other call must fail closed with ErrDestinationInvalid -- never
// a second writer silently clobbering the first, and never more than one
// file surviving at the destination. This is precisely the race an
// earlier Lstat-then-Rename implementation could not close (Rename
// unconditionally replaces its target); os.Link's atomic no-clobber
// semantics in atomicWrite are what make this deterministic.
func TestWriteBundleConcurrentWritersOnlyOneSucceeds(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "bundle.tar")

	const writers = 16
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("payload-from-writer-%d", i))
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = WriteBundle(path, payloads[i])
		}(i)
	}
	close(start)
	wg.Wait()

	successCount := 0
	winner := -1
	for i, err := range results {
		switch {
		case err == nil:
			successCount++
			winner = i
		case !errors.Is(err, ErrDestinationInvalid):
			t.Fatalf("writer %d: expected success or ErrDestinationInvalid, got: %v", i, err)
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly one writer to succeed, got %d", successCount)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(payloads[winner]) {
		t.Fatal("expected the destination to contain exactly the winning writer's payload")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one surviving file after concurrent writers, got %d: %v", len(entries), entries)
	}
}
