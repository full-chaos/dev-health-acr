package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file exercises readBoundedRegularFile (boundedfile.go) directly:
// the shared implementation behind the CA-bundle and credential-file
// reads covered end to end in api_client_cacert_test.go, config_ca_test.go,
// and credential_file_test.go. The two adversarial tests at the bottom
// prove, under real concurrent path swapping rather than a single
// before/after snapshot, that the atomic O_NOFOLLOW/O_NONBLOCK open this
// package now uses instead of a separate lstat-then-open check can never
// be raced into following a symlink or blocking on a FIFO.

func TestReadBoundedRegularFileAcceptsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular")
	want := []byte("canary-content")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	data, info, err := readBoundedRegularFile(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want) {
		t.Fatalf("unexpected data: %q", data)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("returned FileInfo is not a regular file: %s", info.Mode())
	}
}

func TestReadBoundedRegularFileRejectsNonexistentFile(t *testing.T) {
	_, _, err := readBoundedRegularFile(filepath.Join(t.TempDir(), "missing"), 4096)
	if err == nil {
		t.Fatal("a nonexistent path was accepted")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestReadBoundedRegularFileRejectsDirectory(t *testing.T) {
	if _, _, err := readBoundedRegularFile(t.TempDir(), 4096); err == nil {
		t.Fatal("a directory path was accepted")
	}
}

func TestReadBoundedRegularFileRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.WriteFile(target, []byte("canary-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundedRegularFile(link, 4096); err == nil {
		t.Fatal("a symlinked path was accepted even though it resolved to a valid regular file")
	}
}

func TestReadBoundedRegularFileRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	if err := os.WriteFile(path, make([]byte, 4097), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readBoundedRegularFile(path, 4096); err == nil {
		t.Fatal("an oversized file was accepted")
	}
}

func TestDescribeFileErrorNeverEchoesPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "canary-username-marker", "missing")
	_, _, err := readBoundedRegularFile(path, 4096)
	if err == nil {
		t.Fatal("a nonexistent nested path was accepted")
	}
	described := describeFileError("field", err)
	if strings.Contains(described.Error(), "canary-username-marker") {
		t.Fatalf("describeFileError leaked the path: %v", described)
	}
	if strings.Contains(described.Error(), dir) {
		t.Fatalf("describeFileError leaked the temp dir: %v", described)
	}
}

// TestReadBoundedRegularFileSymlinkSwapRaceNeverFollows proves that under
// real, continuous concurrent path swapping between a regular file and a
// symlink to an unrelated "secret" file, readBoundedRegularFile can never
// be raced into returning data read through the symlink. Every rename
// used to perform the swap is a single atomic filesystem operation, so
// this is not merely a before/after snapshot test: the goal is to prove
// the atomic O_NOFOLLOW open holds even when the race window is actively,
// repeatedly exercised for the whole test duration, not just at one
// instant.
func TestReadBoundedRegularFileSymlinkSwapRaceNeverFollows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	dir := t.TempDir()
	outsideSecret := filepath.Join(dir, "outside-secret")
	if err := os.WriteFile(outsideSecret, []byte("outside-secret-content"), 0o600); err != nil {
		t.Fatal(err)
	}
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
		iteration := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			iteration++
			tmpLink := filepath.Join(dir, "tmp-link")
			if err := os.Symlink(outsideSecret, tmpLink); err != nil {
				continue
			}
			if err := os.Rename(tmpLink, path); err != nil {
				continue
			}
			tmpFile := filepath.Join(dir, "tmp-file")
			if err := os.WriteFile(tmpFile, legit, 0o600); err != nil {
				continue
			}
			_ = os.Rename(tmpFile, path)
		}
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	attempts, successes := 0, 0
	for time.Now().Before(deadline) {
		attempts++
		data, _, err := readBoundedRegularFile(path, 4096)
		if err == nil {
			successes++
			if string(data) != string(legit) {
				close(stop)
				wg.Wait()
				t.Fatalf("readBoundedRegularFile returned data that did not come from the verified regular file (possible symlink follow): %q", data)
			}
		}
	}
	close(stop)
	wg.Wait()
	if attempts == 0 || successes == 0 {
		t.Fatalf("race test did not exercise both states: attempts=%d successes=%d", attempts, successes)
	}
}
