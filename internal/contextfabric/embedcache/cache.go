// Package embedcache caches the READ path's query-side embedding calls
// (CHAOS-3841 / CHAOS-3742 spec §5 L15).
//
// hybridSearchNodes (falkorgraph/vector.go) embeds the user's question text
// on every hybrid search. A repeated or retried question re-pays the
// provider round-trip -- latency and, for a metered remote provider, cost --
// for a text it has already embedded. This package wraps a
// contextfabric.Embedder with a small in-process LRU keyed on the embedder's
// own identity plus the exact query text, so a repeat resolves from memory.
//
// Scope is deliberately narrow: this cache is a pure efficiency layer with
// zero semantic effect. It caches ONLY the single-text calls the read path
// makes (contextfabric.Embedder's doc comment: "one call per projection
// batch... and with a single text on the read path") -- a projection
// batch's multi-text call always passes straight through, uncached, so
// wrapping the embedder never changes what acr-projector writes.
//
// The cache key carries the embedder's FULL identity string
// (contextfabric.EmbedderIdentity.String(), which after CHAOS-3742 T3 also
// carries the composition tag) as one OPAQUE value -- never parsed or
// decomposed. That is what makes correctness free by construction: any
// change that would make an old cached vector wrong (a different provider,
// model, or -- once T3 lands -- template/config generation) is by
// definition a different identity string, so it is a different cache key
// and a guaranteed miss. See TestIdentityChangeMisses.
package embedcache

import (
	"container/list"
	"context"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"golang.org/x/sync/singleflight"
)

// DefaultMaxEntries bounds the cache when no size is configured. It is a
// small number deliberately: the value this cache captures is REPEATED
// query text, not broad coverage of a corpus, and an oversized cache just
// spends memory holding entries that will never be asked for again.
const DefaultMaxEntries = 512

// cacheKey is a plain comparable struct, not a delimited string. A
// delimiter-joined key ("identity\x00text") would need an escaping
// argument to rule out a crafted text colliding with a different
// (identity, text) pair; a struct key has no such argument to make.
type cacheKey struct {
	identity string
	text     string
}

type cacheEntry struct {
	key    cacheKey
	vector []float32
}

// Metrics is a point-in-time, cumulative-since-construction snapshot of
// cache effectiveness (CHAOS-3841: "counters for hit/miss so effectiveness
// is observable").
type Metrics struct {
	Hits   uint64
	Misses uint64
}

// Cache wraps a contextfabric.Embedder with a bounded LRU over its
// single-text Embed calls. Safe for concurrent use, per the Embedder port's
// own requirement.
type Cache struct {
	inner      contextfabric.Embedder
	maxEntries int

	mu    sync.Mutex
	order *list.List
	items map[cacheKey]*list.Element

	// group coalesces concurrent identical misses (codex round 1, finding
	// 4): N simultaneous cold requests for the SAME (identity, text) share
	// ONE call to inner.Embed rather than each paying its own provider
	// round-trip. Zero value is ready to use.
	group singleflight.Group

	hits   atomic.Uint64
	misses atomic.Uint64
}

// New wraps embedder with a query-embedding cache bounded to maxEntries
// distinct (identity, text) pairs. maxEntries <= 0 falls back to
// DefaultMaxEntries rather than being treated as "unbounded" -- an
// unbounded read-driven cache is an uncapped memory grant to whatever text
// callers send it.
func New(embedder contextfabric.Embedder, maxEntries int) *Cache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	return &Cache{
		inner:      embedder,
		maxEntries: maxEntries,
		order:      list.New(),
		items:      make(map[cacheKey]*list.Element),
	}
}

// Identity delegates verbatim. Wrapping must be invisible to every caller
// that only cares which model produced a vector (AC-3778-7's fence, the
// composition-tag stamp, and this package's own cache key all read this
// value).
func (c *Cache) Identity() contextfabric.EmbedderIdentity {
	return c.inner.Identity()
}

// Embed caches only single-text calls; every other call (the projection
// batch shape) passes straight through uncached. Only a successful,
// well-shaped result -- exactly one non-empty vector for the one input
// text, produced by a request that was not already canceled or past its
// deadline -- is ever stored. See maybeCache.
func (c *Cache) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) != 1 {
		return c.inner.Embed(ctx, texts)
	}
	key := cacheKey{identity: c.inner.Identity().String(), text: texts[0]}
	if vector, ok := c.get(key); ok {
		c.hits.Add(1)
		return [][]float32{vector}, nil
	}
	c.misses.Add(1)
	return c.embedAndCache(ctx, key, texts)
}

// embedOutcome carries a coalesced call's result through singleflight.Group,
// which shares ONE value across every waiter. The outcome is encoded here
// rather than in Do's own error return, so a coalesced provider error is
// delivered to every waiter exactly as inner.Embed produced it, not
// reinterpreted by singleflight's own error-propagation semantics (the same
// posture modelruntimeresolver.Resolver takes for the same reason).
type embedOutcome struct {
	vectors [][]float32
	err     error
}

// embedAndCache runs inner.Embed for key exactly once across however many
// callers are concurrently asking for the SAME (identity, text) pair
// (codex round 1, finding 4), then decides whether the result is safe to
// keep for the next caller.
//
// Only the caller that becomes the singleflight LEADER supplies the ctx an
// in-flight call actually runs under; a caller that joins an already
// in-flight request waits on the leader's outcome rather than starting a
// second provider call under its own ctx. That is an accepted, narrow
// consequence of coalescing at all, not an oversight -- the alternative
// (one provider call per waiter) is exactly the multiplication this exists
// to prevent.
func (c *Cache) embedAndCache(ctx context.Context, key cacheKey, texts []string) ([][]float32, error) {
	value, err, _ := c.group.Do(singleflightKey(key), func() (interface{}, error) {
		vectors, embedErr := c.inner.Embed(ctx, texts)
		c.maybeCache(ctx, key, vectors, embedErr)
		return embedOutcome{vectors: vectors, err: embedErr}, nil
	})
	if err != nil {
		// Do's own function above always returns a nil error (the outcome
		// carries embedErr instead), so reaching this means Do itself
		// malfunctioned -- defensive, not a path this package's own logic
		// can produce.
		return nil, err
	}
	outcome := value.(embedOutcome)
	// codex round 2, the surviving HIGH: singleflight.Group hands the SAME
	// value -- one embedOutcome, wrapping the provider's own [][]float32 --
	// to every coalesced waiter. Returning outcome.vectors directly would
	// let two callers that both rode the same in-flight request share a
	// backing array: one mutating "its own" result would silently corrupt
	// what every other waiter sees. Every waiter -- leader included, so
	// there is exactly one code path to reason about, not a leader/
	// follower split -- gets its OWN clone. The value cached via
	// maybeCache above was already isolated by put's own copy; this clone
	// is solely about what crosses back out of this function.
	return cloneVectors(outcome.vectors), outcome.err
}

// maybeCache stores a result only when it is unambiguously reusable by a
// later, unrelated caller:
//   - no error from the provider;
//   - the request had not already been canceled or exceeded its deadline
//     at the moment this decision is made (codex round 1, finding 2) --
//     caching whatever a canceled call happened to return would let a
//     request's own cancellation silently determine what every FUTURE
//     caller for this text receives;
//   - exactly one vector for the one input text, and that vector
//     non-empty (codex round 1, finding 3) -- an empty vector is not a
//     smaller valid answer, it is the malformed-response shape
//     ErrResponseShape exists to name, and the graph read path
//     (falkorgraph.vectorSearchNodes) treats a zero-length vector as
//     silently finding nothing rather than erroring, so caching one would
//     replay a fault as a permanent, undiagnosable "no match".
//
// Any other shape is left uncached, so the next caller gets a fresh
// attempt rather than a replayed fault.
func (c *Cache) maybeCache(ctx context.Context, key cacheKey, vectors [][]float32, err error) {
	if err != nil || ctx.Err() != nil {
		return
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return
	}
	c.put(key, vectors[0])
}

// singleflightKey renders key as a string singleflight.Group can dedupe on,
// WITHOUT a plain delimiter join. "identity\x00text" would need an argument
// that no possible identity or text value could ever contain "\x00" -- an
// argument this package would have to keep re-proving as those values'
// sources change. A length-prefixed encoding needs no such argument: the
// decimal length before the first ':' pins exactly where identity ends
// regardless of what characters identity or text contain, so two distinct
// (identity, text) pairs can never render to the same string.
func singleflightKey(key cacheKey) string {
	return strconv.Itoa(len(key.identity)) + ":" + key.identity + ":" + key.text
}

// cloneVectors deep-copies a [][]float32 so the caller shares no backing
// array with whatever produced it -- the outer slice AND every inner
// vector. Nil-ness is preserved per element (a nil inner vector clones to
// nil, not an empty non-nil slice), matching whatever inner.Embed actually
// returned rather than normalizing it.
func cloneVectors(vectors [][]float32) [][]float32 {
	if vectors == nil {
		return nil
	}
	out := make([][]float32, len(vectors))
	for i, v := range vectors {
		if v == nil {
			continue
		}
		clone := make([]float32, len(v))
		copy(clone, v)
		out[i] = clone
	}
	return out
}

// Metrics returns a snapshot of hit/miss counts accumulated since
// construction. Hits and Misses are two INDEPENDENT atomic loads, not a
// single transactional read -- under concurrent traffic the pair can be
// momentarily inconsistent (e.g. a hit landing between the two loads is
// reflected in one field but not yet the other). That is acceptable for an
// observability counter and deliberately not fixed with a lock: a lock here
// would serialize every Metrics() caller against Embed for a guarantee
// nothing in this package needs.
func (c *Cache) Metrics() Metrics {
	return Metrics{Hits: c.hits.Load(), Misses: c.misses.Load()}
}

func (c *Cache) get(key cacheKey) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	stored := el.Value.(*cacheEntry).vector
	// Return a defensive copy: the caller and the cache must never share a
	// backing array, or a caller mutating its "own" slice would corrupt
	// every future hit (and a concurrent reader's slice underfoot).
	out := make([]float32, len(stored))
	copy(out, stored)
	return out, true
}

func (c *Cache) put(key cacheKey, vector []float32) {
	stored := make([]float32, len(vector))
	copy(stored, vector)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).vector = stored
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&cacheEntry{key: key, vector: stored})
	c.items[key] = el
	if c.order.Len() <= c.maxEntries {
		return
	}
	oldest := c.order.Back()
	if oldest == nil {
		return
	}
	c.order.Remove(oldest)
	delete(c.items, oldest.Value.(*cacheEntry).key)
}
