//go:build !darwin && !linux

package sidecar

func installIsolatedCredentialLifecycleLockForTesting() (func(), error) {
	original := credentialLifecycleLockAcquire
	credentialLifecycleLockAcquire = acquireCredentialLifecycleLock
	return func() {
		credentialLifecycleLockAcquire = original
	}, nil
}
