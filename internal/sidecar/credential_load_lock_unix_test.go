//go:build darwin || linux

package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func holdFlock(t *testing.T, path string, how int) func() {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(fd, how|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return func() { _ = syscall.Close(fd) }
}

func lockTestPath(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp("/var/tmp", "acr-credential-lifecycle-load-test-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	return file.Name()
}

func TestSharedFlockWaitsForAnExclusiveHolderThenSucceeds(t *testing.T) {
	path := lockTestPath(t)
	release := holdFlock(t, path, syscall.LOCK_EX)
	go func() {
		time.Sleep(300 * time.Millisecond)
		release()
	}()

	closeLock, err := acquireCredentialLifecycleSharedLockAt(path, 5*time.Second)
	if err != nil {
		t.Fatalf("shared acquire = %v, want success after the holder released", err)
	}
	_ = closeLock()
}

func TestSharedFlockTimesOutBeyondTheBound(t *testing.T) {
	path := lockTestPath(t)
	release := holdFlock(t, path, syscall.LOCK_EX)
	defer release()

	_, err := acquireCredentialLifecycleSharedLockAt(path, 100*time.Millisecond)
	if !errors.Is(err, ErrCredentialLifecycleWaitTimeout) {
		t.Fatalf("shared acquire = %v, want the wait timeout", err)
	}
}

func TestSharedFlocksCoexistAndRefuseTheExclusiveTryLock(t *testing.T) {
	path := lockTestPath(t)
	first, err := acquireCredentialLifecycleSharedLockAt(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := acquireCredentialLifecycleSharedLockAt(path, time.Second)
	if err != nil {
		t.Fatalf("second shared acquire = %v, want coexistence", err)
	}
	defer second()

	_, err = acquireCredentialLifecycleLockAt(path)
	if !errors.Is(err, ErrCredentialLifecycleBusy) {
		t.Fatalf("exclusive acquire = %v, want busy while readers hold the lock", err)
	}
}

func TestSharedFlockRejectsUnsafeLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if _, err := acquireCredentialLifecycleSharedLockAt(path, 10*time.Millisecond); !errors.Is(err, errCredentialLifecycleLockUnsafe) {
		t.Fatalf("shared acquire under an unsafe parent = %v, want unsafe", err)
	}
}
