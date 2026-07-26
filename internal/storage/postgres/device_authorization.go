package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type DeviceAuthorizationStoreOptions struct {
	Now func() time.Time
}

type DeviceAuthorizationStore struct {
	DB    *sql.DB
	audit *AuditStore
	now   func() time.Time
}

func NewDeviceAuthorizationStore(db *sql.DB, audit *AuditStore) (*DeviceAuthorizationStore, error) {
	return NewDeviceAuthorizationStoreWithOptions(db, audit, DeviceAuthorizationStoreOptions{Now: time.Now})
}

func NewDeviceAuthorizationStoreWithOptions(db *sql.DB, audit *AuditStore, options DeviceAuthorizationStoreOptions) (*DeviceAuthorizationStore, error) {
	if db == nil || audit == nil || audit.DB != db || audit.GenerateID == nil || options.Now == nil {
		return nil, storage.ErrInvalidDeviceAuthorization
	}
	return &DeviceAuthorizationStore{DB: db, audit: audit, now: options.Now}, nil
}

func (s *DeviceAuthorizationStore) Create(ctx context.Context, input storage.DeviceAuthorizationCreateInput) (storage.DeviceAuthorization, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if input.DeviceCodeHash.IsZero() || input.UserCodeHash.IsZero() {
		return storage.DeviceAuthorization{}, storage.ErrInvalidDeviceAuthorization
	}
	now := s.now().UTC()
	record := storage.DeviceAuthorization{
		DeviceCodeHash:     input.DeviceCodeHash,
		UserCodeHash:       input.UserCodeHash,
		State:              storage.DeviceAuthorizationStatePending,
		CreatedAt:          now,
		ExpiresAt:          now.Add(storage.DeviceAuthorizationTTL),
		PollInterval:       storage.DeviceAuthorizationPollInterval,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization,
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO acr.device_authorizations (
    device_code_hash, user_code_hash, state, created_at, expires_at,
    poll_interval_seconds, issuance_provenance
) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		record.DeviceCodeHash.String(), record.UserCodeHash.String(), record.State,
		record.CreatedAt, record.ExpiresAt, int(record.PollInterval.Seconds()), record.IssuanceProvenance,
	)
	if err != nil {
		sanitized := sanitizeDatabaseError(err)
		if errors.Is(sanitized, storage.ErrConflict) {
			return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, storage.DeviceAuthorizationStatePending, 0)
		}
		return storage.DeviceAuthorization{}, fmt.Errorf("create device authorization: %w", sanitized)
	}
	return record, nil
}

func (s *DeviceAuthorizationStore) GetByDeviceCodeHash(ctx context.Context, hash storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	now := s.now().UTC()
	if err := expireDeviceAuthorization(ctx, s.DB, "device_code_hash", hash.String(), now); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+deviceAuthorizationColumns+`
FROM acr.device_authorizations WHERE device_code_hash = $1`, hash.String())
	record, err := scanDeviceAuthorization(row)
	return mapDeviceAuthorizationLookup("get device authorization by device code", record, err)
}

func (s *DeviceAuthorizationStore) GetByUserCodeHash(ctx context.Context, hash storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	now := s.now().UTC()
	if err := expireDeviceAuthorization(ctx, s.DB, "user_code_hash", hash.String(), now); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+deviceAuthorizationColumns+`
FROM acr.device_authorizations WHERE user_code_hash = $1`, hash.String())
	record, err := scanDeviceAuthorization(row)
	return mapDeviceAuthorizationLookup("get device authorization by user code", record, err)
}

func (s *DeviceAuthorizationStore) Poll(ctx context.Context, hash storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	now := s.now().UTC()
	if err := expireDeviceAuthorization(ctx, s.DB, "device_code_hash", hash.String(), now); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
UPDATE acr.device_authorizations
SET last_poll_at = $2
WHERE device_code_hash = $1
  AND state IN ('pending', 'approved')
  AND expires_at > $2
  AND (last_poll_at IS NULL OR last_poll_at + poll_interval_seconds * INTERVAL '1 second' <= $2)
RETURNING `+deviceAuthorizationColumns, hash.String(), now)
	record, err := scanDeviceAuthorization(row)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.DeviceAuthorization{}, fmt.Errorf("poll device authorization: %w", sanitizeDatabaseError(err))
	}
	record, err = s.GetByDeviceCodeHash(ctx, hash)
	if err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if record.State.Terminal() {
		return record, nil
	}
	retryAfter := record.PollInterval
	if record.LastPollAt != nil {
		retryAfter = record.LastPollAt.Add(record.PollInterval).Sub(now)
	}
	return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorPollTooSoon, record.State, retryAfter)
}

func (s *DeviceAuthorizationStore) readyDeviceAuthorization(ctx context.Context) error {
	if s == nil || s.DB == nil || s.audit == nil || s.audit.DB != s.DB || s.audit.GenerateID == nil || s.now == nil || storage.IsNil(ctx) {
		return storage.ErrInvalidDeviceAuthorization
	}
	return ctx.Err()
}
