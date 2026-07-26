package memory

import (
	"context"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type DeviceAuthorizationStoreOptions struct {
	Credentials *storage.CredentialLifecycle
	Now         func() time.Time
}

type DeviceAuthorizationStore struct {
	mu          sync.Mutex
	credentials *storage.CredentialLifecycle
	now         func() time.Time
	byDevice    map[storage.DeviceCodeHash]storage.DeviceAuthorization
	byUser      map[storage.UserCodeHash]storage.DeviceCodeHash
}

func NewDeviceAuthorizationStore(options DeviceAuthorizationStoreOptions) (*DeviceAuthorizationStore, error) {
	if options.Credentials == nil || options.Now == nil {
		return nil, storage.ErrInvalidDeviceAuthorization
	}
	if err := options.Credentials.Validate(); err != nil {
		return nil, err
	}
	return &DeviceAuthorizationStore{
		credentials: options.Credentials,
		now:         options.Now,
		byDevice:    make(map[storage.DeviceCodeHash]storage.DeviceAuthorization),
		byUser:      make(map[storage.UserCodeHash]storage.DeviceCodeHash),
	}, nil
}

func (s *DeviceAuthorizationStore) Create(ctx context.Context, input storage.DeviceAuthorizationCreateInput) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if input.DeviceCodeHash.IsZero() || input.UserCodeHash.IsZero() || storage.ValidateDeviceAuthorizationHints(input) != nil {
		return storage.DeviceAuthorization{}, storage.ErrInvalidDeviceAuthorization
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if _, exists := s.byDevice[input.DeviceCodeHash]; exists {
		return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, storage.DeviceAuthorizationStatePending, 0)
	}
	if _, exists := s.byUser[input.UserCodeHash]; exists {
		return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, storage.DeviceAuthorizationStatePending, 0)
	}
	now := s.now().UTC()
	record := storage.DeviceAuthorization{
		DeviceCodeHash:     input.DeviceCodeHash,
		UserCodeHash:       input.UserCodeHash,
		OrganizationIDHint: input.OrganizationIDHint,
		RepositoryHints:    append([]string(nil), input.RepositoryHints...),
		State:              storage.DeviceAuthorizationStatePending,
		ExpiresAt:          now.Add(storage.DeviceAuthorizationTTL),
		PollInterval:       storage.DeviceAuthorizationPollInterval,
		CreatedAt:          now,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization,
	}
	s.byDevice[input.DeviceCodeHash] = record
	s.byUser[input.UserCodeHash] = input.DeviceCodeHash
	return cloneDeviceAuthorization(record), nil
}

func (s *DeviceAuthorizationStore) GetByDeviceCodeHash(ctx context.Context, hash storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deviceLocked(hash)
}

func (s *DeviceAuthorizationStore) GetByUserCodeHash(ctx context.Context, hash storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deviceHash, exists := s.byUser[hash]
	if !exists {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	return s.deviceLocked(deviceHash)
}

func (s *DeviceAuthorizationStore) Preview(ctx context.Context, hash storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deviceHash, exists := s.byUser[hash]
	if !exists {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	record, exists := s.byDevice[deviceHash]
	if !exists {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	if !s.now().UTC().Before(record.ExpiresAt) && (record.State == storage.DeviceAuthorizationStatePending || record.State == storage.DeviceAuthorizationStateApproved || record.State == storage.DeviceAuthorizationStateExpired) {
		return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorExpired, storage.DeviceAuthorizationStateExpired, 0)
	}
	return cloneDeviceAuthorization(record), nil
}

func (s *DeviceAuthorizationStore) Poll(ctx context.Context, hash storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.deviceLocked(hash)
	if err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if record.State.Terminal() {
		return record, nil
	}
	now := s.now().UTC()
	if record.LastPollAt != nil {
		next := record.LastPollAt.Add(record.PollInterval)
		if now.Before(next) {
			return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorPollTooSoon, record.State, next.Sub(now))
		}
	}
	record.LastPollAt = ptrTime(now)
	s.byDevice[hash] = cloneDeviceAuthorization(record)
	return cloneDeviceAuthorization(record), nil
}

func (s *DeviceAuthorizationStore) Approve(ctx context.Context, hash storage.UserCodeHash, grant storage.DeviceAuthorizationGrant) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if err := storage.ValidateDeviceAuthorizationGrant(grant); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deviceHash, exists := s.byUser[hash]
	if !exists {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	record, err := s.deviceLocked(deviceHash)
	if err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if record.State != storage.DeviceAuthorizationStatePending {
		return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	now := s.now().UTC()
	record.State = storage.DeviceAuthorizationStateApproved
	record.AuthorizedOrgID = grant.OrgID
	record.AuthorizedRepositoryScopes = append([]string(nil), grant.RepositoryScopes...)
	record.AuthorizedScopes = append([]string(nil), grant.Scopes...)
	record.ApprovingSubject = grant.ApprovingSubject
	record.ApprovingAuthenticationMethod = grant.ApprovingAuthenticationMethod
	record.ApprovedAt = ptrTime(now)
	s.byDevice[deviceHash] = cloneDeviceAuthorization(record)
	return cloneDeviceAuthorization(record), nil
}

func (s *DeviceAuthorizationStore) Deny(ctx context.Context, hash storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.ready(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deviceHash, exists := s.byUser[hash]
	if !exists {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	record, err := s.deviceLocked(deviceHash)
	if err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if record.State != storage.DeviceAuthorizationStatePending {
		return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	record.State = storage.DeviceAuthorizationStateDenied
	s.byDevice[deviceHash] = cloneDeviceAuthorization(record)
	return cloneDeviceAuthorization(record), nil
}

func (s *DeviceAuthorizationStore) deviceLocked(hash storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	record, exists := s.byDevice[hash]
	if !exists {
		return storage.DeviceAuthorization{}, storage.ErrDeviceAuthorizationNotFound
	}
	now := s.now().UTC()
	if !now.Before(record.ExpiresAt) {
		switch record.State {
		case storage.DeviceAuthorizationStatePending, storage.DeviceAuthorizationStateApproved:
			record.State = storage.DeviceAuthorizationStateExpired
			s.byDevice[hash] = cloneDeviceAuthorization(record)
		case storage.DeviceAuthorizationStateExpired:
		case storage.DeviceAuthorizationStateDenied, storage.DeviceAuthorizationStateRedeemed:
			return cloneDeviceAuthorization(record), nil
		}
	}
	if record.State == storage.DeviceAuthorizationStateExpired {
		return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorExpired, record.State, 0)
	}
	return cloneDeviceAuthorization(record), nil
}

func (s *DeviceAuthorizationStore) ready(ctx context.Context) error {
	if s == nil || s.credentials == nil || s.now == nil || s.byDevice == nil || s.byUser == nil || storage.IsNil(ctx) {
		return storage.ErrInvalidDeviceAuthorization
	}
	return ctx.Err()
}

func cloneDeviceAuthorization(record storage.DeviceAuthorization) storage.DeviceAuthorization {
	record.RepositoryHints = append([]string(nil), record.RepositoryHints...)
	record.AuthorizedRepositoryScopes = append([]string(nil), record.AuthorizedRepositoryScopes...)
	record.AuthorizedScopes = append([]string(nil), record.AuthorizedScopes...)
	record.LastPollAt = cloneTime(record.LastPollAt)
	record.ApprovedAt = cloneTime(record.ApprovedAt)
	record.RedeemedAt = cloneTime(record.RedeemedAt)
	return record
}
