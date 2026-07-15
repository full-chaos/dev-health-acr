package auth

import (
	"sync"
	"time"
)

type webAssertionReplays struct {
	mu       sync.Mutex
	byJTI    map[string]time.Time
	capacity int
}

func (r *webAssertionReplays) observe(jti string, expiresAt, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for candidate, expiry := range r.byJTI {
		if !expiry.After(now) {
			delete(r.byJTI, candidate)
		}
	}
	if _, found := r.byJTI[jti]; found {
		return true
	}
	if len(r.byJTI) >= r.capacity {
		return true
	}
	r.byJTI[jti] = expiresAt
	return false
}
