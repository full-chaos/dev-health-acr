package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/internal/credentiallifecycle"
)

type credentialStore struct {
	mu     sync.RWMutex
	audit  *AuditStore
	byID   map[string]storage.CredentialRecord
	byHash map[string]string
	now    func() time.Time
}

type CredentialStoreOptions struct {
	Audit *AuditStore
	Now   func() time.Time
}

func NewCredentialStore(audits ...*AuditStore) (*storage.CredentialLifecycle, error) {
	if len(audits) > 1 {
		return nil, fmt.Errorf("%w: at most one audit store", storage.ErrInvalidCredentialLifecycle)
	}
	audit := NewAuditStore()
	if len(audits) == 1 {
		audit = audits[0]
	}
	_, lifecycle, err := newCredentialStore(audit, time.Now)
	return lifecycle, err
}

func NewCredentialStoreWithOptions(options CredentialStoreOptions) (*storage.CredentialLifecycle, error) {
	_, lifecycle, err := newCredentialStore(options.Audit, options.Now)
	return lifecycle, err
}

func newCredentialStore(audit *AuditStore, now func() time.Time) (*credentialStore, *storage.CredentialLifecycle, error) {
	if audit == nil || now == nil {
		return nil, nil, storage.ErrInvalidCredentialLifecycle
	}
	store := &credentialStore{
		audit: audit, byID: make(map[string]storage.CredentialRecord), byHash: make(map[string]string), now: now,
	}
	if err := audit.bindLifecycle(store); err != nil {
		return nil, nil, err
	}
	lifecycle, err := credentiallifecycle.New(credentiallifecycle.Backend{
		Store: store, Create: store.createCredential, Rotate: store.rotateCredential, Revoke: store.revokeCredential, Rollback: store.rollbackCredentialRotation,
	})
	if err != nil {
		return nil, nil, err
	}
	return store, lifecycle, nil
}

func (s *credentialStore) List(ctx context.Context, orgID string) ([]contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

func (s *credentialStore) GetByID(ctx context.Context, orgID, credentialID string) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	record, ok := s.byID[credentialID]
	if !ok || record.Metadata.OrgID != orgID {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	return cloneCredential(record.Metadata), nil
}

func (s *credentialStore) FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	if err := s.ready(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	credentialID, ok := s.byHash[tokenHash]
	if !ok {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	record, ok := s.byID[credentialID]
	now := s.now().UTC()
	if !ok || record.Metadata.RevokedAt != nil || record.Metadata.ExpiresAt != nil && !record.Metadata.ExpiresAt.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrNotFound
	}
	return cloneCredential(record.Metadata), nil
}

func (s *credentialStore) TouchLastUsed(ctx context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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

func (s *credentialStore) ready(ctx context.Context) error {
	if s == nil || s.audit == nil || s.byID == nil || s.byHash == nil || s.now == nil || ctx == nil {
		return storage.ErrInvalidCredentialLifecycle
	}
	return ctx.Err()
}
