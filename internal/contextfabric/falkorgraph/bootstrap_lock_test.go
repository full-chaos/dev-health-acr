package falkorgraph

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stallBound is how long a write of one organization may wait while another
// organization's bootstrap is stuck on the server. A write that waits on the
// stuck bootstrap waits until the test releases it, far past this bound.
const stallBound = 2 * time.Second

// gatedBootstrapConn holds the first schema statement (CREATE INDEX on
// Subject) of a gated key until the test releases it, as a server that stops
// answering does. Like the write client, it ignores the caller's ctx.
type gatedBootstrapConn struct {
	*fakeConn
	entered chan string

	mu          sync.Mutex
	release     map[string]chan struct{}
	failNext    map[string]error
	calls       map[string]int
	inFlight    map[string]int
	maxInFlight map[string]int
}

func newGatedBootstrapConn(t *testing.T) *gatedBootstrapConn {
	t.Helper()
	c := &gatedBootstrapConn{
		fakeConn: &fakeConn{constraintsFunc: func(context.Context, string) ([]constraintStatus, error) {
			return []constraintStatus{{Status: "OPERATIONAL", Label: labelSubject, EntityType: "NODE"}}, nil
		}},
		entered:     make(chan string, 64),
		release:     map[string]chan struct{}{},
		failNext:    map[string]error{},
		calls:       map[string]int{},
		inFlight:    map[string]int{},
		maxInFlight: map[string]int{},
	}
	t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for key, ch := range c.release {
			close(ch)
			delete(c.release, key)
		}
	})
	return c
}

func (c *gatedBootstrapConn) gate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.release[key] = make(chan struct{})
}

// open releases the held statement of key; later statements of key pass at once.
func (c *gatedBootstrapConn) open(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.release[key]; ok {
		close(ch)
		delete(c.release, key)
	}
}

// handOff releases the statement held now and holds the next one of key.
func (c *gatedBootstrapConn) handOff(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.release[key]; ok {
		close(ch)
	}
	c.release[key] = make(chan struct{})
}

func (c *gatedBootstrapConn) failNextOf(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failNext[key] = err
}

func (c *gatedBootstrapConn) stats(key string) (calls, maxInFlight int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key], c.maxInFlight[key]
}

func (c *gatedBootstrapConn) createIndex(_ context.Context, graphKey, label string, _ []string, _ bool) error {
	if label != labelSubject {
		return nil
	}
	c.mu.Lock()
	c.calls[graphKey]++
	c.inFlight[graphKey]++
	if c.inFlight[graphKey] > c.maxInFlight[graphKey] {
		c.maxInFlight[graphKey] = c.inFlight[graphKey]
	}
	held := c.release[graphKey]
	fail := c.failNext[graphKey]
	delete(c.failNext, graphKey)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inFlight[graphKey]--
		c.mu.Unlock()
	}()
	c.entered <- graphKey
	if held != nil {
		<-held
	}
	return fail
}

func newGatedAdapter(t *testing.T, c *gatedBootstrapConn) *Adapter {
	t.Helper()
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-gate", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true,
	}, c)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	return adapter
}

func writeKeyFor(t *testing.T, a *Adapter, orgID string) string {
	t.Helper()
	key, err := a.resolveWriteKey(context.Background(), orgID)
	if err != nil {
		t.Fatalf("resolveWriteKey(%s): %v", orgID, err)
	}
	return key
}

func applyAsync(a *Adapter, orgID string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := a.ApplyProjectionBatch(context.Background(), vectorProbeBatch(orgID))
		done <- err
	}()
	return done
}

func awaitEntered(t *testing.T, c *gatedBootstrapConn, key string) {
	t.Helper()
	select {
	case got := <-c.entered:
		if got != key {
			t.Fatalf("first bootstrap statement entered for key %q, want %q", got, key)
		}
	case <-time.After(stallBound):
		t.Fatalf("the bootstrap of %q never reached CREATE INDEX", key)
	}
}

func awaitApplied(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(stallBound):
		t.Fatalf("%s: still waiting after %s on another key's stuck bootstrap", what, stallBound)
	}
}

// One organization's bootstrap stuck on the server must not hold the writes
// of other organizations: neither an organization already bootstrapped nor
// one that bootstraps its own key now.
func TestAStuckBootstrapDoesNotHoldOtherOrganizationsWrites(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter := newGatedAdapter(t, c)
	keyA, keyB, keyC := writeKeyFor(t, adapter, "org-a"), writeKeyFor(t, adapter, "org-b"), writeKeyFor(t, adapter, "org-c")

	awaitApplied(t, applyAsync(adapter, "org-b"), "premise: org-b bootstraps on a healthy server")
	awaitEntered(t, c, keyB)

	c.gate(keyA)
	stuck := applyAsync(adapter, "org-a")
	awaitEntered(t, c, keyA)

	awaitApplied(t, applyAsync(adapter, "org-b"), "org-b write while org-a's bootstrap is stuck")
	awaitApplied(t, applyAsync(adapter, "org-c"), "org-c bootstrap while org-a's bootstrap is stuck")
	awaitEntered(t, c, keyC)

	if bootstrapCached(adapter, keyA) {
		t.Fatal("org-a must not read as bootstrapped while its bootstrap is stuck")
	}
	c.open(keyA)
	awaitApplied(t, stuck, "org-a after its bootstrap is released")
	if !bootstrapCached(adapter, keyA) || !bootstrapCached(adapter, keyB) || !bootstrapCached(adapter, keyC) {
		t.Fatal("every organization must read as bootstrapped at the end")
	}
	for _, key := range []string{keyA, keyB, keyC} {
		if calls, _ := c.stats(key); calls != 1 {
			t.Fatalf("CREATE INDEX calls for %q = %d, want 1", key, calls)
		}
	}
	assertNoBootstrapGates(t, adapter)
}

// A purge of the graph while its bootstrap is in flight must win: the
// bootstrap that finishes after the purge must not mark the purged graph as
// bootstrapped, so the next write bootstraps it again.
func TestAPurgeDuringABootstrapLeavesTheGraphUnbootstrapped(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter := newGatedAdapter(t, c)
	key := writeKeyFor(t, adapter, "org-a")
	if purgeKey := graphKey(adapter.config.GraphPrefix, "org-a"); purgeKey != key {
		t.Fatalf("premise: purge key %q != write key %q", purgeKey, key)
	}

	c.gate(key)
	stuck := applyAsync(adapter, "org-a")
	awaitEntered(t, c, key)

	purged := make(chan error, 1)
	go func() { purged <- adapter.PurgeOrganization(context.Background(), "org-a") }()
	select {
	case err := <-purged:
		if err != nil {
			t.Fatalf("PurgeOrganization: %v", err)
		}
	case <-time.After(stallBound):
		// The purge waits for the bootstrap only when one lock covers both;
		// release it so the row still checks the end state.
		c.open(key)
		if err := <-purged; err != nil {
			t.Fatalf("PurgeOrganization: %v", err)
		}
	}
	c.open(key)
	awaitApplied(t, stuck, "the write whose bootstrap the purge overtook")
	if bootstrapCached(adapter, key) {
		t.Fatal("a bootstrap that finished after a purge marked the purged graph as bootstrapped")
	}

	awaitApplied(t, applyAsync(adapter, "org-a"), "the next write after the purge")
	if calls, _ := c.stats(key); calls != 2 {
		t.Fatalf("CREATE INDEX calls = %d, want 2 (the next write bootstraps the purged graph again)", calls)
	}
	if !bootstrapCached(adapter, key) {
		t.Fatal("the next write's bootstrap must mark the graph as bootstrapped")
	}
	assertNoBootstrapGates(t, adapter)
}

var errBootstrapRefused = errors.New("bootstrap refused")

// bootstrapGateUsers reports -1 while bootstrapMu is held for writing, so a
// lock held across a bootstrap fails the caller's deadline instead of hanging it.
func bootstrapGateUsers(a *Adapter, key string) int {
	if !a.bootstrapMu.TryRLock() {
		return -1
	}
	defer a.bootstrapMu.RUnlock()
	if gate := a.bootstrapGates[key]; gate != nil {
		return gate.users
	}
	return 0
}

func assertNoBootstrapGates(t *testing.T, a *Adapter) {
	t.Helper()
	a.bootstrapMu.RLock()
	defer a.bootstrapMu.RUnlock()
	if len(a.bootstrapGates) != 0 {
		t.Fatalf("%d bootstrap gates left after every caller returned, want 0", len(a.bootstrapGates))
	}
}

// awaitGateUsers waits until n callers hold or wait for key's gate. It fails
// at once if a caller reaches CREATE INDEX instead, which a waiter must not.
func awaitGateUsers(t *testing.T, a *Adapter, c *gatedBootstrapConn, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(stallBound)
	for bootstrapGateUsers(a, key) != n {
		select {
		case got := <-c.entered:
			t.Fatalf("a caller reached CREATE INDEX for %q while another bootstrap of the key was in flight", got)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate users of %q = %d after %s, want %d", key, bootstrapGateUsers(a, key), stallBound, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// Two writers of one key: one bootstraps, the other waits for it and does not
// send the schema statements again.
func TestTwoWritersOfOneKeyBootstrapItOnce(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter := newGatedAdapter(t, c)
	key := writeKeyFor(t, adapter, "org-a")

	c.gate(key)
	first := applyAsync(adapter, "org-a")
	awaitEntered(t, c, key)
	second := applyAsync(adapter, "org-a")
	awaitGateUsers(t, adapter, c, key, 2)

	c.open(key)
	awaitApplied(t, first, "the writer that bootstraps")
	awaitApplied(t, second, "the writer that waits for the bootstrap")
	if calls, maxInFlight := c.stats(key); calls != 1 || maxInFlight != 1 {
		t.Fatalf("CREATE INDEX calls/max in flight = %d/%d, want 1/1", calls, maxInFlight)
	}
	if !bootstrapCached(adapter, key) {
		t.Fatal("the key must read as bootstrapped")
	}
	assertNoBootstrapGates(t, adapter)
}

// A bootstrap that fails hands the key's gate to the writer already waiting
// for it. A writer that arrives while that second bootstrap runs waits on the
// same gate: never a second gate, never two bootstraps of one key at once.
func TestAFailedBootstrapHandsTheGateToTheWaiterAndANewcomerWaits(t *testing.T) {
	c := newGatedBootstrapConn(t)
	adapter := newGatedAdapter(t, c)
	key := writeKeyFor(t, adapter, "org-a")

	c.gate(key)
	c.failNextOf(key, errBootstrapRefused)
	first := applyAsync(adapter, "org-a")
	awaitEntered(t, c, key)
	second := applyAsync(adapter, "org-a")
	awaitGateUsers(t, adapter, c, key, 2)

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
	awaitGateUsers(t, adapter, c, key, 2)

	c.open(key)
	awaitApplied(t, second, "the waiter that bootstraps after the failure")
	awaitApplied(t, newcomer, "the newcomer")
	if calls, maxInFlight := c.stats(key); calls != 2 || maxInFlight != 1 {
		t.Fatalf("CREATE INDEX calls/max in flight = %d/%d, want 2/1", calls, maxInFlight)
	}
	if !bootstrapCached(adapter, key) {
		t.Fatal("the key must read as bootstrapped")
	}
	assertNoBootstrapGates(t, adapter)
}
