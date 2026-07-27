package sidecar

import (
	"errors"
	"testing"
)

var ErrCredentialLifecycleLockTestSeamUnavailable = errors.New("acr: isolated credential lifecycle locks are available only under go test")

func InstallIsolatedCredentialLifecycleLockForTesting() (func(), error) {
	if !testing.Testing() {
		return nil, ErrCredentialLifecycleLockTestSeamUnavailable
	}
	return installIsolatedCredentialLifecycleLockForTesting()
}
