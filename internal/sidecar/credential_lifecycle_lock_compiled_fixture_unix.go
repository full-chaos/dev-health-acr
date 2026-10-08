//go:build (darwin || linux) && acr_compiled_lifecycle_lock_fixture

package sidecar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func init() {
	credentialLifecycleLockAcquire = acquireCompiledLifecycleLock
	credentialLifecycleSharedLockAcquire = func(time.Duration) (func() error, error) {
		return func() error { return nil }, nil
	}
}

func acquireCompiledLifecycleLock() (func() error, error) {
	path := filepath.Join("/var/tmp", fmt.Sprintf("acr-credential-lifecycle-fixture-%d-%d.lock", os.Geteuid(), os.Getpid()))
	closeLock, err := acquireCredentialLifecycleLockAt(path)
	if err != nil {
		return nil, err
	}
	return func() error {
		return errors.Join(closeLock(), os.Remove(path))
	}, nil
}
