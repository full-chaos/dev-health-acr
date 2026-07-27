//go:build !darwin && !linux && acr_compiled_lifecycle_lock_fixture

package sidecar

func init() {
	credentialLifecycleLockAcquire = acquireCredentialLifecycleLock
}
