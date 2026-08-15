package embedcache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// fakeEmbedder is a minimal contextfabric.Embedder whose behavior a test can
// script: which identity it reports, what it returns per call, and how many
// times its Embed method was actually invoked (the ground truth for whether
// the cache saved a round-trip).
type fakeEmbedder struct {
	mu       sync.Mutex
	identity contextfabric.EmbedderIdentity
	calls    int
	// nextErr / nextVectors, when set, are consumed by the NEXT call only,
	// so a test can script one failing call inside an otherwise successful
	// sequence.
	nextErr     error
	nextVectors [][]float32
	// block, when non-nil, is waited on BEFORE the call is counted or
	// answered -- it lets a coalescing test hold the singleflight leader
	// open while other goroutines join the same in-flight call.
	block chan struct{}
}

func (f *fakeEmbedder) Identity() contextfabric.EmbedderIdentity {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.nextErr != nil {
		err := f.nextErr
		f.nextErr = nil
		return nil, err
	}
	if f.nextVectors != nil {
		out := f.nextVectors
		f.nextVectors = nil
		return out, nil
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = []float32{deterministicVectorValue(text)}
	}
	return out, nil
}

func (f *fakeEmbedder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// deterministicVectorValue gives each distinct text a distinct, deterministic
// "vector" so a test can tell which text actually produced a result.
func deterministicVectorValue(text string) float32 {
	var sum float32
	for _, r := range text {
		sum += float32(r)
	}
	return sum
}

func TestRepeatedQueryHitsCache(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)

	ctx := context.Background()
	first, err := cache.Embed(ctx, []string{"which projects are behind"})
	if err != nil {
		t.Fatalf("first Embed: %v", err)
	}
	second, err := cache.Embed(ctx, []string{"which projects are behind"})
	if err != nil {
		t.Fatalf("second Embed: %v", err)
	}
	if inner.callCount() != 1 {
		t.Fatalf("inner embedder called %d times, want 1 (second call should hit cache)", inner.callCount())
	}
	if len(first) != 1 || len(second) != 1 || first[0][0] != second[0][0] {
		t.Fatalf("cached result diverged from original: %v vs %v", first, second)
	}
	metrics := cache.Metrics()
	if metrics.Hits != 1 || metrics.Misses != 1 {
		t.Fatalf("metrics = %+v, want 1 hit / 1 miss", metrics)
	}
}

func TestDistinctTextMisses(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	ctx := context.Background()

	if _, err := cache.Embed(ctx, []string{"question one"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := cache.Embed(ctx, []string{"question two"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("inner embedder called %d times, want 2 (distinct texts must not share a cache entry)", inner.callCount())
	}
	metrics := cache.Metrics()
	if metrics.Hits != 0 || metrics.Misses != 2 {
		t.Fatalf("metrics = %+v, want 0 hits / 2 misses", metrics)
	}
}

// TestIdentityChangeMisses proves the load-bearing invariant from the
// CHAOS-3742 spec review: the cache key carries the embedder's full,
// OPAQUE identity string, so a rebuild/re-embed that changes identity
// (a different provider/model, or -- post T3 -- a different composition
// tag) naturally MISSES rather than serving a vector from the wrong
// generation. Nothing in this package parses or special-cases the
// identity string; this test is what demonstrates the property holds.
func TestIdentityChangeMisses(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	ctx := context.Background()
	const text = "which projects are behind"

	if _, err := cache.Embed(ctx, []string{text}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Simulate a rebuild that swaps in a new embedder identity (e.g. the
	// composition tag suffix T3 will append, or simply a different model).
	inner.mu.Lock()
	inner.identity = contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large#t2:r2000:b1"}
	inner.mu.Unlock()

	if _, err := cache.Embed(ctx, []string{text}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("inner embedder called %d times, want 2 (identity change must miss even for identical text)", inner.callCount())
	}
	metrics := cache.Metrics()
	if metrics.Hits != 0 || metrics.Misses != 2 {
		t.Fatalf("metrics = %+v, want 0 hits / 2 misses across an identity change", metrics)
	}
}

func TestErrorsAreNeverCached(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	ctx := context.Background()
	const text = "which projects are behind"

	inner.nextErr = errors.New("provider unavailable")
	if _, err := cache.Embed(ctx, []string{text}); err == nil {
		t.Fatal("expected the scripted error to surface")
	}
	// A retry of the SAME text must go back to the provider: the failed
	// call must not have been memoized.
	if _, err := cache.Embed(ctx, []string{text}); err != nil {
		t.Fatalf("retry after a failed call: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("inner embedder called %d times, want 2 (an error must never be cached)", inner.callCount())
	}
	metrics := cache.Metrics()
	if metrics.Hits != 0 {
		t.Fatalf("metrics = %+v, want 0 hits (nothing legitimate was ever stored)", metrics)
	}
}

func TestMalformedResponseIsNeverCached(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	ctx := context.Background()
	const text = "which projects are behind"

	// A response that does not pair one vector with the one input text is
	// exactly the ErrResponseShape condition the Embedder port's doc
	// comment calls out; the cache must treat it the same as an error.
	inner.nextVectors = [][]float32{{1}, {2}}
	if _, err := cache.Embed(ctx, []string{text}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := cache.Embed(ctx, []string{text}); err != nil {
		t.Fatalf("second Embed: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("inner embedder called %d times, want 2 (a malformed response must never be cached)", inner.callCount())
	}
}

func TestBatchCallsBypassCache(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	ctx := context.Background()
	texts := []string{"doc one", "doc two", "doc three"}

	if _, err := cache.Embed(ctx, texts); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := cache.Embed(ctx, texts); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("inner embedder called %d times, want 2 (a projection batch call must never be cached)", inner.callCount())
	}
	metrics := cache.Metrics()
	if metrics.Hits != 0 || metrics.Misses != 0 {
		t.Fatalf("metrics = %+v, want 0/0 (batch calls do not touch the query cache)", metrics)
	}
}

func TestEvictionRespectsMaxEntries(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 2)
	ctx := context.Background()

	if _, err := cache.Embed(ctx, []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := cache.Embed(ctx, []string{"b"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// "c" evicts the LRU entry ("a", untouched since it was written).
	if _, err := cache.Embed(ctx, []string{"c"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, err := cache.Embed(ctx, []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if inner.callCount() != 4 {
		t.Fatalf("inner embedder called %d times, want 4 (a evicted, so its re-request must miss)", inner.callCount())
	}
	if len(cache.items) > 2 {
		t.Fatalf("cache holds %d entries, want at most 2", len(cache.items))
	}
}

func TestReturnedVectorIsNotAliasedWithCacheStorage(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	ctx := context.Background()
	const text = "which projects are behind"

	first, err := cache.Embed(ctx, []string{text})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// A caller mutating its own returned slice must never corrupt the
	// cache's stored copy.
	first[0][0] = -9999

	second, err := cache.Embed(ctx, []string{text})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if second[0][0] == -9999 {
		t.Fatal("cache hit returned a vector aliased with a caller-mutated slice")
	}
	if inner.callCount() != 1 {
		t.Fatalf("inner embedder called %d times, want 1", inner.callCount())
	}
}

// TestCanceledContextIsNeverCached is codex round 1 finding 2: a result
// produced while ctx was already canceled or past its deadline must not be
// cached, even though the fake embedder (like a real one might, under a
// race) still happily returned a well-shaped vector.
func TestCanceledContextIsNeverCached(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 0)
	const text = "which projects are behind"

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cache.Embed(canceled, []string{text}); err != nil {
		t.Fatalf("Embed with a canceled context: %v", err)
	}
	if inner.callCount() != 1 {
		t.Fatalf("inner embedder called %d times, want 1", inner.callCount())
	}

	// A fresh, non-canceled request for the SAME text must still be a
	// miss: the canceled call's result must never have been stored.
	if _, err := cache.Embed(context.Background(), []string{text}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if inner.callCount() != 2 {
		t.Fatalf("inner embedder called %d times, want 2 (a canceled-context result must never be cached)", inner.callCount())
	}
	metrics := cache.Metrics()
	if metrics.Hits != 0 {
		t.Fatalf("metrics = %+v, want 0 hits", metrics)
	}
}

// TestEmptyVectorIsNeverCached is codex round 1 finding 3: a response that
// pairs the one input text with a zero-length vector is the malformed shape
// ErrResponseShape exists to name, and must never be cached -- caching it
// would replay a fault as a permanent, silent "no match" on the graph read
// path (falkorgraph.vectorSearchNodes treats an empty vector as finding
// nothing, not as an error).
func TestEmptyVectorIsNeverCached(t *testing.T) {
	for name, vectors := range map[string][][]float32{
		"nil vector":   {nil},
		"empty vector": {{}},
	} {
		t.Run(name, func(t *testing.T) {
			inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
			cache := New(inner, 0)
			ctx := context.Background()
			const text = "which projects are behind"

			inner.nextVectors = vectors
			if _, err := cache.Embed(ctx, []string{text}); err != nil {
				t.Fatalf("Embed: %v", err)
			}
			if _, err := cache.Embed(ctx, []string{text}); err != nil {
				t.Fatalf("second Embed: %v", err)
			}
			if inner.callCount() != 2 {
				t.Fatalf("inner embedder called %d times, want 2 (%s must never be cached)", inner.callCount(), name)
			}
		})
	}
}

// TestConcurrentIdenticalMissesCoalesceToOneProviderCall is codex round 1
// finding 4: N simultaneous cold requests for the SAME (identity, text)
// must share ONE provider round-trip, not multiply it.
func TestConcurrentIdenticalMissesCoalesceToOneProviderCall(t *testing.T) {
	block := make(chan struct{})
	inner := &fakeEmbedder{
		identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"},
		block:    block,
	}
	cache := New(inner, 0)
	ctx := context.Background()
	const text = "which projects are behind"
	const concurrency = 20

	var wg sync.WaitGroup
	results := make([][][]float32, concurrency)
	errs := make([]error, concurrency)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = cache.Embed(ctx, []string{text})
		}(i)
	}
	// Give every goroutine time to reach and join the in-flight
	// singleflight call before releasing the leader.
	time.Sleep(100 * time.Millisecond)
	close(block)
	wg.Wait()

	if inner.callCount() != 1 {
		t.Fatalf("inner embedder called %d times, want 1 (concurrent identical misses must coalesce)", inner.callCount())
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if len(results[i]) != 1 || results[i][0][0] != results[0][0][0] {
			t.Fatalf("goroutine %d result %v diverged from goroutine 0 result %v", i, results[i], results[0])
		}
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	inner := &fakeEmbedder{identity: contextfabric.EmbedderIdentity{Provider: "openai", Model: "text-embedding-3-large"}}
	cache := New(inner, 8)
	ctx := context.Background()

	var wg sync.WaitGroup
	texts := []string{"one", "two", "three", "four", "five"}
	for i := 0; i < 50; i++ {
		text := texts[i%len(texts)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Embed(ctx, []string{text}); err != nil {
				t.Errorf("concurrent Embed: %v", err)
			}
		}()
	}
	wg.Wait()
}
