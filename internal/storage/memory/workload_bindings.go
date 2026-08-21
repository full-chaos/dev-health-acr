package memory

import (
	"context"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkloadBindingStore is an in-memory storage.WorkloadBindingStore for
// tests. Production always uses postgres.WorkloadBindingStore; there is no
// CRUD API for this table (see storage.WorkloadBinding's doc comment), so
// tests seed rows directly via Put.
type WorkloadBindingStore struct {
	mu   sync.RWMutex
	rows map[storage.WorkloadBindingKey]storage.WorkloadBinding
}

func NewWorkloadBindingStore() *WorkloadBindingStore {
	return &WorkloadBindingStore{rows: make(map[storage.WorkloadBindingKey]storage.WorkloadBinding)}
}

// Put seeds (or replaces) a binding row for key. Test-only; production has
// no write path for this table.
func (s *WorkloadBindingStore) Put(key storage.WorkloadBindingKey, binding storage.WorkloadBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding.RepositoryScopes = append([]string(nil), binding.RepositoryScopes...)
	s.rows[key] = binding
}

func (s *WorkloadBindingStore) Lookup(ctx context.Context, key storage.WorkloadBindingKey) (storage.WorkloadBinding, error) {
	if err := ctx.Err(); err != nil {
		return storage.WorkloadBinding{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.rows[key]
	if !ok {
		return storage.WorkloadBinding{}, storage.ErrNotFound
	}
	// Defensive copy on the way out too -- codex round 1 finding: without
	// it, a caller mutating the returned slice would silently mutate this
	// store's own row, the same aliasing bug postgres's version can't
	// have (it decodes a fresh slice from JSON on every read).
	binding.RepositoryScopes = append([]string(nil), binding.RepositoryScopes...)
	return binding, nil
}
