//go:build darwin || linux

package sidecar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var errCredentialLifecycleLockUnsafe = errors.New("acr: credential lifecycle lock is unsafe")

func acquireCredentialLifecycleLock() (func() error, error) {
	return acquireCredentialLifecycleLockAt(credentialLifecycleLockPath())
}

func credentialLifecycleLockPath() string {
	return filepath.Join("/var/tmp", fmt.Sprintf("acr-credential-lifecycle-%d.lock", os.Geteuid()))
}

func acquireCredentialLifecycleLockAt(path string) (func() error, error) {
	if err := validateCredentialLifecycleLockParent(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return acquireCredentialLifecycleLockFile(path)
}

func acquireCredentialLifecycleSharedLock(wait time.Duration) (func() error, error) {
	return acquireCredentialLifecycleSharedLockAt(credentialLifecycleLockPath(), wait)
}

func acquireCredentialLifecycleSharedLockAt(path string, wait time.Duration) (func() error, error) {
	if err := validateCredentialLifecycleLockParent(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return acquireCredentialLifecycleLockFileMode(path, syscall.LOCK_SH, wait)
}

func acquireCredentialLifecycleLockFile(path string) (func() error, error) {
	return acquireCredentialLifecycleLockFileMode(path, syscall.LOCK_EX, 0)
}

// acquireCredentialLifecycleLockFileMode takes the flock in how mode without
// blocking, retrying until wait has passed. An exclusive caller passes a zero
// wait and gets ErrCredentialLifecycleBusy at once; a shared caller that is
// still refused after wait gets ErrCredentialLifecycleWaitTimeout.
func acquireCredentialLifecycleLockFileMode(path string, how int, wait time.Duration) (func() error, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) {
			return nil, fmt.Errorf("%w: %v", errCredentialLifecycleLockUnsafe, err)
		}
		return nil, fmt.Errorf("acquire credential lifecycle lock: %w", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || !credentialLifecycleLockFileMetadataIsSafe(stat) {
		_ = syscall.Close(fd)
		return nil, errCredentialLifecycleLockUnsafe
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(fd, how|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = syscall.Close(fd)
			return nil, fmt.Errorf("acquire credential lifecycle lock: %w", err)
		}
		if wait == 0 {
			_ = syscall.Close(fd)
			return nil, ErrCredentialLifecycleBusy
		}
		if !time.Now().Before(deadline) {
			_ = syscall.Close(fd)
			return nil, ErrCredentialLifecycleWaitTimeout
		}
		time.Sleep(credentialLifecycleSharedPoll)
	}
	return func() error {
		unlockErr := syscall.Flock(fd, syscall.LOCK_UN)
		closeErr := syscall.Close(fd)
		if unlockErr != nil {
			return unlockErr
		}
		return closeErr
	}, nil
}

func credentialLifecycleLockFileMetadataIsSafe(stat syscall.Stat_t) bool {
	return stat.Mode&syscall.S_IFMT == syscall.S_IFREG &&
		stat.Uid == uint32(os.Geteuid()) &&
		stat.Mode&0o077 == 0 &&
		stat.Nlink == 1
}

func validateCredentialLifecycleLockParent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect lock parent: %v", errCredentialLifecycleLockUnsafe, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errCredentialLifecycleLockUnsafe
	}
	return validateCredentialLifecycleLockParentMetadata(info.Mode(), stat.Uid)
}

func validateCredentialLifecycleLockParentMetadata(mode os.FileMode, owner uint32) error {
	if mode&os.ModeDir == 0 || mode&os.ModeSymlink != 0 || owner != 0 || mode&os.ModeSticky == 0 {
		return errCredentialLifecycleLockUnsafe
	}
	return nil
}
