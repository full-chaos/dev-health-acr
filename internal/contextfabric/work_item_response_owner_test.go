package contextfabric

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newResponseOwnerTestGate(t *testing.T) *WorkItemMembershipGate {
	t.Helper()
	gate, err := NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	return gate
}

func acquireResponseOwnerTestLease(t *testing.T, gate *WorkItemMembershipGate) *WorkItemMembershipLease {
	t.Helper()
	lease, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("gate.Acquire: %v", err)
	}
	if lease == nil {
		t.Fatal("gate.Acquire returned a nil lease")
	}
	return lease
}

func assertResponseOwnerTestGateOccupied(t *testing.T, gate *WorkItemMembershipGate) {
	t.Helper()
	if got := gate.Stats().InFlight; got != 1 {
		t.Fatalf("gate in-flight occupancy = %d, want 1", got)
	}
	if _, err := gate.Acquire(context.Background()); !errors.Is(err, ErrWorkItemMembershipQueueFull) {
		t.Fatalf("second gate.Acquire error = %v, want ErrWorkItemMembershipQueueFull", err)
	}
}

func assertResponseOwnerTestGateAvailable(t *testing.T, gate *WorkItemMembershipGate) {
	t.Helper()
	if got := gate.Stats().InFlight; got != 0 {
		t.Fatalf("gate in-flight occupancy = %d, want 0", got)
	}
	lease := acquireResponseOwnerTestLease(t, gate)
	lease.Release()
	if got := gate.Stats().InFlight; got != 0 {
		t.Fatalf("gate in-flight occupancy after probe release = %d, want 0", got)
	}
}

func TestWorkItemResponseOwnerContextAndNilSafety(t *testing.T) {
	if owner, ok := WorkItemResponseOwnerFromContext(nil); ok || owner != nil {
		t.Fatalf("WorkItemResponseOwnerFromContext(nil) = (%v, %t), want (nil, false)", owner, ok)
	}

	ctx, owner := NewWorkItemResponseOwnerContext(nil)
	if ctx == nil || owner == nil {
		t.Fatalf("NewWorkItemResponseOwnerContext returned ctx=%v owner=%v", ctx, owner)
	}
	if got, ok := WorkItemResponseOwnerFromContext(ctx); !ok || got != owner {
		t.Fatalf("owner from context = (%p, %t), want (%p, true)", got, ok, owner)
	}
	if got := WithWorkItemResponseOwner(ctx, nil); got != ctx {
		t.Fatalf("WithWorkItemResponseOwner(ctx, nil) returned a different context")
	}
	if got, ok := WorkItemResponseOwnerFromContext(WithWorkItemResponseOwner(nil, owner)); !ok || got != owner {
		t.Fatalf("owner from nil-parent context = (%p, %t), want (%p, true)", got, ok, owner)
	}

	var nilOwner *WorkItemResponseOwner
	nilOwner.Release(nil)
	nilOwner.Complete()
	if err := nilOwner.Retain(nil); err != nil {
		t.Fatalf("nil owner Retain(nil) = %v, want nil", err)
	}

	gate := newResponseOwnerTestGate(t)
	lease := acquireResponseOwnerTestLease(t, gate)
	if err := nilOwner.Retain(lease); !errors.Is(err, ErrWorkItemResponseOwnerUnavailable) {
		t.Fatalf("nil owner Retain(lease) = %v, want ErrWorkItemResponseOwnerUnavailable", err)
	}
	assertResponseOwnerTestGateAvailable(t, gate)
}

func TestWorkItemResponseOwnerHoldsPermitUntilRelease(t *testing.T) {
	gate := newResponseOwnerTestGate(t)
	lease := acquireResponseOwnerTestLease(t, gate)
	owner := NewWorkItemResponseOwner()

	if err := owner.Retain(lease); err != nil {
		t.Fatalf("owner.Retain: %v", err)
	}
	if err := owner.Retain(lease); err != nil {
		t.Fatalf("idempotent owner.Retain: %v", err)
	}
	assertResponseOwnerTestGateOccupied(t, gate)

	owner.Release(lease)
	assertResponseOwnerTestGateAvailable(t, gate)
	owner.Release(lease)
	owner.Complete()
}

func TestWorkItemResponseOwnerCompleteClosesAndReleasesOnce(t *testing.T) {
	gate := newResponseOwnerTestGate(t)
	lease := acquireResponseOwnerTestLease(t, gate)
	owner := NewWorkItemResponseOwner()

	if err := owner.Retain(lease); err != nil {
		t.Fatalf("owner.Retain: %v", err)
	}
	assertResponseOwnerTestGateOccupied(t, gate)

	owner.Complete()
	assertResponseOwnerTestGateAvailable(t, gate)
	owner.Complete()
	owner.Release(lease)
	assertResponseOwnerTestGateAvailable(t, gate)
}

func TestWorkItemResponseOwnerRejectsLateRetainAndReleasesIncoming(t *testing.T) {
	gate := newResponseOwnerTestGate(t)
	owner := NewWorkItemResponseOwner()
	owner.Complete()

	incoming := acquireResponseOwnerTestLease(t, gate)
	if err := owner.Retain(incoming); !errors.Is(err, ErrWorkItemResponseOwnerClosed) {
		t.Fatalf("late owner.Retain = %v, want ErrWorkItemResponseOwnerClosed", err)
	}
	assertResponseOwnerTestGateAvailable(t, gate)

	// A closed owner remains closed. Prove the incoming permit was returned by
	// acquiring the same real one-permit gate again.
	second := acquireResponseOwnerTestLease(t, gate)
	second.Release()
	owner.Complete()
}

func TestWorkItemResponseOwnerRejectsSecondLeaseWithoutDiscardingFirst(t *testing.T) {
	firstGate := newResponseOwnerTestGate(t)
	secondGate := newResponseOwnerTestGate(t)
	first := acquireResponseOwnerTestLease(t, firstGate)
	second := acquireResponseOwnerTestLease(t, secondGate)
	owner := NewWorkItemResponseOwner()

	if err := owner.Retain(first); err != nil {
		t.Fatalf("owner.Retain(first): %v", err)
	}
	if err := owner.Retain(second); !errors.Is(err, ErrWorkItemResponseOwnerLeaseConflict) {
		t.Fatalf("owner.Retain(second) = %v, want ErrWorkItemResponseOwnerLeaseConflict", err)
	}
	assertResponseOwnerTestGateOccupied(t, firstGate)
	assertResponseOwnerTestGateAvailable(t, secondGate)

	owner.Release(first)
	assertResponseOwnerTestGateAvailable(t, firstGate)
	owner.Complete()
}

func TestWorkItemResponseOwnerIgnoresUnregisteredLease(t *testing.T) {
	firstGate := newResponseOwnerTestGate(t)
	secondGate := newResponseOwnerTestGate(t)
	first := acquireResponseOwnerTestLease(t, firstGate)
	foreign := acquireResponseOwnerTestLease(t, secondGate)
	owner := NewWorkItemResponseOwner()

	if err := owner.Retain(first); err != nil {
		t.Fatalf("owner.Retain(first): %v", err)
	}
	owner.Release(foreign)
	assertResponseOwnerTestGateOccupied(t, firstGate)
	assertResponseOwnerTestGateOccupied(t, secondGate)

	owner.Complete()
	assertResponseOwnerTestGateAvailable(t, firstGate)
	assertResponseOwnerTestGateOccupied(t, secondGate)
	foreign.Release()
	assertResponseOwnerTestGateAvailable(t, secondGate)
}

func TestWorkItemResponseOwnerCompleteReleasesOutsideOwnerLock(t *testing.T) {
	gate := newResponseOwnerTestGate(t)
	underlying := acquireResponseOwnerTestLease(t, gate)
	owner := NewWorkItemResponseOwner()
	// The wrapper makes release re-enter the owner. This would deadlock if
	// Complete held the owner lock while invoking the lease callback.
	wrapped := &WorkItemMembershipLease{release: func() {
		underlying.Release()
		owner.Complete()
	}}
	if err := owner.Retain(wrapped); err != nil {
		t.Fatalf("owner.Retain(wrapped): %v", err)
	}

	done := make(chan struct{})
	go func() {
		owner.Complete()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owner.Complete deadlocked while releasing a re-entrant lease")
	}
	assertResponseOwnerTestGateAvailable(t, gate)
	owner.Complete()
}

func TestWorkItemResponseOwnerRegistrationAndCompletionRace(t *testing.T) {
	const iterations = 200
	for iteration := 0; iteration < iterations; iteration++ {
		firstGate := newResponseOwnerTestGate(t)
		secondGate := newResponseOwnerTestGate(t)
		first := acquireResponseOwnerTestLease(t, firstGate)
		second := acquireResponseOwnerTestLease(t, secondGate)
		owner := NewWorkItemResponseOwner()

		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan error, 2)
		wg.Add(4)
		go func() {
			defer wg.Done()
			<-start
			results <- owner.Retain(first)
		}()
		go func() {
			defer wg.Done()
			<-start
			results <- owner.Retain(second)
		}()
		go func() {
			defer wg.Done()
			<-start
			owner.Complete()
		}()
		go func() {
			defer wg.Done()
			<-start
			owner.Release(first)
		}()
		close(start)
		wg.Wait()
		close(results)

		for err := range results {
			if err != nil && !errors.Is(err, ErrWorkItemResponseOwnerClosed) && !errors.Is(err, ErrWorkItemResponseOwnerLeaseConflict) {
				t.Fatalf("iteration %d Retain error = %v, want nil, closed, or conflict", iteration, err)
			}
		}
		assertResponseOwnerTestGateAvailable(t, firstGate)
		assertResponseOwnerTestGateAvailable(t, secondGate)
		owner.Complete()
	}
}
