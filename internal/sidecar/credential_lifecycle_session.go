package sidecar

import (
	"errors"
	"sync"
)

var errCredentialLifecycleBusy = errors.New("acr: credential lifecycle is already active")

var credentialLifecycleGate sync.Mutex

type CredentialLifecycleSession struct {
	close     func() error
	closeOnce sync.Once
	closeErr  error
}

func BeginCredentialLifecycleSession() (*CredentialLifecycleSession, error) {
	if !credentialLifecycleGate.TryLock() {
		return nil, errCredentialLifecycleBusy
	}
	close, err := acquireCredentialLifecycleLock()
	if err != nil {
		credentialLifecycleGate.Unlock()
		return nil, err
	}
	return &CredentialLifecycleSession{close: func() error {
		defer credentialLifecycleGate.Unlock()
		return close()
	}}, nil
}

func (s *CredentialLifecycleSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.close != nil {
			s.closeErr = s.close()
		}
	})
	return s.closeErr
}
