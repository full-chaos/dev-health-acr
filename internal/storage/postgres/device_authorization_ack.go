package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const deviceAckRevokeActor = "device-credential-ack"

func nullableText(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// AcknowledgeCredential marks the credential of an ackable device redemption
// as received by the client. It takes the authorization row lock first, so it
// and the revoke sweep serialize on the same row: an acknowledgement is
// accepted only while the credential is still live and inside the ack window,
// and an acknowledgement that was already recorded is returned unchanged.
func (s *DeviceAuthorizationStore) AcknowledgeCredential(ctx context.Context, orgID, credentialID string) (time.Time, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return time.Time{}, err
	}
	if !uuidPattern.MatchString(orgID) || credentialID == "" {
		return time.Time{}, storage.ErrInvalidDeviceAuthorization
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("begin device credential acknowledgement: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	var redeemedAt time.Time
	var ackedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `
SELECT redeemed_at, credential_acked_at FROM acr.device_authorizations
WHERE redeemed_credential_id = $2 AND authorized_org_id = $1::uuid
  AND state = 'redeemed' AND ack_required
FOR UPDATE`, orgID, credentialID).Scan(&redeemedAt, &ackedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, storage.ErrDeviceAuthorizationNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("lock device authorization for acknowledgement: %w", sanitizeDatabaseError(err))
	}
	if ackedAt.Valid {
		return ackedAt.Time.UTC(), nil
	}
	if !now.Before(redeemedAt.Add(storage.DeviceCredentialAckWindow)) {
		return time.Time{}, storage.ErrDeviceAuthorizationNotFound
	}
	var revokedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM acr.client_credentials WHERE org_id = $1 AND credential_id = $2`, orgID, credentialID).Scan(&revokedAt); err != nil {
		return time.Time{}, fmt.Errorf("read credential for acknowledgement: %w", sanitizeDatabaseError(err))
	}
	if revokedAt.Valid {
		return time.Time{}, storage.ErrDeviceAuthorizationNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE acr.device_authorizations SET credential_acked_at = $2 WHERE redeemed_credential_id = $1`, credentialID, now); err != nil {
		return time.Time{}, fmt.Errorf("acknowledge device credential: %w", sanitizeDatabaseError(err))
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, fmt.Errorf("commit device credential acknowledgement: %w", sanitizeDatabaseError(err))
	}
	return now, nil
}

// RevokeUnacknowledged revokes the credentials whose ack window has elapsed.
// Rows are locked SKIP LOCKED so concurrent pods split the batch, and the
// revocation commits with the row lock, so an acknowledgement and the sweep
// cannot both win.
func (s *DeviceAuthorizationStore) RevokeUnacknowledged(ctx context.Context, limit int) (int, error) {
	if err := s.readyDeviceAuthorization(ctx); err != nil {
		return 0, err
	}
	if limit <= 0 {
		return 0, storage.ErrInvalidDeviceAuthorization
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin device credential revoke sweep: %w", sanitizeDatabaseError(err))
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	rows, err := tx.QueryContext(ctx, `
SELECT d.authorized_org_id::text, d.redeemed_credential_id
FROM acr.device_authorizations d
JOIN acr.client_credentials c ON c.credential_id = d.redeemed_credential_id
WHERE d.state = 'redeemed' AND d.ack_required AND d.credential_acked_at IS NULL
  AND d.redeemed_at <= $1 AND c.revoked_at IS NULL
ORDER BY d.redeemed_at
LIMIT $2
FOR UPDATE OF d SKIP LOCKED`, now.Add(-storage.DeviceCredentialAckWindow), limit)
	if err != nil {
		return 0, fmt.Errorf("select unacknowledged device credentials: %w", sanitizeDatabaseError(err))
	}
	type target struct{ orgID, credentialID string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.orgID, &t.credentialID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan unacknowledged device credential: %w", sanitizeDatabaseError(err))
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("read unacknowledged device credentials: %w", sanitizeDatabaseError(err))
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close unacknowledged device credentials: %w", sanitizeDatabaseError(err))
	}
	for _, t := range targets {
		if err := s.revokeUnacknowledgedTx(ctx, tx, t.orgID, t.credentialID, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit device credential revoke sweep: %w", sanitizeDatabaseError(err))
	}
	return len(targets), nil
}

func (s *DeviceAuthorizationStore) revokeUnacknowledgedTx(ctx context.Context, tx *sql.Tx, orgID, credentialID string, now time.Time) error {
	credential, err := lockedCredential(ctx, tx, orgID, credentialID)
	if err != nil {
		return err
	}
	if credential.RevokedAt != nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE acr.client_credentials SET revoked_at = $3
WHERE org_id = $1 AND credential_id = $2 AND revoked_at IS NULL`, orgID, credentialID, now); err != nil {
		return fmt.Errorf("revoke unacknowledged device credential: %w", sanitizeDatabaseError(err))
	}
	credential.RevokedAt = cloneTime(&now)
	input := storage.CredentialRevocationInput{
		OrgID: orgID, CredentialID: credentialID, ActorID: deviceAckRevokeActor,
		ActorType: "system", Reason: "device credential not acknowledged",
	}
	return s.audit.record(ctx, tx, withRevocationDetails(credentialRevokedEvent(credential, deviceAckRevokeActor, now), input))
}
