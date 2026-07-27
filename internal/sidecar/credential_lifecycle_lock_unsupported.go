//go:build !darwin && !linux

package sidecar

func acquireCredentialLifecycleLock() (func() error, error) {
	return func() error { return nil }, nil
}
