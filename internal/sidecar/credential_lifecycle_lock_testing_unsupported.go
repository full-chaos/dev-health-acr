//go:build !darwin && !linux

package sidecar

func installIsolatedCredentialLifecycleLockForTesting() (func(), error) {
	original := credentialLifecycleLockAcquire
	originalShared := credentialLifecycleSharedLockAcquire
	credentialLifecycleLockAcquire = acquireCredentialLifecycleLock
	credentialLifecycleSharedLockAcquire = acquireCredentialLifecycleSharedLock
	return func() {
		credentialLifecycleLockAcquire = original
		credentialLifecycleSharedLockAcquire = originalShared
	}, nil
}
