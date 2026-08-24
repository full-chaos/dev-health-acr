package pglifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// lifecycleReader is the narrow read-only slice of contextfabric.GraphLifecycleStore
// Resolver needs -- kept as its own interface so a test can fake exactly
// this and nothing else.
type lifecycleReader interface {
	Get(ctx context.Context, orgID string) (contextfabric.OrgGraphLifecycle, bool, error)
}

// Resolver is the uncached contextfabric.OrgEpochResolver, backed directly
// by a GraphLifecycleStore read. See CachedResolver for the bounded-lease
// wrapper design brief §3.5 requires ("readers cache the pointer under a
// bounded lease L").
type Resolver struct {
	store lifecycleReader
}

func NewResolver(store lifecycleReader) (*Resolver, error) {
	if store == nil {
		return nil, errors.New("pglifecycle: resolver requires a lifecycle store")
	}
	return &Resolver{store: store}, nil
}

var _ contextfabric.OrgEpochResolver = (*Resolver)(nil)

func (r *Resolver) ResolveActiveEpoch(ctx context.Context, orgID string) (int64, error) {
	row, found, err := r.store.Get(ctx, orgID)
	if err != nil {
		return 0, err
	}
	if !found {
		// No lifecycle row: LifecycleStatusServing at epoch 0, the legacy
		// key -- the zero-migration default (contextfabric.OrgGraphLifecycle's
		// own doc comment).
		return 0, nil
	}
	return row.ActiveEpoch, nil
}

func (r *Resolver) ResolveBuildEpoch(ctx context.Context, orgID string) (int64, bool, error) {
	row, found, err := r.store.Get(ctx, orgID)
	if err != nil {
		return 0, false, err
	}
	if !found || row.Status != contextfabric.LifecycleStatusBuilding || row.TargetEpoch == nil {
		return 0, false, nil
	}
	return *row.TargetEpoch, true, nil
}

// MaxCachedResolverLease bounds CachedResolver's TTL (design brief §3.5:
// "KeyResolver refuses unbounded leases" -- the drain-bound soundness
// argument for GRAPH.DELETE, §3.5's "Drain before delete", is only sound
// if every reader's cache entry provably expires within a small, known
// bound). Ten minutes is generous relative to the grace window (hours,
// design brief D11) while still being a small, auditable number, not "as
// long as the process lives".
const MaxCachedResolverLease = 10 * time.Minute

type cacheEntry struct {
	activeEpoch int64
	buildEpoch  int64
	buildOK     bool
	expiresAt   time.Time
}

// CachedResolver wraps any contextfabric.OrgEpochResolver with a bounded
// TTL lease per organization (design brief §3.5): a stale cache entry can
// serve the OLD complete graph briefly after a flip, which is the ordinary
// staleness class the taint gate (S2) handles, never a correctness hazard
// on its own -- but the lease MUST be bounded, because §3.5's drain-before-
// delete argument (GRAPH.DELETE issued no earlier than
// drain_start + lease + deadline) is only sound if every reader's cache
// entry provably expires within a known bound. The constructor refuses a
// non-positive or unbounded (> MaxCachedResolverLease) lease outright.
type CachedResolver struct {
	inner contextfabric.OrgEpochResolver
	lease time.Duration
	now   func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
	// generation is CHAOS-4208 round-2's per-org invalidation counter,
	// bumped by Invalidate. refresh's own store reads happen OUTSIDE the
	// mutex (they're the whole point of not serializing every org's I/O
	// behind one lock), so a refresh already in flight when Invalidate
	// fires can still be holding a pre-transition read when it goes to
	// write its result back -- refresh captures generation before it
	// reads, and only commits its result to entries if the generation is
	// still what it captured, so a since-fired Invalidate can never be
	// silently overwritten by a stale write racing behind it.
	generation map[string]uint64
}

// CachedResolverOptions configures CachedResolver. Now defaults to
// time.Now; tests override it for deterministic expiry.
type CachedResolverOptions struct {
	Now func() time.Time
}

func NewCachedResolver(inner contextfabric.OrgEpochResolver, lease time.Duration, options CachedResolverOptions) (*CachedResolver, error) {
	if inner == nil {
		return nil, errors.New("pglifecycle: cached resolver requires an inner resolver")
	}
	if lease <= 0 || lease > MaxCachedResolverLease {
		return nil, fmt.Errorf("pglifecycle: cached resolver lease must be a bounded positive duration (0, %s]", MaxCachedResolverLease)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &CachedResolver{inner: inner, lease: lease, now: now, entries: make(map[string]cacheEntry), generation: make(map[string]uint64)}, nil
}

var _ contextfabric.OrgEpochResolver = (*CachedResolver)(nil)

func (c *CachedResolver) ResolveActiveEpoch(ctx context.Context, orgID string) (int64, error) {
	entry, ok := c.lookup(orgID)
	if ok {
		return entry.activeEpoch, nil
	}
	entry, err := c.refresh(ctx, orgID)
	if err != nil {
		return 0, err
	}
	return entry.activeEpoch, nil
}

func (c *CachedResolver) ResolveBuildEpoch(ctx context.Context, orgID string) (int64, bool, error) {
	entry, ok := c.lookup(orgID)
	if ok {
		return entry.buildEpoch, entry.buildOK, nil
	}
	entry, err := c.refresh(ctx, orgID)
	if err != nil {
		return 0, false, err
	}
	return entry.buildEpoch, entry.buildOK, nil
}

func (c *CachedResolver) lookup(orgID string) (cacheEntry, bool) {
	orgID = strings.TrimSpace(orgID)
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok || c.now().After(entry.expiresAt) {
		return cacheEntry{}, false
	}
	return entry, true
}

func (c *CachedResolver) refresh(ctx context.Context, orgID string) (cacheEntry, error) {
	orgID = strings.TrimSpace(orgID)
	c.mu.Lock()
	startGeneration := c.generation[orgID]
	c.mu.Unlock()

	active, err := c.inner.ResolveActiveEpoch(ctx, orgID)
	if err != nil {
		return cacheEntry{}, err
	}
	buildEpoch, buildOK, err := c.inner.ResolveBuildEpoch(ctx, orgID)
	if err != nil {
		return cacheEntry{}, err
	}
	entry := cacheEntry{activeEpoch: active, buildEpoch: buildEpoch, buildOK: buildOK, expiresAt: c.now().Add(c.lease)}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation[orgID] != startGeneration {
		// Invalidate fired while this refresh's reads were in flight -- the
		// reads above may already be stale relative to the transition that
		// triggered it. Still correct to RETURN to this one caller (it's the
		// same snapshot-read guarantee any uncached call would give), but it
		// must not be cached: caching it would silently resurrect the exact
		// pre-transition entry Invalidate just cleared.
		return entry, nil
	}
	c.entries[orgID] = entry
	return entry, nil
}

// Invalidate drops orgID's cached entry immediately, so the NEXT resolve
// call re-reads the store rather than waiting out the lease. Not required
// for correctness (a stale cached pointer only ever serves a complete old
// graph, design brief §3.3), but flip/rollback/beginLifecycleBuild call
// this on their own process's cache as a courtesy so a request immediately
// following a transition this same process just performed sees it without
// waiting out the lease. Also bumps orgID's generation counter so a
// refresh already in flight when this fires cannot overwrite the
// invalidation with a stale result once its own reads complete (see
// refresh's own doc comment).
func (c *CachedResolver) Invalidate(orgID string) {
	orgID = strings.TrimSpace(orgID)
	c.mu.Lock()
	delete(c.entries, orgID)
	c.generation[orgID]++
	c.mu.Unlock()
}
