package falkorgraph

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

type waitRecorder struct {
	mu       sync.Mutex
	outcomes []string
	waits    []time.Duration
}

func (r *waitRecorder) record(_ context.Context, outcome string, wait time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
	r.waits = append(r.waits, wait)
}

func (r *waitRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.outcomes)
}

func newWaitRecordedAdapter(t *testing.T, c *gatedBootstrapConn) (*Adapter, *waitRecorder) {
	t.Helper()
	adapter := newGatedAdapter(t, c)
	recorder := &waitRecorder{}
	adapter.config.Telemetry = SlogTelemetry{BootstrapWait: recorder.record}
	return adapter, recorder
}

func assertWaits(t *testing.T, recorder *waitRecorder, want ...string) {
	t.Helper()
	if got := recorder.snapshot(); !slices.Equal(got, want) {
		t.Fatalf("bootstrap wait outcomes = %v, want %v", got, want)
	}
}

// Two writers of one key: the bootstrapper counts nothing, the waiter counts
// once as bootstrapped.
func TestBootstrapWaitCountsOnlyTheWaiter(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter, recorder := newWaitRecordedAdapter(t, c)
	key := writeKeyFor(t, adapter, "org-a")

	c.gate(key)
	first := applyAsync(adapter, "org-a")
	awaitEntered(t, c, key)
	second := applyAsync(adapter, "org-a")
	awaitGateContended(t, adapter, key, 1)
	assertWaits(t, recorder)

	c.open(key)
	awaitApplied(t, first, "the bootstrapper")
	awaitApplied(t, second, "the waiter")
	assertWaits(t, recorder, BootstrapWaitBootstrapped)
	if recorder.waits[0] < 0 {
		t.Fatalf("wait = %s, want >= 0", recorder.waits[0])
	}
}

func TestBootstrapWaitMovesNothingWithoutAWaiter(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter, recorder := newWaitRecordedAdapter(t, c)
	awaitApplied(t, applyAsync(adapter, "org-a"), "a lone writer")
	awaitApplied(t, applyAsync(adapter, "org-a"), "a writer of an already bootstrapped key")
	awaitApplied(t, applyAsync(adapter, "org-b"), "a writer of another key")
	assertWaits(t, recorder)
}

// A writer of another key never waits behind a stuck bootstrap.
func TestBootstrapWaitIgnoresAStuckBootstrapOfAnotherKey(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter, recorder := newWaitRecordedAdapter(t, c)
	keyA := writeKeyFor(t, adapter, "org-a")

	c.gate(keyA)
	stuck := applyAsync(adapter, "org-a")
	awaitEntered(t, c, keyA)
	awaitApplied(t, applyAsync(adapter, "org-b"), "org-b while org-a is stuck")
	assertWaits(t, recorder)

	c.open(keyA)
	awaitApplied(t, stuck, "org-a")
	assertWaits(t, recorder)
}

// The holder fails: the waiter runs the bootstrap (took_over); a newcomer that
// waits behind that second bootstrap ends bootstrapped.
func TestBootstrapWaitNamesTheTakeOver(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter, recorder := newWaitRecordedAdapter(t, c)
	key := writeKeyFor(t, adapter, "org-a")

	c.gate(key)
	c.failNextOf(key, errBootstrapRefused)
	first := applyAsync(adapter, "org-a")
	awaitEntered(t, c, key)
	second := applyAsync(adapter, "org-a")
	awaitGateContended(t, adapter, key, 1)

	c.handOff(key)
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("the writer whose CREATE INDEX failed must return an error")
		}
	case <-time.After(stallBound):
		t.Fatal("the writer whose CREATE INDEX failed did not return")
	}
	awaitEntered(t, c, key)
	newcomer := applyAsync(adapter, "org-a")
	awaitGateContended(t, adapter, key, 2)

	c.open(key)
	awaitApplied(t, second, "the waiter that takes over")
	awaitApplied(t, newcomer, "the newcomer")
	assertWaits(t, recorder, BootstrapWaitTookOver, BootstrapWaitBootstrapped)
}

func bootstrapGateContended(a *Adapter, key string) int {
	a.bootstrapMu.RLock()
	defer a.bootstrapMu.RUnlock()
	if gate := a.bootstrapGates[key]; gate != nil {
		return gate.contended
	}
	return 0
}

// awaitGateContended waits until n callers found key's gate held, which is the
// point from which a waiter is certain to be counted whatever the scheduler
// does next.
func awaitGateContended(t *testing.T, a *Adapter, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(stallBound)
	for bootstrapGateContended(a, key) < n {
		if time.Now().After(deadline) {
			t.Fatalf("contended callers of %q = %d after %s, want %d", key, bootstrapGateContended(a, key), stallBound, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A caller registered on the gate that has not locked it yet (descheduled
// between registering and locking) must not turn the caller that locks first
// and runs the bootstrap into a counted waiter.
func TestBootstrapWaitIgnoresACallerRegisteredButNotYetLocking(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter, recorder := newWaitRecordedAdapter(t, c)
	key := writeKeyFor(t, adapter, "org-a")

	late := adapter.acquireBootstrapGate(key)
	if err := adapter.ensureOrgGraph(context.Background(), key); err != nil {
		t.Fatalf("ensureOrgGraph: %v", err)
	}
	adapter.releaseBootstrapGate(key, late)

	assertWaits(t, recorder)
	if !bootstrapCached(adapter, key) {
		t.Fatal("the key must read as bootstrapped")
	}
	assertNoBootstrapGates(t, adapter)
}
