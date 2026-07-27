package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *DeviceAuthorizationStore) Approve(ctx context.Context, hash storage.UserCodeHash, grant storage.DeviceAuthorizationGrant) (storage.DeviceAuthorization, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	if err := storage.ValidateDeviceAuthorizationGrant(grant); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	repositories, err := json.Marshal(grant.RepositoryScopes)
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("encode authorized repositories: %w", err)
	}
	scopes, err := json.Marshal(grant.Scopes)
	if err != nil {
		return storage.DeviceAuthorization{}, fmt.Errorf("encode authorized scopes: %w", err)
	}
	now := s.now().UTC()
	if err := expireDeviceAuthorization(ctx, s.DB, "user_code_hash", hash.String(), now); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
UPDATE acr.device_authorizations
SET state = 'approved', authorized_org_id = $3::uuid,
    authorized_repository_scopes = $4::jsonb, authorized_scopes = $5::jsonb,
    approving_subject = $6, approving_authentication_method = $7, approved_at = $2
WHERE user_code_hash = $1 AND state = 'pending' AND expires_at > $2
RETURNING `+deviceAuthorizationColumns,
		hash.String(), now, grant.OrgID, string(repositories), string(scopes),
		grant.ApprovingSubject, grant.ApprovingAuthenticationMethod,
	)
	record, err := scanDeviceAuthorization(row)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.DeviceAuthorization{}, fmt.Errorf("approve device authorization: %w", sanitizeDatabaseError(err))
	}
	return s.transitionConflictByUser(ctx, hash)
}

func (s *DeviceAuthorizationStore) Deny(ctx context.Context, hash storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	now := s.now().UTC()
	if err := expireDeviceAuthorization(ctx, s.DB, "user_code_hash", hash.String(), now); err != nil {
		return storage.DeviceAuthorization{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
UPDATE acr.device_authorizations
SET state = 'denied'
WHERE user_code_hash = $1 AND state = 'pending' AND expires_at > $2
RETURNING `+deviceAuthorizationColumns, hash.String(), now)
	record, err := scanDeviceAuthorization(row)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.DeviceAuthorization{}, fmt.Errorf("deny device authorization: %w", sanitizeDatabaseError(err))
	}
	return s.transitionConflictByUser(ctx, hash)
}

func (s *DeviceAuthorizationStore) transitionConflictByUser(ctx context.Context, hash storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	record, err := s.GetByUserCodeHash(ctx, hash)
	if err != nil {
		return storage.DeviceAuthorization{}, err
	}
	return storage.DeviceAuthorization{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
}

func expireDeviceAuthorization(ctx context.Context, executor execer, column, hash string, now time.Time) error {
	var query string
	switch column {
	case "device_code_hash":
		query = `UPDATE acr.device_authorizations SET state = 'expired'
WHERE device_code_hash = $1 AND state IN ('pending', 'approved') AND expires_at <= $2`
	case "user_code_hash":
		query = `UPDATE acr.device_authorizations SET state = 'expired'
WHERE user_code_hash = $1 AND state IN ('pending', 'approved') AND expires_at <= $2`
	default:
		return storage.ErrInvalidDeviceAuthorization
	}
	if _, err := executor.ExecContext(ctx, query, hash, now); err != nil {
		return fmt.Errorf("expire device authorization: %w", sanitizeDatabaseError(err))
	}
	return nil
}
