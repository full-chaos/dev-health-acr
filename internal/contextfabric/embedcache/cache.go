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
	"sync"
	"sync/atomic"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
// well-shaped result -- exactly one vector for the one input text -- is
// ever stored: an error or a malformed response is never cached, so a
// transient provider fault is never replayed as a permanent one.
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
	vectors, err := c.inner.Embed(ctx, texts)
	if err != nil || len(vectors) != 1 {
		return vectors, err
	}
	c.put(key, vectors[0])
	return vectors, nil
}

// Metrics returns a snapshot of hit/miss counts accumulated since
// construction.
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
