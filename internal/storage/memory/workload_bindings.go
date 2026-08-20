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
	return binding, nil
}
