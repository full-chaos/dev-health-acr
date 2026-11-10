package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkloadBindingStore is an in-memory storage.WorkloadBindingStore for
// tests. Production always uses postgres.WorkloadBindingStore; there is no
// CRUD API for this table (see storage.WorkloadBinding's doc comment), so
// tests seed rows directly via Put.
type WorkloadBindingStore struct {
	mu   sync.RWMutex
	rows map[storage.WorkloadBindingKey][]storage.WorkloadBinding
}

func NewWorkloadBindingStore() *WorkloadBindingStore {
	return &WorkloadBindingStore{rows: make(map[storage.WorkloadBindingKey][]storage.WorkloadBinding)}
}

// Put seeds (or replaces, per organization and org scope) a binding row for key. Test-only; production has
// no write path for this table.
func (s *WorkloadBindingStore) Put(key storage.WorkloadBindingKey, binding storage.WorkloadBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding.RepositoryScopes = append([]string(nil), binding.RepositoryScopes...)
	if binding.OrgScope == "" {
		binding.OrgScope = storage.WorkloadOrgScopeOrg
	}
	rows := append([]storage.WorkloadBinding(nil), s.rows[key]...)
	replaced := false
	for i := range rows {
		if rows[i].OrgScope == binding.OrgScope && rows[i].OrgID == binding.OrgID {
			rows[i], replaced = binding, true
		}
	}
	if !replaced {
		rows = append(rows, binding)
	}
	// postgres's order: org_id with a live_orgs row (no org_id) last.
	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].OrgID == "") != (rows[j].OrgID == "") {
			return rows[j].OrgID == ""
		}
		return rows[i].OrgID < rows[j].OrgID
	})
	s.rows[key] = rows
}

func (s *WorkloadBindingStore) Lookup(ctx context.Context, key storage.WorkloadBindingKey) ([]storage.WorkloadBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, ok := s.rows[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	// Defensive copy on the way out too: a caller mutating the returned
	// slices must not mutate this store's own rows (the aliasing bug
	// postgres's version can't have: it decodes fresh slices on every read).
	out := make([]storage.WorkloadBinding, len(stored))
	for i, binding := range stored {
		binding.RepositoryScopes = append([]string(nil), binding.RepositoryScopes...)
		out[i] = binding
	}
	return out, nil
}
