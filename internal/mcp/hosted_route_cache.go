package mcp

import (
	"container/list"
	"sync"
	"time"
)

type hostedRouteCache struct {
	mu   sync.Mutex
	max  int
	ttl  time.Duration
	now  func() time.Time
	byID map[string]*list.Element
	lru  *list.List
}
type hostedRouteEntry struct {
	id      string
	expires time.Time
}

func newHostedRouteCache(max int, ttl time.Duration, now func() time.Time) *hostedRouteCache {
	return &hostedRouteCache{max: max, ttl: ttl, now: now, byID: map[string]*list.Element{}, lru: list.New()}
}
func (c *hostedRouteCache) put(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.byID[id]; ok {
		e.Value = hostedRouteEntry{id, c.now().Add(c.ttl)}
		c.lru.MoveToFront(e)
		return
	}
	c.byID[id] = c.lru.PushFront(hostedRouteEntry{id, c.now().Add(c.ttl)})
	for c.lru.Len() > c.max {
		e := c.lru.Back()
		delete(c.byID, e.Value.(hostedRouteEntry).id)
		c.lru.Remove(e)
	}
}
func (c *hostedRouteCache) has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byID[id]
	if !ok {
		return false
	}
	if !c.now().Before(e.Value.(hostedRouteEntry).expires) {
		delete(c.byID, id)
		c.lru.Remove(e)
		return false
	}
	c.lru.MoveToFront(e)
	return true
}
