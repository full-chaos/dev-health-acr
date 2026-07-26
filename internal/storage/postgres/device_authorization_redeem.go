package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func (s *DeviceAuthorizationStore) Redeem(ctx context.Context, hash storage.DeviceCodeHash, input storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("begin device authorization redemption: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	if err := expireDeviceAuthorization(ctx, tx, "device_code_hash", hash.String(), now); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	row := tx.QueryRowContext(ctx, `SELECT `+deviceAuthorizationColumns+`
FROM acr.device_authorizations WHERE device_code_hash = $1 FOR UPDATE`, hash.String())
	record, err := scanDeviceAuthorization(row)
	if errors.Is(err, sql.ErrNoRows) {
		return contractsv1.ClientCredential{}, storage.ErrDeviceAuthorizationNotFound
	}
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("lock device authorization for redemption: %w", sanitizeDatabaseError(err))
	}
	if record.State == storage.DeviceAuthorizationStateExpired {
		return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorExpired, record.State, 0)
	}
	if record.State != storage.DeviceAuthorizationStateApproved {
		return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	input.IssuanceProvenance = storage.CredentialIssuanceProvenanceDeviceAuthorization
	if err := storage.ValidateCredentialCreateInput(input); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if !storage.DeviceAuthorizationCredentialMatches(record, input) || !uuidPattern.MatchString(input.OrgID) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidDeviceAuthorization
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return contractsv1.ClientCredential{}, storage.ErrInvalidCredentialInput
	}
	credential := credentialFromCreate(input, now)
	credentialRecord := storage.CredentialRecord{
		Metadata: credential, TokenHash: input.TokenHash, CreatedBy: input.ActorID,
		IssuanceProvenance: input.IssuanceProvenance,
	}
	if err := insertCredential(ctx, tx, credentialRecord); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	if err := s.audit.record(ctx, tx, credentialCreatedEvent(credentialRecord)); err != nil {
		return contractsv1.ClientCredential{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE acr.device_authorizations
SET state = 'redeemed', redeemed_at = $2, redeemed_credential_id = $3
WHERE device_code_hash = $1 AND state = 'approved' AND expires_at > $2`,
		hash.String(), now, credential.CredentialID,
	)
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("redeem device authorization: %w", sanitizeDatabaseError(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("device authorization redemption rows affected: %w", sanitizeDatabaseError(err))
	}
	if rows != 1 {
		return contractsv1.ClientCredential{}, storage.NewDeviceAuthorizationError(storage.DeviceAuthorizationErrorConflict, record.State, 0)
	}
	if err := tx.Commit(); err != nil {
		return contractsv1.ClientCredential{}, fmt.Errorf("commit device authorization redemption: %w", sanitizeDatabaseError(err))
	}
	return credential, nil
}
