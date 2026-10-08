package sidecar

import (
	"errors"
	"sync"
	"time"
)

// ErrCredentialLifecycleBusy means another local credential lifecycle session
// still owns the process-wide or cross-process lock. Callers may render
// actionable recovery guidance for this sentinel, while treating every other
// acquisition failure as an unsafe or unavailable lock boundary.
var ErrCredentialLifecycleBusy = errors.New("acr: credential lifecycle is already active")

var ErrCredentialLifecycleSessionInvalid = errors.New("acr: credential lifecycle session is invalid")

var ErrCredentialLifecycleSessionClosed = errors.New("acr: credential lifecycle session is closed")

// ErrCredentialLifecycleWaitTimeout means a read-only credential load waited
// its full bound for a concurrent credential mutation (login, refresh, logout)
// to finish and the mutation was still running.
var ErrCredentialLifecycleWaitTimeout = errors.New("acr: credential lifecycle did not become available in time")

// credentialLifecycleSharedWait bounds how long a read-only credential load
// waits for a concurrent mutation. Mutations keep the exclusive try-lock.
const credentialLifecycleSharedWaitDefault = 2 * time.Second

var credentialLifecycleSharedWait = credentialLifecycleSharedWaitDefault

// credentialLifecycleGate lets any number of read-only loads share the process
// while a mutation session holds it exclusively.
var credentialLifecycleGate sync.RWMutex

var credentialLifecycleLockAcquire = acquireCredentialLifecycleLock

var credentialLifecycleSharedLockAcquire = acquireCredentialLifecycleSharedLock

const credentialLifecycleSharedPoll = 10 * time.Millisecond

// acquireSharedCredentialLifecycle takes the shared side of the lifecycle
// boundary for a read-only load, waiting at most wait for a mutation to end.
func acquireSharedCredentialLifecycle(wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for !credentialLifecycleGate.TryRLock() {
		if !time.Now().Before(deadline) {
			return nil, ErrCredentialLifecycleWaitTimeout
		}
		time.Sleep(credentialLifecycleSharedPoll)
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	release, err := credentialLifecycleSharedLockAcquire(remaining)
	if err != nil {
		credentialLifecycleGate.RUnlock()
		return nil, err
	}
	return func() {
		_ = release()
		credentialLifecycleGate.RUnlock()
	}, nil
}

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
		return nil, ErrCredentialLifecycleBusy
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
