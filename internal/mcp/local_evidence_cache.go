package mcp

import (
	"container/list"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

type cachedLocalEvidence struct {
	evidence sidecar.LocalExpandedEvidence
	ref      contractsv1.EvidenceRef
	expires  time.Time
}

type localCacheEntry struct {
	key   string
	value cachedLocalEvidence
}

type localEvidenceCache struct {
	mu   sync.Mutex
	max  int
	ttl  time.Duration
	now  func() time.Time
	byID map[string]*list.Element
	lru  *list.List
}

func newLocalEvidenceCache(max int, ttl time.Duration, now func() time.Time) *localEvidenceCache {
	return &localEvidenceCache{max: max, ttl: ttl, now: now, byID: map[string]*list.Element{}, lru: list.New()}
}

func (c *localEvidenceCache) get(id string) (cachedLocalEvidence, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.byID[id]
	if !ok {
		return cachedLocalEvidence{}, false
	}
	entry := element.Value.(localCacheEntry)
	if !c.now().Before(entry.value.expires) {
		c.lru.Remove(element)
		delete(c.byID, id)
		return cachedLocalEvidence{}, false
	}
	c.lru.MoveToFront(element)
	return copyCachedLocalEvidence(entry.value), true
}

func (c *localEvidenceCache) putBatch(entries []cachedLocalEvidence) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for _, value := range entries {
		value.expires = now.Add(c.ttl)
		value = copyCachedLocalEvidence(value)
		if element, ok := c.byID[value.ref.EvidenceRefID]; ok {
			element.Value = localCacheEntry{key: value.ref.EvidenceRefID, value: value}
			c.lru.MoveToFront(element)
			continue
		}
		c.byID[value.ref.EvidenceRefID] = c.lru.PushFront(localCacheEntry{key: value.ref.EvidenceRefID, value: value})
	}
	for c.lru.Len() > c.max {
		element := c.lru.Back()
		entry := element.Value.(localCacheEntry)
		delete(c.byID, entry.key)
		c.lru.Remove(element)
	}
}

func copyCachedLocalEvidence(value cachedLocalEvidence) cachedLocalEvidence {
	copy := value
	copy.ref.Metadata = make(map[string]any, len(value.ref.Metadata))
	for key, field := range value.ref.Metadata {
		copy.ref.Metadata[key] = field
	}
	return copy
}
