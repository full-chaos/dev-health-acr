package entitlements

import (
	"sync"
	"time"
)

type grants struct {
	runtime      bool
	serviceModel bool
}

type cachedEntitlement struct {
	grants  grants
	expires time.Time
}

type cache struct {
	mu       sync.Mutex
	items    map[string]cachedEntitlement
	capacity int
}

func newCache(capacity int) cache {
	return cache{items: make(map[string]cachedEntitlement, capacity), capacity: capacity}
}

func (c *cache) get(key string, now time.Time) (grants, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok {
		return grants{}, false
	}
	if !now.Before(entry.expires) {
		delete(c.items, key)
		return grants{}, false
	}
	return entry.grants, true
}

func (c *cache) put(key string, value grants, expires time.Time) {
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
	c.items[key] = cachedEntitlement{grants: value, expires: expires}
}

func (c *cache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}
