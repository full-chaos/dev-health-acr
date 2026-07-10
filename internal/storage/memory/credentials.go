package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CredentialStore is an in-memory implementation for tests, local demos, and
// interface development. It intentionally models the same atomic lifecycle
// semantics expected from the PostgreSQL adapter.
type CredentialStore struct {
	mu     sync.RWMutex
	byID   map[string]storage.CredentialRecord
	byHash map[string]string
}

func NewCredentialStore() *CredentialStore {
	return &CredentialStore{
		byID:   make(map[string]storage.CredentialRecord),
		byHash: make(map[string]string),
	}
}

func (s *CredentialStore) Create(_ context.Context, record storage.CredentialRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[record.Metadata.CredentialID]; exists {
		return storage.ErrConflict
	}
	if _, exists := s.byHash[record.TokenHash]; exists {
		return storage.ErrConflict
	}
	record = cloneRecord(record)
	s.byID[record.Metadata.CredentialID] = record
	s.byHash[record.TokenHash] = record.Metadata.CredentialID
	return nil
}

func (s *CredentialStore) List(_ context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]contractsv1.ClientCredential, 0)
	for _, record := range s.byID {
		if record.Metadata.OrgID == orgID {
			values = append(values, cloneCredential(record.Metadata))
		}
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values, nil
}

func (s *CredentialStore) GetByID(_ context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.byID[credentialID]
	if !ok || record.Metadata.OrgID != orgID {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	return cloneCredential(record.Metadata), nil
}

func (s *CredentialStore) FindByTokenHash(_ context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	credentialID, ok := s.byHash[tokenHash]
	if !ok {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	record, ok := s.byID[credentialID]
	if !ok {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	return cloneCredential(record.Metadata), nil
}

func (s *CredentialStore) Rotate(_ context.Context, orgID, credentialID string, replacement storage.CredentialRecord, previousValidUntil *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[credentialID]
	if !ok || old.Metadata.OrgID != orgID {
		return storage.ErrNotFound
	}
	if _, exists := s.byID[replacement.Metadata.CredentialID]; exists {
		return storage.ErrConflict
	}
	if _, exists := s.byHash[replacement.TokenHash]; exists {
		return storage.ErrConflict
	}

	cutover := replacement.Metadata.CreatedAt
	if previousValidUntil == nil || !previousValidUntil.After(cutover) {
		old.Metadata.RevokedAt = ptrTime(cutover)
	} else if old.Metadata.ExpiresAt == nil || previousValidUntil.Before(*old.Metadata.ExpiresAt) {
		old.Metadata.ExpiresAt = ptrTime(*previousValidUntil)
	}
	s.byID[credentialID] = cloneRecord(old)

	replacement = cloneRecord(replacement)
	s.byID[replacement.Metadata.CredentialID] = replacement
	s.byHash[replacement.TokenHash] = replacement.Metadata.CredentialID
	return nil
}

func (s *CredentialStore) Revoke(_ context.Context, orgID, credentialID string, revokedAt time.Time) (contractsv1.ClientCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[credentialID]
	if !ok || record.Metadata.OrgID != orgID {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	if record.Metadata.RevokedAt == nil || revokedAt.Before(*record.Metadata.RevokedAt) {
		record.Metadata.RevokedAt = ptrTime(revokedAt)
	}
	s.byID[credentialID] = cloneRecord(record)
	return cloneCredential(record.Metadata), nil
}

func (s *CredentialStore) TouchLastUsed(_ context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[credentialID]
	if !ok {
		return storage.ErrNotFound
	}
	if record.Metadata.LastUsedAt == nil || usedAt.After(*record.Metadata.LastUsedAt) {
		record.Metadata.LastUsedAt = ptrTime(usedAt)
		record.LastUsedIP = ip
		record.LastUsedUserAgent = userAgent
		s.byID[credentialID] = cloneRecord(record)
	}
	return nil
}

// RecordForTest returns the private record for same-package and integration
// tests. Production code must only consume the CredentialStore interface.
func (s *CredentialStore) RecordForTest(credentialID string) (storage.CredentialRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.byID[credentialID]
	return cloneRecord(record), ok
}

func cloneRecord(record storage.CredentialRecord) storage.CredentialRecord {
	record.Metadata = cloneCredential(record.Metadata)
	return record
}

func cloneCredential(value contractsv1.ClientCredential) contractsv1.ClientCredential {
	value.RepositoryScopes = append([]string(nil), value.RepositoryScopes...)
	value.Scopes = append([]string(nil), value.Scopes...)
	value.ExpiresAt = cloneTime(value.ExpiresAt)
	value.RevokedAt = cloneTime(value.RevokedAt)
	value.LastUsedAt = cloneTime(value.LastUsedAt)
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ptrTime(value time.Time) *time.Time {
	copy := value
	return &copy
}
