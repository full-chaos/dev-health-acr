// Package memoryinvestigation is the in-memory test/dev twin of
// contextfabric.InvestigationResultStore (mirrors internal/storage's
// memory-vs-postgres split, internal/storage/AGENTS.md). It enforces the
// same org-scoping and immutability semantics as
// internal/contextfabric/pginvestigation.Store so behavior cannot silently
// drift between the two implementations.
package memoryinvestigation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ErrNotFound identifies a Get that found no row for the requested
// (org_id, result_id). It fires identically whether result_id is genuinely
// unknown or belongs to a different organization, matching
// pginvestigation.ErrNotFound's non-enumerating-404 behavior.
var ErrNotFound = errors.New("memoryinvestigation: investigation result not found")

// entry holds an immutable, already-serialized snapshot. Storing the JSON
// form (rather than the Go struct) keeps Save's idempotent-replay
// comparison and Get's defensive copy on the same simple code path: encode
// once, decode fresh on every read.
type entry struct {
	orgID   string
	payload []byte
}

// Store is a mutex-protected, map-backed contextfabric.InvestigationResultStore.
type Store struct {
	mu      sync.Mutex
	results map[string]entry
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{results: make(map[string]entry)}
}

// Save persists an immutable InvestigationResult snapshot. It never
// overwrites an existing entry: an identical replay under the same
// result_id succeeds idempotently, a divergent one errors.
func (s *Store) Save(ctx context.Context, principal storage.Principal, result contextfabric.InvestigationResult) error {
	if s == nil {
		return errors.New("memoryinvestigation: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	resultID := strings.TrimSpace(result.ResultID)
	if orgID == "" || resultID == "" {
		return errors.New("memoryinvestigation: organization and result id are required")
	}
	// M2 (Codex adversarial review, CHAOS-3755): reject a semantically
	// invalid result before it is ever persisted -- an immutable row that
	// fails the same contract the public API enforces on every returned
	// result can never be corrected later.
	if err := result.Validate(); err != nil {
		return fmt.Errorf("memoryinvestigation: invalid investigation result: %w", err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("memoryinvestigation: marshal investigation result: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, found := s.results[resultID]; found {
		// M1 (Codex adversarial review, CHAOS-3755): the conflict check
		// is org-scoped FIRST, independent of content equality.
		// InvestigationResult carries no organization discriminator of
		// its own, so a byte-identical replay from a DIFFERENT org would
		// otherwise pass the content-equality check below and be treated
		// as a successful idempotent replay, while the row still belongs
		// to whichever org wrote it first.
		if existing.orgID != orgID {
			return fmt.Errorf("memoryinvestigation: investigation result %q already exists under a different organization", resultID)
		}
		if bytes.Equal(existing.payload, payload) {
			return nil
		}
		return fmt.Errorf("memoryinvestigation: investigation result %q already exists with different content", resultID)
	}
	s.results[resultID] = entry{orgID: orgID, payload: payload}
	return nil
}

// Get returns the InvestigationResult for resultID, scoped to
// principal.OrgID. A result stored under a different organization is
// reported identically to an unknown result_id (ErrNotFound); this
// package never allows result_id alone to satisfy a lookup.
func (s *Store) Get(ctx context.Context, principal storage.Principal, resultID string) (contextfabric.InvestigationResult, error) {
	if s == nil {
		return contextfabric.InvestigationResult{}, errors.New("memoryinvestigation: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.InvestigationResult{}, err
	}
	orgID := strings.TrimSpace(principal.OrgID)
	resultID = strings.TrimSpace(resultID)
	if orgID == "" || resultID == "" {
		return contextfabric.InvestigationResult{}, ErrNotFound
	}

	s.mu.Lock()
	stored, found := s.results[resultID]
	s.mu.Unlock()
	if !found || stored.orgID != orgID {
		return contextfabric.InvestigationResult{}, ErrNotFound
	}

	var result contextfabric.InvestigationResult
	if err := json.Unmarshal(stored.payload, &result); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("memoryinvestigation: decode investigation result: %w", err)
	}
	// M2 (Codex adversarial review, CHAOS-3755): validate on read too, not
	// just on write. Save already rejects an invalid result before it is
	// stored, but Get defends independently against any row that reached
	// storage some other way (e.g. written directly, or by a future/older
	// binary with different validation) -- a caller must never receive a
	// result this package cannot vouch for.
	if err := result.Validate(); err != nil {
		return contextfabric.InvestigationResult{}, fmt.Errorf("memoryinvestigation: stored investigation result is invalid: %w", err)
	}
	return result, nil
}
