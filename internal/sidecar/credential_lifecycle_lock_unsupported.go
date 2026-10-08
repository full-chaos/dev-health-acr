//go:build !darwin && !linux

package sidecar

import "time"

func acquireCredentialLifecycleLock() (func() error, error) {
	return func() error { return nil }, nil
}

func acquireCredentialLifecycleSharedLock(time.Duration) (func() error, error) {
	return func() error { return nil }, nil
}
