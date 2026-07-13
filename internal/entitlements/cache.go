package entitlements

import (
	"sync"
	"time"
)

type cachedEntitlement struct {
	entitled bool
	expires  time.Time
}

type cache struct {
	mu       sync.Mutex
	items    map[string]cachedEntitlement
	capacity int
}

func newCache(capacity int) cache {
	return cache{items: make(map[string]cachedEntitlement, capacity), capacity: capacity}
}

func (c *cache) get(key string, now time.Time) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return false, false
	}
	if !now.Before(entry.expires) {
		delete(c.items, key)
		return false, false
	}
	return entry.entitled, true
}

func (c *cache) put(key string, entitled bool, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.capacity {
		var oldestKey string
		var oldestExpiry time.Time
		for candidate, entry := range c.items {
			if oldestKey == "" || entry.expires.Before(oldestExpiry) {
				oldestKey = candidate
				oldestExpiry = entry.expires
			}
		}
		delete(c.items, oldestKey)
	}
	c.items[key] = cachedEntitlement{entitled: entitled, expires: expires}
}

func (c *cache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
