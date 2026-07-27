package sidecar

import (
	"errors"
	"sync"
)

var errCredentialLifecycleBusy = errors.New("acr: credential lifecycle is already active")

var ErrCredentialLifecycleSessionInvalid = errors.New("acr: credential lifecycle session is invalid")

var ErrCredentialLifecycleSessionClosed = errors.New("acr: credential lifecycle session is closed")

var credentialLifecycleGate sync.Mutex

var credentialLifecycleLockAcquire = acquireCredentialLifecycleLock

type CredentialLifecycleSession struct {
	state *credentialLifecycleSessionState
}

type credentialLifecycleSessionState struct {
	mu           sync.Mutex
	ready        *sync.Cond
	close        func() error
	closeErr     error
	active       bool
	closing      bool
	inFlight     int
	closed       chan struct{}
	closeStarted chan struct{}
}

func BeginCredentialLifecycleSession() (*CredentialLifecycleSession, error) {
	if !credentialLifecycleGate.TryLock() {
		return nil, errCredentialLifecycleBusy
	}
	close, err := credentialLifecycleLockAcquire()
	if err != nil {
		credentialLifecycleGate.Unlock()
		return nil, err
	}
	state := &credentialLifecycleSessionState{active: true, closed: make(chan struct{}), closeStarted: make(chan struct{})}
	state.ready = sync.NewCond(&state.mu)
	state.close = func() error {
		defer credentialLifecycleGate.Unlock()
		return close()
	}
	return &CredentialLifecycleSession{state: state}, nil
}

func (s *CredentialLifecycleSession) Close() error {
	if s == nil || s.state == nil {
		return ErrCredentialLifecycleSessionInvalid
	}
	state := s.state
	state.mu.Lock()
	if state.closing {
		closed := state.closed
		state.mu.Unlock()
		<-closed
		state.mu.Lock()
		err := state.closeErr
		state.mu.Unlock()
		return err
	}
	state.closing = true
	state.active = false
	close(state.closeStarted)
	for state.inFlight != 0 {
		state.ready.Wait()
	}
	release := state.close
	state.mu.Unlock()

	err := release()

	state.mu.Lock()
	state.closeErr = err
	close(state.closed)
	state.mu.Unlock()
	return err
}

func (s *CredentialLifecycleSession) beginOperation() (func(), error) {
	if s == nil || s.state == nil {
		return nil, ErrCredentialLifecycleSessionInvalid
	}
	state := s.state
	state.mu.Lock()
	if !state.active {
		state.mu.Unlock()
		return nil, ErrCredentialLifecycleSessionClosed
	}
	state.inFlight++
	state.mu.Unlock()
	return func() {
		state.mu.Lock()
		state.inFlight--
		if state.inFlight == 0 {
			state.ready.Broadcast()
		}
		state.mu.Unlock()
	}, nil
}
