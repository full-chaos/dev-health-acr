//go:build darwin || linux

package sidecar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func acquireCredentialLifecycleLock() (func() error, error) {
	path := filepath.Join("/var/tmp", fmt.Sprintf("acr-credential-lifecycle-%d.lock", os.Geteuid()))
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire credential lifecycle lock: %w", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 || stat.Nlink != 1 {
		_ = syscall.Close(fd)
		return nil, errors.New("acr: credential lifecycle lock is unsafe")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = syscall.Close(fd)
		return nil, errCredentialLifecycleBusy
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
