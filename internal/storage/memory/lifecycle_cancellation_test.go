package memory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stagedCancellationContext struct {
	context.Context
	firstCheck chan struct{}
	canceled   atomic.Bool
	once       sync.Once
}

type countedCancellationContext struct {
	context.Context
	threshold int32
	reached   chan struct{}
	checks    atomic.Int32
	canceled  atomic.Bool
}

func (c *countedCancellationContext) Err() error {
	if c.checks.Add(1) == c.threshold {
		close(c.reached)
	}
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *stagedCancellationContext) Err() error {
	c.once.Do(func() { close(c.firstCheck) })
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func TestCredentialStore_rechecksCancellationAfterLifecycleLock(t *testing.T) {
	// Given
	audit := NewAuditStore()
	backend, store, err := newCredentialStore(audit, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &stagedCancellationContext{Context: context.Background(), firstCheck: make(chan struct{})}
	backend.mu.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := store.CreateCredential(ctx, validCredentialCreateInput("blocked"))
		result <- err
	}()
	<-ctx.firstCheck
	ctx.canceled.Store(true)
	backend.mu.Unlock()

	// When
	err = <-result

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateCredential() error = %v, want context canceled", err)
	}
	credentials, listErr := store.List(context.Background(), "org_1")
	if listErr != nil || len(credentials) != 0 || len(audit.Events()) != 0 {
		t.Fatalf("canceled create changed state: credentials=%#v audits=%#v error=%v", credentials, audit.Events(), listErr)
	}
}

func TestCredentialStore_rechecksCancellationAfterAuditLock(t *testing.T) {
	// Given
	audit := NewAuditStore()
	store := mustCredentialStore(t, audit)
	ctx := &countedCancellationContext{Context: context.Background(), threshold: 4, reached: make(chan struct{})}
	audit.mu.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := store.CreateCredential(ctx, validCredentialCreateInput("audit-blocked"))
		result <- err
	}()
	<-ctx.reached
	ctx.canceled.Store(true)
	audit.mu.Unlock()

	// When
	err := <-result

	// Then
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateCredential() error = %v, want context canceled", err)
	}
	credentials, listErr := store.List(context.Background(), "org_1")
	if listErr != nil || len(credentials) != 0 || len(audit.Events()) != 0 {
		t.Fatalf("canceled create changed state: credentials=%#v audits=%#v error=%v", credentials, audit.Events(), listErr)
	}
}
