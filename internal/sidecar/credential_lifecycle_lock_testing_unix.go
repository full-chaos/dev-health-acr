//go:build darwin || linux

package sidecar

import (
	"errors"
	"fmt"
	"os"
	"time"
)

func installIsolatedCredentialLifecycleLockForTesting() (func(), error) {
	file, err := os.CreateTemp("/var/tmp", fmt.Sprintf("acr-credential-lifecycle-test-%d-*", os.Geteuid()))
	if err != nil {
		return nil, fmt.Errorf("create isolated credential lifecycle lock: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close isolated credential lifecycle lock: %w", err)
	}
	original := credentialLifecycleLockAcquire
	originalShared := credentialLifecycleSharedLockAcquire
	credentialLifecycleSharedLockAcquire = func(wait time.Duration) (func() error, error) {
		return acquireCredentialLifecycleSharedLockAt(path, wait)
	}
	credentialLifecycleLockAcquire = func() (func() error, error) {
		closeLock, err := acquireCredentialLifecycleLockAt(path)
		if err != nil {
			return nil, err
		}
		return func() error {
			return errors.Join(closeLock(), os.Remove(path))
		}, nil
	}
	return func() {
		credentialLifecycleLockAcquire = original
		credentialLifecycleSharedLockAcquire = originalShared
		_ = os.Remove(path)
	}, nil
}
