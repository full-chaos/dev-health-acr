package mcp

import (
	"sync"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/stretchr/testify/require"
)

func TestFederation_CacheLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	cache := newLocalEvidenceCache(1024, time.Minute, func() time.Time { return now })
	entry := cachedLocalEvidence{ref: contractsv1.EvidenceRef{EvidenceRefID: "a", Metadata: map[string]any{"key": "value"}}}
	cache.putBatch([]cachedLocalEvidence{entry})
	got, found := cache.get("a")
	got.ref.Metadata["key"] = "changed"
	now = now.Add(time.Minute)
	_, expired := cache.get("a")
	require.True(t, found)
	require.False(t, expired)
	require.Equal(t, "value", entry.ref.Metadata["key"])
	for index := range 1025 {
		cache.putBatch([]cachedLocalEvidence{{ref: contractsv1.EvidenceRef{EvidenceRefID: string(rune(index + 1))}}})
	}
	require.LessOrEqual(t, cache.lru.Len(), 1024)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { _, _ = cache.get("missing") })
	}
	group.Wait()
}
